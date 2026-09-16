package tools

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/term"
)

// The shell tool's synchronous execution runs inside one persistent
// interactive terminal per Harness process: the user's real shell in a
// pseudo-terminal (internal/term), kept alive for the whole session.
// Working directory, environment, virtualenvs and the sudo timestamp
// survive across calls; commands that would hang a pipe-based runner
// (interactive prompts, REPLs, TUIs) just run in the terminal.
//
// Exit codes and the working directory are recovered from a sentinel
// the shell prints after each single-line command. Credential
// prompts (sudo, su, ssh passphrases, docker login, any hidden-line
// reader) are detected in the output stream and in the terminal's
// input discipline, and answered through a masked TUI prompt while the
// tool call is still blocked, so the password goes from the user
// straight to the terminal and never enters the model's context - the
// model sees only the command's result, as if nothing had been asked.
// After the first authentication a sudo credential stays valid on the
// session's tty.

const (
	// ptyStartupMs is the quiet window that ends shell startup output
	// (prompt, motd, rc noise) before the first command.
	ptyStartupMs = 800
	// ptySettleMs is how long Input waits for output to go quiet
	// before returning the screen.
	ptySettleMs = 600
	// ptyRestartDelay throttles session restarts after a shell exit.
	ptyRestartDelay = time.Second

	// ptyAltSettleMs is how long the runner waits after a program takes
	// the alternate screen before rendering it, so the first frame is a
	// drawn UI rather than a half-painted one.
	ptyAltSettleMs = 250
	// ptyKeyGap paces named keys: sent as one burst, a leading escape
	// reads as a meta prefix and fast programs drop the tail.
	ptyKeyGap = 25 * time.Millisecond

	// ptyQuietMs is the silence window after which the runner stops
	// waiting blindly and looks at what the foreground job is doing:
	// blocked on the terminal means the command is waiting for input.
	ptyQuietMs = 1500
	// ptyEchoSettleMs is how long output that merely looks like a
	// credential prompt gets for the line discipline to confirm it:
	// hidden-line readers clear echo before printing their prompt, but
	// a slow one may still be mid-switch when its text is on the wire.
	// It is also how long a raw-mode read gets to turn out to be the
	// shell's own line editor - the prompt marker arriving behind text
	// a finished command printed.
	ptyEchoSettleMs = 400
	// ptyBlindQuietMs is the silence a job has to keep before it is
	// reported as waiting for input when the kernel will not say where
	// it sleeps (a setuid program: sudo, su). Such a job could as well
	// be a credential reader sitting in PAM's fail delay, about to
	// reject the answer and ask again; a couple of seconds tells the two
	// apart, where the ordinary quiet window would call it a question.
	ptyBlindQuietMs = 4000
	// ptySettleForPromptMs is the grace given to a just-finished
	// command's prompt to land after the quiet window trips, before the
	// silence is read as anything else.
	ptySettleForPromptMs = 300
	// ptyFenceWait is how long the fence marker gets to come back
	// before a call gives up on clearing the session and runs anyway: a
	// session that does not answer has something in it, which is what
	// the run did unconditionally before the fence existed.
	ptyFenceWait = 2 * time.Second
	// ptyFenceSettle is the grace the prompt following the fence gets to
	// land, so it is drained along with the fence instead of being left
	// behind to satisfy the wait for the command that comes next.
	ptyFenceSettle = 300 * time.Millisecond
	// ptyFenceQuietMs is the quiet window the fence's wait for the
	// prompt's mode tail uses: bracketed-paste enable and friends follow
	// the prompt marker by a write or two, and prompts that send none
	// end the wait on silence instead.
	ptyFenceQuietMs = 20
	// ptyMaxWait bounds how long one call keeps leasing patience
	// forward to a command that is still producing output, so a stream
	// that never ends cannot hold a call forever.
	ptyMaxWait = 15 * time.Minute
)

// sentinel is one session's completion marker: the command that prints
// it, the pattern that parses it, and the loose pattern used to wait
// for it while its digits and path are still streaming in.
type sentinel struct {
	cmd   string
	parse *regexp.Regexp
	loose *regexp.Regexp
	// begin is the fence a call prints before its command, and beginRe
	// the pattern that recognises it once it comes back.
	begin   string
	beginRe *regexp.Regexp
}

func newSentinel(d shellDialect) sentinel {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A predictable tag still beats no tag: the point is to be
		// unlike anything a command prints, not to be unguessable.
		binary.BigEndian.PutUint64(b[:], uint64(time.Now().UnixNano()))
	}
	tag := hex.EncodeToString(b[:])
	return sentinel{
		cmd: fmt.Sprintf(d.sentinelCmd, tag),
		// The greedy (.*) takes everything up to the final "__" on the
		// line, so working directories containing underscores parse.
		parse:   regexp.MustCompile(fmt.Sprintf(d.sentinelRe, tag)),
		loose:   regexp.MustCompile(fmt.Sprintf(d.sentinelLoose, tag)),
		begin:   fmt.Sprintf(d.fenceCmd, tag),
		beginRe: regexp.MustCompile(fmt.Sprintf(d.fenceRe, tag)),
	}
}

var (
	// credPromptRe matches output that looks like a credential prompt:
	// sudo's in any locale (the "[sudo] ... :" shape survives
	// translation), or a generic password/passphrase/passcode/PIN
	// request. It is only a fast-path hint: a match is confirmed
	// against the terminal's input discipline (a hidden-line read,
	// term.SecretRead) before the masked prompt opens, so a program
	// that merely prints one of these words - with echo left on - is
	// not intercepted. Prompts the hint cannot read (other languages,
	// escape sequences interleaved with the text) are still caught by
	// the discipline check on the quiet window.
	//
	// A rejection line ("Sorry, try again.") is deliberately not in
	// here. It is printed before the reader re-prompts, so a dialog
	// opened on it collects an answer the reader is not asking for yet,
	// and the prompt that follows then reads as another question - two
	// dialogs, and a third when the answers run out. The retry is
	// caught where it belongs: by the prompt the reader prints next, or
	// by the hidden-line read it blocks in.
	credPromptRe = regexp.MustCompile(`(?i)(?:\[sudo\][^:\n]{0,60}:|` +
		`\b(?:password|passphrase|passcode|passwort|pass phrase|mot de passe|contraseña|pin)\b[^:\n]{0,64}:)`)
	// ptyPromptRe matches the OSC 133 prompt marker installed by
	// ptySetupCmd. Seeing it after a command means the shell - not some
	// program the command started - has control back.
	ptyPromptRe = regexp.MustCompile("\x1b\\]133;A")
	// ptyPromptSplitRe is the same marker with its terminator (BEL, or
	// the ST form some emulators send), so cleaning can replace the
	// whole sequence with the line break the prompt implies rather than
	// leaving the text around it joined together.
	ptyPromptSplitRe = regexp.MustCompile("\x1b\\]133;A(?:\x07|\x1b\\\\)?")
	// ptyPasteRe is the fallback prompt heuristic for a shell whose
	// prompt could not be replaced: bracketed-paste-enable, which POSIX
	// shells emit before a prompt. It over-matches (TUIs and REPLs send
	// it too), so it is only used together with the alt-screen guard.
	ptyPasteRe = regexp.MustCompile(`\x1b\[\?2004h`)
	// ptyAltScreenRe matches a program switching to the alternate
	// screen: an editor, pager or TUI has taken over the terminal.
	ptyAltScreenRe = regexp.MustCompile(`\x1b\[\?(?:1049|1047|47)h`)
)

// PTYResult is the outcome of a terminal command.
type PTYResult struct {
	// Output is the cleaned terminal output of the command: ANSI
	// stripped, echoed command and sentinel removed, everything after
	// the sentinel (the next prompt) truncated.
	Output string
	// ExitCode is the command's exit code from the sentinel. Nil when
	// the command was still running when the wait budget expired.
	ExitCode *int
	// Cwd is the session's working directory at command completion.
	Cwd string
	// Running reports whether the command had not completed when the
	// wait budget expired.
	Running bool
	// AltScreen reports that a full-screen program (editor, pager, TUI)
	// owns the terminal, so Output is a rendered screen instead of a
	// stream of new output.
	AltScreen bool
	// Unchanged reports that a poll found the screen exactly as the
	// previous call left it. Output is empty in that case.
	Unchanged bool
	// Interrupted reports that the caller gave up on the command and it
	// was stopped with ctrl-c.
	Interrupted bool
	// WhileBusy reports that this call looked at a session with a
	// command still running in it: the output is the screen as it
	// stands, and the command's own output goes to the call waiting on
	// it rather than here.
	WhileBusy bool
	// Waiting reports that the command had stopped and was blocked
	// reading the terminal when the call returned: it asked a question
	// and needs input or keys before anything else happens. There is no
	// exit code yet.
	Waiting bool
}

// ptyTerminal is the slice of term.Session the runner uses; it exists
// so tests can substitute a scripted terminal.
type ptyTerminal interface {
	Send(b []byte) error
	WaitForAny(ctx context.Context, patterns []*regexp.Regexp, timeout time.Duration) int
	WaitForAnyOrQuiet(ctx context.Context, patterns []*regexp.Regexp, quiet, timeout time.Duration) (int, bool)
	WaitForOutput(ctx context.Context, timeout time.Duration) bool
	WaitForQuiet(ctx context.Context, quiet, timeout time.Duration) bool
	Drain() []byte
	Pending() []byte
	PendingLen() int
	Alive() bool
	AltScreen() bool
	Screen() string
	BracketedPaste() bool
	Paste(text string) error
	Size() (rows, cols int)
	IdleFor() time.Duration
	SampleJob() term.JobActivity
	SecretRead() term.SecretReadState
	ResetWaitSample()
	RescanFromStart()
	Close()
}

// ptyRunner owns the single terminal session for one working directory
// and serializes access: commands run one at a time, like a person
// typing in a terminal.
type ptyRunner struct {
	// mu guards the runner's own state; it is held only for as long as
	// that takes. cmdMu is the one long-held lock: it serialises whole
	// commands, so a second command waits while the first is running -
	// but keystrokes and polls do not, which is what lets a prompt be
	// answered while the command that asked is still going.
	// sendMu keeps two writers from interleaving bytes on the wire.
	mu     sync.Mutex
	cmdMu  sync.Mutex
	sendMu sync.Mutex
	// key is the registry key (owner and directory) and cwd the
	// directory the shell started in.
	key string
	cwd string
	// lastCwd is the working directory the last completed command's
	// sentinel reported - the session's cwd as of then. Calls that run
	// no command (poll, keys, input) have no sentinel to read, so they
	// report this instead of the directory the shell started in.
	lastCwd string
	// announcedCwd is the directory the model was last told about. It
	// starts as the directory the shell opened in, which the system
	// prompt already states, so a session that never leaves it never
	// spends a line saying so.
	announcedCwd string
	ask          question.Service
	session      ptyTerminal

	startedAt time.Time
	lastUsed  time.Time
	lastEcho  []string
	// promptRe is how this session's shell announces it is back at a
	// prompt: the OSC 133 marker when the prompt could be replaced, the
	// bracketed-paste heuristic when it could not.
	promptRe *regexp.Regexp
	// lastScreen is the last rendered screen handed to the caller, so a
	// poll of an idle TUI can say "unchanged" instead of resending it.
	lastScreen string
	// sentinel is this session's completion marker.
	sentinel sentinel
	// running records that the last call left a program in the session
	// - a command still going, a REPL, a full-screen program - so the
	// next text typed goes to that program rather than being run as a
	// command, and a poll waits for it rather than snapshotting it.
	running bool
	// inFlight is set while a call is waiting on the session and owns
	// its output; a call typing meanwhile answers, it does not report.
	inFlight bool
	// restarted records that the shell had exited and a fresh one was
	// opened to serve the current call, so the caller can be told that
	// the state it built up (cd, exports, venv) is gone.
	restarted bool
}

var (
	ptyRunnersMu  sync.Mutex
	ptyRunners    = map[string]*ptyRunner{}
	ptyReaperOnce sync.Once
)

const (
	// ptyMaxRunners bounds concurrent terminal sessions per process -
	// across every working directory and every slot within one. Opening
	// past the cap evicts the most-idle session, mirroring the pty-mcp
	// max-sessions policy.
	ptyMaxRunners = 16
	// ptyIdleTimeout is how long an idle terminal session is kept alive
	// before its shell is reaped. Any Run/Input/Poll refreshes it.
	ptyIdleTimeout = 30 * time.Minute
	// ptyExitedGrace is how long a runner whose shell has exited stays
	// in the map before removal.
	ptyExitedGrace = 2 * time.Minute
	// ptyReapInterval is how often the reaper sweeps.
	ptyReapInterval = time.Minute
)

// shellIdle reports whether the shell itself is waiting at its prompt,
// so that text typed now is a command line. It is not idle while the
// last call left a program running - unless that program has since
// finished on its own, which the shell announces with its prompt marker
// in the output nobody has drained yet - and not while a full-screen
// program has the terminal.
func (r *ptyRunner) shellIdle(s ptyTerminal) bool {
	if s.AltScreen() {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		return true
	}
	if r.promptRe != nil && r.promptRe.Match(s.Pending()) {
		r.running = false
		return true
	}
	return false
}

// touch refreshes the runner's idle clock (called from Type/Poll under
// r.mu).
func (r *ptyRunner) touch() {
	r.lastUsed = time.Now()
}

// ptyReap closes and removes idle or long-exited runners. The caller
// must hold ptyRunnersMu; closing happens off the map lock.
func ptyReap() {
	for _, r := range ptyRunners {
		r.mu.Lock()
		idle := time.Since(r.lastUsed)
		exited := r.session != nil && !r.session.Alive()
		r.mu.Unlock()
		if idle >= ptyIdleTimeout || (exited && idle >= ptyExitedGrace) {
			delete(ptyRunners, r.key)
			go r.Close()
		}
	}
}

// CloseTerminalSessions closes every terminal session this process holds
// and waits for their shells to be gone. It runs at shutdown: a shell
// left behind keeps its working directory open, which on Windows means
// the directory cannot be removed while the shell lives - the way a
// test's temp dir, or a workspace being torn down, finds out a session
// was still there.
func CloseTerminalSessions() {
	closeTerminalSessions(func(*ptyRunner) bool { return true })
}

// closeOwnerSessions closes the sessions one owner holds.
func closeOwnerSessions(owner string) {
	prefix := owner + "\x00"
	closeTerminalSessions(func(r *ptyRunner) bool { return strings.HasPrefix(r.key, prefix) })
}

// CloseTerminalSessionsUnder closes every session whose shell was opened
// in dir or below it. Tests that hand an agent a temporary directory
// call it before that directory is removed.
func CloseTerminalSessionsUnder(dir string) {
	dir = filepath.Clean(dir)
	closeTerminalSessions(func(r *ptyRunner) bool {
		rel, err := filepath.Rel(dir, r.cwd)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	})
}

// closeTerminalSessions removes the runners keep selects from the
// registry and closes them, off the registry lock, waiting for each.
func closeTerminalSessions(keep func(*ptyRunner) bool) {
	ptyRunnersMu.Lock()
	var victims []*ptyRunner
	for key, r := range ptyRunners {
		if keep(r) {
			victims = append(victims, r)
			delete(ptyRunners, key)
		}
	}
	ptyRunnersMu.Unlock()
	var wg sync.WaitGroup
	for _, r := range victims {
		wg.Go(r.Close)
	}
	wg.Wait()
}

// ptyReaperStart launches the background sweeper once per process.
func ptyReaperStart() {
	ptyReaperOnce.Do(func() {
		go func() {
			for range time.Tick(ptyReapInterval) {
				ptyRunnersMu.Lock()
				ptyReap()
				ptyRunnersMu.Unlock()
			}
		}()
	})
}

// runnerKey names a runner in the registry: each owner (agent) gets its
// own session per working directory, the way a person has one terminal
// tab per project they are in.
func runnerKey(owner, cwd string) string {
	return owner + "\x00" + cwd
}

// ptyRunnerFor returns the runner for one owner's workingDir, creating
// (and warm-starting) it on first use. The ask service collects sudo
// passwords from the user; nil disables prompting. Runners are reused
// until they idle out (ptyIdleTimeout) or are evicted at the cap, so
// shell state survives across calls without leaking one PTY per agent
// and working directory forever.
func ptyRunnerFor(owner, cwd string, ask question.Service) *ptyRunner {
	key := runnerKey(owner, cwd)

	ptyRunnersMu.Lock()
	defer ptyRunnersMu.Unlock()
	ptyReap()
	if r, ok := ptyRunners[key]; ok {
		r.mu.Lock()
		r.touch()
		r.mu.Unlock()
		return r
	}
	// Enforce the cap: evict the most-idle runner to make room.
	if len(ptyRunners) >= ptyMaxRunners {
		var victim *ptyRunner
		for _, r := range ptyRunners {
			r.mu.Lock()
			older := victim == nil || r.lastUsed.Before(victim.lastUsed)
			r.mu.Unlock()
			if older {
				victim = r
			}
		}
		if victim != nil {
			delete(ptyRunners, victim.key)
			go victim.Close()
		}
	}
	r := &ptyRunner{key: key, cwd: cwd, ask: ask, lastUsed: time.Now()}
	ptyRunners[key] = r
	ptyReaperStart()
	// Warm start in the background: interactive shells (nix,
	// starship) can take hundreds of milliseconds to become ready.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		r.mu.Lock()
		defer r.mu.Unlock()
		if _, err := r.ensureSessionLocked(ctx); err != nil {
			slog.Warn("Terminal session failed to open", "error", err)
		}
	}()
	return r
}

// terminal returns the runner's session, opening it if needed. The
// state lock is held only for that, never across the wait that follows.
func (r *ptyRunner) terminal(ctx context.Context) (ptyTerminal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touch()
	return r.ensureSessionLocked(ctx)
}

// setState runs f under the state lock.
func (r *ptyRunner) setState(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f()
}

// send writes to the terminal with the write lock held, so two callers
// cannot interleave their bytes.
func (r *ptyRunner) send(s ptyTerminal, b []byte) error {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	if err := s.Send(b); err != nil {
		return fmt.Errorf("terminal session: %w", err)
	}
	return nil
}

// commandInFlight reports whether a command is running here and owns
// the output stream.
func (r *ptyRunner) commandInFlight() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inFlight
}

// ensureSessionLocked opens (or reopens after a shell exit) the
// terminal session. Callers must hold r.mu.
func (r *ptyRunner) ensureSessionLocked(ctx context.Context) (ptyTerminal, error) {
	if r.session != nil && r.session.Alive() {
		return r.session, nil
	}
	// The previous shell died (exit, crash): give restarts a small
	// delay so a runaway loop cannot spin, and remember that it
	// happened - the caller is about to run in a shell that has none of
	// the state the last one had.
	if r.session != nil {
		r.restarted = true
		r.running = false
		r.lastCwd = ""
		// The replacement shell opens in the session directory again, so
		// that is what the model should be told about next -- and only if
		// something moves away from it.
		r.announcedCwd = r.cwd
		if time.Since(r.startedAt) < ptyRestartDelay {
			time.Sleep(ptyRestartDelay)
		}
	}
	s, err := term.Start(r.cwd)
	if err != nil {
		return nil, err
	}
	r.session = s
	r.startedAt = time.Now()
	r.lastScreen = ""
	shellPath, _ := term.Shell()
	dialect := dialectFor(shellPath)
	r.sentinel = newSentinel(dialect)
	// Let the shell settle past its startup output so the first
	// command's output starts clean.
	_ = s.WaitForQuiet(ctx, ptyStartupMs*time.Millisecond, 5*time.Second)
	s.Drain()

	// Sandbox history before anything else: see ptyHistoryOffCmd. The
	// short settle lets the assignment land before the setup line is
	// accepted, so the setup line itself cannot be recorded either.
	if dialect.historyOffCmd != "" {
		if err := s.Send([]byte(dialect.historyOffCmd + term.Enter)); err == nil {
			_ = s.WaitForQuiet(ctx, 300*time.Millisecond, 2*time.Second)
			s.Drain()
		}
	}

	// Strip aliases and install the prompt marker. If the marker never
	// arrives - an exotic shell, a prompt framework that reinstalls its
	// own PS1 - fall back to the bracketed-paste heuristic rather than
	// leaving every command waiting for a marker that will never come.
	r.promptRe = ptyPasteRe
	if err := s.Send([]byte(dialect.setupCmd + term.Enter)); err == nil {
		if s.WaitForAny(ctx, []*regexp.Regexp{ptyPromptRe}, 5*time.Second) == 0 {
			r.promptRe = ptyPromptRe
		} else {
			slog.Warn("Terminal session prompt marker not seen; using fallback prompt detection")
		}
		_ = s.WaitForQuiet(ctx, ptyStartupMs*time.Millisecond, 5*time.Second)
		s.Drain()
	}
	return s, nil
}

// fence clears the session down to a known point before a command is
// sent: it prints a marker of its own, waits for it to come back,
// swallows the prompt that follows it, and drains everything up to
// there. Whatever was in the buffer - the tail of the previous call's
// output, the prompt its sentinel left behind - belongs to a call that
// has already reported, so a command sent after the fence can only see
// bytes it caused itself.
//
// A session that does not answer the fence has something running in
// it. Nothing is drained in that case and the command goes out anyway,
// exactly as it did before the fence existed; clean drops the marker if
// it surfaces later.
func (r *ptyRunner) fence(ctx context.Context, s ptyTerminal) {
	var mark sentinel
	var promptRe *regexp.Regexp
	r.setState(func() { mark, promptRe = r.sentinel, r.promptRe })
	if mark.beginRe == nil || promptRe == nil {
		return
	}
	if err := r.send(s, []byte(mark.begin+term.Enter)); err != nil {
		return
	}
	if s.WaitForAny(ctx, []*regexp.Regexp{mark.beginRe}, ptyFenceWait) != 0 {
		return
	}
	// Taking the prompt that follows the fence is the point of waiting
	// here: left in the buffer, it is exactly what the next wait would
	// read as the command it is about to send having already finished.
	_ = s.WaitForAny(ctx, []*regexp.Regexp{promptRe}, ptyFenceSettle)
	// A line editor's prompt does not end at its marker: the mode tail -
	// the bracketed-paste enable included - trails it by a write or two.
	// Wait for that too (or a short quiet spell, for prompts without
	// one) so the paste tracking the next command's delivery depends on
	// has settled before the drain.
	_, _ = s.WaitForAnyOrQuiet(ctx, []*regexp.Regexp{ptyPasteRe}, ptyFenceQuietMs*time.Millisecond, ptyFenceSettle)
	s.Drain()
}

// Reset kills the session's shell and opens a fresh one in its place.
// It is the way out of a terminal that cannot be talked down - a
// process ignoring ctrl-c, a shell left in a mode nothing answers in -
// and it deliberately does not wait for the command lock: the call
// stuck on that lock is the thing being rescued. Everything the old
// shell held (cd, exported variables, activated environments, the sudo
// credential) is gone with it.
func (r *ptyRunner) Reset(ctx context.Context) error {
	r.mu.Lock()
	old := r.session
	r.session = nil
	r.inFlight = false
	r.running = false
	r.restarted = false
	r.lastEcho = nil
	r.lastScreen = ""
	r.lastCwd = ""
	r.announcedCwd = r.cwd
	r.mu.Unlock()

	if old != nil {
		old.Close()
	}
	if _, err := r.terminal(ctx); err != nil {
		return err
	}
	// The run that was in flight against the old shell unwinds once its
	// session closes and writes its own verdict into the runner on the
	// way out; clear that behind it so the fresh shell does not start
	// out looking busy.
	r.setState(func() {
		r.inFlight = false
		r.running = false
		r.restarted = false
	})
	return nil
}

// Type is the one way into the terminal: it writes what a person would
// type and waits for the terminal to reach a state worth reporting.
// What the text means is decided by the session's state, not by the
// caller. With the shell at its prompt it is a command line - Enter is
// implied, and the completion sentinel recovers its exit code and
// working directory. With a program running it goes to that program -
// an answer, keystrokes, a block of lines for a REPL - and the call
// waits for whatever that leads to: the program finishing (exit code),
// asking again (waiting), redrawing (screen), or going idle. Typing at a
// command another call is still waiting on answers that call's question
// without queuing behind it or taking its output.
func (r *ptyRunner) Type(ctx context.Context, text string, waitSeconds int) (res PTYResult, err error) {
	s, err := r.terminal(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	if r.commandInFlight() {
		return r.typeWhileBusy(ctx, s, text)
	}

	// One waiter at a time on a session; a call typing while this one
	// waits takes the path above, not this lock.
	r.cmdMu.Lock()
	defer r.cmdMu.Unlock()

	segs := parseInput(text)
	defer r.setState(func() {
		r.inFlight = false
		// Remember whether this session still has something in it, so
		// the next text goes to it and the next poll waits for it.
		r.running = (res.Running || res.AltScreen) && s.Alive()
	})
	if r.shellIdle(s) {
		return r.runCommand(ctx, s, text, segs, waitSeconds)
	}
	return r.driveProgram(ctx, s, text, segs, waitSeconds)
}

// runCommand types a command line at the shell's prompt and waits for
// it, driven by events rather than a fixed timer: the shell's prompt
// returning (the command finished), a credential prompt (answered
// masked along the way), a full-screen takeover, output going quiet
// while the foreground job is blocked reading the terminal (the command
// stopped to ask something), or the wait budget running out.
func (r *ptyRunner) runCommand(ctx context.Context, s ptyTerminal, text string, segs []inputSegment, waitSeconds int) (PTYResult, error) {
	// Start this command from a known point: everything the previous
	// call left behind is drained first, so nothing it printed can end
	// up in this call's output and its prompt cannot be mistaken for
	// this command finishing.
	r.fence(ctx, s)

	// Enter is implied: at a prompt, a line nobody presses Enter on
	// does nothing. Text that ends in a newline or a named key (a tab,
	// asking for completion) is typed as it is.
	body := strings.TrimSuffix(text, "\n")
	var echo []string
	if !hasKeys(segs) {
		echo = strings.Split(body, "\n")
	} else if last := segs[len(segs)-1]; last.key == nil && !strings.HasSuffix(last.text, "\n") {
		segs[len(segs)-1].text += "\n"
	}
	r.setState(func() {
		r.lastEcho = echo
		r.inFlight = true
		r.running = false
	})

	var err error
	if echo != nil {
		err = r.pasteCommand(ctx, s, body)
	} else {
		err = r.typeInput(s, segs)
	}
	if err != nil {
		return PTYResult{}, err
	}
	return r.awaitCompletion(ctx, s, echo, waitSeconds)
}

// driveProgram types into the program the session has running and
// waits for what follows, on the same events as a command: the shell's
// prompt marker when the program quit on the keystroke (its exit code
// is recovered), output going quiet when it has redrawn (its screen) or
// stopped to ask (waiting), or the budget running out. Nothing here
// guesses how long a program takes to quit: the shell says when it has.
func (r *ptyRunner) driveProgram(ctx context.Context, s ptyTerminal, text string, segs []inputSegment, waitSeconds int) (PTYResult, error) {
	var echo []string
	if !hasKeys(segs) {
		// Echo stripping is for typed lines. A sequence with keys in it
		// leaves no line to strip, and a guess would eat real output.
		echo = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	}
	r.setState(func() {
		r.lastEcho = echo
		r.inFlight = true
	})
	if err := r.typeInput(s, segs); err != nil {
		return PTYResult{}, err
	}
	// The program may have finished on the keystroke before this wait
	// begins; its prompt can already be sitting in the undrained output.
	s.RescanFromStart()
	return r.awaitCompletion(ctx, s, echo, waitSeconds)
}

// typeWhileBusy types at a command another call is waiting on - the
// answer to its question - and reports the screen as it stands. That
// call owns the output stream, so nothing is drained here.
func (r *ptyRunner) typeWhileBusy(ctx context.Context, s ptyTerminal, text string) (PTYResult, error) {
	segs := parseInput(text)
	if !hasKeys(segs) {
		r.setState(func() { r.lastEcho = strings.Split(strings.TrimSuffix(text, "\n"), "\n") })
	}
	if err := r.typeInput(s, segs); err != nil {
		return PTYResult{}, err
	}
	s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 5*time.Second)
	return r.collectBusy(ctx, s, true), nil
}

// awaitCompletion is the event loop behind Type (and behind a poll that
// picks up a command left running). It returns as soon as the terminal
// reaches a state worth the caller's attention:
//
//   - the shell's prompt marker: the command finished, and
//     collectResult recovers its exit code and working directory;
//   - a credential prompt, seen either as prompt-shaped text (confirmed
//     against the terminal's input discipline) or as a foreground job
//     blocked in a hidden-line read: answered through the question
//     service, with the user's think time added to the budget;
//   - a full-screen program taking the terminal: its rendered screen;
//   - output quiet for ptyQuietMs while the foreground job is blocked
//     reading the terminal: the command is waiting for input (Waiting);
//   - the wait budget running out: output so far, Running set.
//
// A command that keeps producing output is making progress, not stuck:
// each time the budget expires while output was still arriving recently,
// it is leased forward, up to ptyMaxWait in total. That keeps a streaming
// build inside one call instead of bouncing the agent into re-polling
// it, while a command that has gone genuinely idle still returns on
// schedule.
func (r *ptyRunner) awaitCompletion(ctx context.Context, s ptyTerminal, echo []string, waitSeconds int) (PTYResult, error) {
	s.ResetWaitSample()
	pats := []*regexp.Regexp{r.promptRe, credPromptRe, ptyAltScreenRe}
	budget := time.Duration(waitSeconds) * time.Second
	// Output within the lease window counts as progress; the window is
	// capped by the budget so a short-budget call still returns on time
	// behind a fast echo rather than leasing forever.
	lease := min(3*time.Second, budget/2)
	deadline := time.Now().Add(budget)
	hardDeadline := time.Now().Add(ptyMaxWait)
	// asked records that a masked credential prompt was already answered
	// in this wait; the next one means the reader refused the answer, and
	// the user is told so. answeredLen is how much undrained output there
	// was when that answer went out, so anything past it is the reader
	// responding rather than the same prompt still standing.
	asked := false
	answeredLen := 0

	for {
		if ctx.Err() != nil {
			// The caller gave up (the user interrupted the turn). Stop
			// the command rather than leaving it running into the next
			// call's output.
			return r.interrupt(s), nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		matched, quiet := s.WaitForAnyOrQuiet(ctx, pats, ptyQuietMs*time.Millisecond, remaining)
		if quiet {
			// The tail of a finishing command is its prompt; let that
			// land before reading anything into the silence.
			if m := s.WaitForAny(ctx, pats, ptySettleForPromptMs*time.Millisecond); m >= 0 {
				matched = m
			} else if s.AltScreen() {
				// A full-screen program gone quiet is showing its
				// screen, whatever its processes are blocked in; the
				// raw stream is a redraw log and says nothing.
				s.Drain()
				return r.screenResult(s), nil
			} else if act := s.SampleJob(); jobWaitingForInput(act) {
				if r.credentialRead(s) {
					if asked && s.PendingLen() <= answeredLen {
						// A hidden-line read with nothing back from the
						// reader since the last answer: the line is on
						// the wire but the reader has not been
						// scheduled to consume it, so the terminal
						// still looks exactly as it did when it asked.
						// A second dialog here would ask for the same
						// password again and call the first attempt
						// rejected; wait for the reader to say
						// something instead.
						if !s.WaitForOutput(ctx, time.Until(deadline)) {
							break // the silence outlasted the budget
						}
						continue
					}
					// The foreground job is blocked reading the terminal
					// with echo off: a reader asking for a credential,
					// whatever language its prompt was printed in (or
					// whether it printed one at all). Answer it masked
					// rather than reporting it as a question the model
					// could answer in the clear.
					mark, err := r.answerCredentialPrompt(ctx, s, asked)
					if err != nil {
						return PTYResult{}, err
					}
					asked, answeredLen = true, mark
					// User think time is not the command's budget.
					deadline = deadline.Add(2 * time.Minute)
					continue
				}
				if !act.WchanReadable && s.IdleFor() < ptyBlindQuietMs*time.Millisecond {
					// Where the job sleeps cannot be seen - a setuid
					// program, whose wait point the kernel hides from
					// this user. Asleep and silent, it could be a
					// credential reader in PAM's fail delay as easily as
					// a program waiting to be answered; the first will
					// speak again within a few seconds, so hold the
					// verdict until the silence outlasts that.
					if s.WaitForOutput(ctx, min(ptyBlindQuietMs*time.Millisecond-s.IdleFor(), time.Until(deadline))) ||
						time.Now().Before(deadline) {
						continue
					}
					break
				}
				// The foreground job is blocked reading the terminal:
				// the command has asked its question and gone quiet.
				return PTYResult{Output: r.clean(string(s.Drain()), echo), Running: true, Waiting: true}, nil
			} else if !s.WaitForOutput(ctx, time.Until(deadline)) {
				break // the silence outlasted the budget
			} else {
				continue // it spoke again; look for the prompt in that
			}
		}
		if matched < 0 {
			// The budget ran out (or the session exited). A command that
			// was still speaking a moment ago - or still burning CPU, or
			// still growing its memory - is making progress, not stuck:
			// lease it another budget's worth of patience, bounded by the
			// hard ceiling, rather than reporting it mid-stream.
			if s.Alive() && time.Now().Before(hardDeadline) &&
				(s.IdleFor() < lease || jobWorking(s.SampleJob())) {
				deadline = time.Now().Add(budget)
				continue
			}
			break
		}
		switch matched {
		case 1: // credential-shaped prompt text
			if s.AltScreen() {
				// A full-screen program's redraw log is not a prompt
				// to answer blind: the word is a label on its screen.
				continue
			}
			if r.credPromptIsEcho(s, echo) {
				// The terminal echoing the command line back, not a
				// program asking anything: the command's own text says
				// "password:", or prints the rejection a reader would.
				// The scan has consumed the match, so a prompt that
				// arrives after the echo is still seen.
				continue
			}
			// Text is only a hint; the input discipline decides. A real
			// credential reader has turned echo off by the time its
			// prompt is on the wire (sudo, su and ssh all do that
			// first), but give a slower one a moment to flip the bit
			// before dismissing the hint.
			if !r.confirmCredentialPrompt(s, ptyEchoSettleMs*time.Millisecond) {
				// Echo is on, or the shell is back at its prompt:
				// ordinary output that happens to look like a prompt.
				// The scan has consumed the match, so waiting on cannot
				// loop here.
				continue
			}
			mark, err := r.answerCredentialPrompt(ctx, s, asked)
			if err != nil {
				return PTYResult{}, err
			}
			asked, answeredLen = true, mark
			// User think time is not the command's budget.
			deadline = deadline.Add(2 * time.Minute)
			continue
		case 2: // a full-screen program took the terminal
			// Give it a moment to paint, then confirm it is still
			// there: a command that flashes the alternate screen and
			// exits (a pager with a short file) is not an app to drive.
			_ = s.WaitForQuiet(ctx, ptyAltSettleMs*time.Millisecond, time.Second)
			if !s.AltScreen() {
				continue
			}
			s.Drain()
			return r.screenResult(s), nil
		case 0: // prompt returned: command finished
			if s.AltScreen() {
				// A full-screen program that enabled bracketed paste
				// under the fallback heuristic; it is running, not done.
				continue
			}
			return r.collectResult(ctx, s)
		}
	}

	if ctx.Err() != nil {
		return r.interrupt(s), nil
	}
	if s.AltScreen() {
		s.Drain()
		return r.screenResult(s), nil
	}
	return PTYResult{Output: r.clean(string(s.Drain()), echo), Running: s.Alive()}, nil
}

// jobWorking reports measurable progress in the foreground job: CPU
// burned or memory grown since the last sample. Output streaming is the
// other progress signal (IdleFor); this one catches the command that
// works in silence - a compiler phase, a memory-heavy sort - and keeps
// its wait alive instead of cutting it off at an arbitrary deadline.
func jobWorking(a term.JobActivity) bool {
	return a.CPUDelta > ptyActiveCPUTicks || a.RSSDeltaKB > ptyActiveRSSKB
}

// jobWaitingForInput reports the deterministic "the command stopped and
// is waiting to be answered" state: a foreground job exists, every one
// of its processes is asleep, it consumed no CPU and grew no memory
// since the last sample, and it is parked in a terminal read. When the
// kernel does not expose wait points, a fully idle job is taken as
// waiting: sending a harmless keystroke beats burning the whole wait
// budget on a question.
func jobWaitingForInput(a term.JobActivity) bool {
	if !a.Observed || !a.Asleep {
		return false
	}
	if jobWorking(a) {
		return false
	}
	return a.InputWait || !a.WchanReadable
}

// Thresholds for reading a JobActivity sample as progress. A handful of
// CPU ticks is process startup noise, and resident sets wobble by a few
// hundred KB on their own; anything past that is real work.
const (
	ptyActiveCPUTicks = 4
	ptyActiveRSSKB    = 512
)

// echoedLine reports whether a line is the terminal echoing back what
// was sent: the text itself, or a prompt followed by it. The prompt has
// to end in whitespace, so a line of real output that merely ends with
// the same text ("got:hello" after sending "hello") is kept.
func echoedLine(line, echo string) bool {
	if line == echo {
		return true
	}
	prefix, ok := strings.CutSuffix(line, echo)
	return ok && prefix != "" && strings.HasSuffix(prefix, " ")
}

// echoDebris reports whether a line is the terminal echoing back what
// was sent, including the case the simple forms miss: a line editor
// redisplay. zle echoes each character as it arrives and then, once the
// whole line is in, redraws it (syntax highlighting, autosuggestions) by
// rewinding the cursor and reprinting - or, for parts that are already
// on screen, skipping forward. Stripped of escape sequences that leaves
// the command concatenated after itself on one line, whole or in
// fragments: "echo helloecho", "which; echo ...". Such a line matches
// when it starts with a prefix of the command, is short enough to be at
// most two copies of it, and can be consumed entirely as chunks that
// each appear in the command in order.
func echoDebris(line, echo string) bool {
	if echo == "" {
		return false
	}
	if echoedLine(line, echo) {
		return true
	}
	if len(line) > 2*len(echo)+8 {
		return false
	}
	consumed, ok := consumeEchoChunks(line, echo)
	return ok && consumed >= len(echo)
}

// consumeEchoChunks greedily consumes line as consecutive chunks that
// each appear in echo, requiring the first chunk to be a prefix of echo.
// It reports how many bytes were consumed and whether the whole line was.
func consumeEchoChunks(line, echo string) (int, bool) {
	consumed := 0
	first := true
	for consumed < len(line) {
		best := 0
		for end := len(line); end > consumed; end-- {
			chunk := line[consumed:end]
			if !strings.Contains(echo, chunk) {
				continue
			}
			if first && !strings.HasPrefix(echo, chunk) {
				continue
			}
			best = len(chunk)
			break
		}
		if best == 0 {
			return consumed, false
		}
		consumed += best
		first = false
	}
	return consumed, true
}

// promptPartialRe matches the residue of zsh's PROMPT_SP partial-line
// marker: the shell prints a highlighted % (or # for root) followed by a
// space fill to the end of the line, then erases it - a scheme to keep
// the next prompt off a half-finished line. Once escape sequences are
// stripped the erase is gone and the marker survives as
// "partial-output%" plus a run of spaces. The fill is nearly the
// terminal width, so a dozen-odd spaces only ever mean the marker; real
// output padded that far past a % or # is unheard of.
var promptPartialRe = regexp.MustCompile(`^(.*?)[%#] {12,}$`)

// resolveBackspaces turns "e\becho hello" into "echo hello": the line
// editor echoes a character before the rest of the line arrives, then
// backspaces over it to redraw. A backspace erases the preceding rune,
// exactly as the terminal would display it.
func resolveBackspaces(s string) string {
	if !strings.Contains(s, "\b") {
		return s
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\b':
			for len(b) > 0 && b[len(b)-1]&0xC0 == 0x80 {
				b = b[:len(b)-1]
			}
			if len(b) > 0 {
				b = b[:len(b)-1]
			}
		default:
			b = append(b, s[i])
		}
	}
	return string(b)
}

// interrupt stops whatever is running with ctrl-c and reports the
// terminal afterwards. The caller's context is already done at this
// point, so the waits here use a fresh short-lived one.
func (r *ptyRunner) interrupt(s ptyTerminal) PTYResult {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 5*time.Second)
	defer cancel()

	_ = r.send(s, []byte{0x03})
	s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 3*time.Second)
	res := r.collect(ctx, s)
	res.Interrupted = true
	return res
}

// screenResult renders the terminal as a screen: what a person would be
// looking at right now. Used whenever a full-screen program owns the
// terminal, where the raw byte stream is a redraw log, not output.
func (r *ptyRunner) screenResult(s ptyTerminal) PTYResult {
	screen := s.Screen()
	res := PTYResult{
		Output:    screen,
		AltScreen: true,
		Running:   s.Alive(),
	}
	r.setState(func() {
		if screen == r.lastScreen {
			res.Unchanged = true
			res.Output = ""
		}
		r.lastScreen = screen
	})
	return res
}

// collectResult writes the sentinel now that the shell is at a prompt,
// waits for it, and parses exit code and cwd out of the drained
// output.
func (r *ptyRunner) collectResult(ctx context.Context, s ptyTerminal) (PTYResult, error) {
	var mark sentinel
	var echo []string
	r.setState(func() { mark, echo = r.sentinel, r.lastEcho })

	if err := r.send(s, []byte(mark.cmd+term.Enter)); err != nil {
		return PTYResult{}, err
	}
	if s.WaitForAny(ctx, []*regexp.Regexp{mark.loose}, 10*time.Second) != 0 {
		// Sentinel never printed: something is still holding the
		// terminal after all. Report it as running - with no exit code
		// and no claim that the command finished - so the caller keeps
		// interacting instead of treating this as a clean result.
		if s.AltScreen() {
			s.Drain()
			return r.screenResult(s), nil
		}
		return PTYResult{Output: r.clean(string(s.Drain()), echo), Running: s.Alive()}, nil
	}

	drained := string(s.Drain())
	cut := drained
	var match []string
	if loc := mark.parse.FindStringIndex(drained); loc != nil {
		match = mark.parse.FindStringSubmatch(drained)
		cut = drained[:loc[0]]
	}
	res := PTYResult{Output: r.clean(cut, echo)}
	if match != nil {
		if code, err := strconv.Atoi(match[1]); err == nil {
			res.ExitCode = &code
		}
		res.Cwd = match[2]
		r.setState(func() { r.lastCwd = res.Cwd })
	}
	return res, nil
}

// knownCwd returns the session's working directory as of its last
// completed command, or the empty string when none has completed here
// yet (the caller falls back to the directory the session was opened
// in).
func (r *ptyRunner) knownCwd() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastCwd
}

// answerCredentialPrompt drains the prompt output, asks the user for
// the secret through a masked TUI prompt, and submits it to the
// terminal. rejected tells the user the reader turned down the answer
// that came before (sudo's "Sorry, try again." and kin).
//
// It returns the amount of undrained output at the moment the answer
// went out: output past that mark is the reader responding, which is
// what tells a real retry from a terminal that has not read the answer
// yet.
func (r *ptyRunner) answerCredentialPrompt(ctx context.Context, s ptyTerminal, rejected bool) (int, error) {
	// Drain through the prompt so the answer is not blended into
	// stale output; the prompt itself is re-shown by the TUI.
	_ = s.Drain()

	if r.ask == nil {
		// No question service (e.g. headless run): send ctrl-c so the
		// reader fails fast instead of hanging the whole wait budget.
		mark := s.PendingLen()
		_ = s.Send([]byte{0x03})
		return mark, nil
	}

	text := "A program in the terminal session is asking for a password"
	if rejected {
		text += " again: the previous attempt was rejected"
	}
	answers, err := r.ask.Ask(ctx, question.Request{
		Questions: []question.Question{{
			ID:          "terminal_password",
			Type:        question.TypeFreeText,
			Text:        text + " (sudo, su, ssh or similar).",
			Description: "The password is written directly to the terminal session and is never shown to the model.",
			Secret:      true,
		}},
	})
	mark := s.PendingLen()
	if err != nil {
		// Cancel the prompt so the reader gives up and the shell returns
		// to a prompt.
		_ = s.Send([]byte{0x03})
		return mark, nil
	}
	_ = s.Send([]byte(answers[0].FillInText + term.Enter))
	return mark, nil
}

// credPromptIsEcho reports whether the credential-shaped text on the
// wire is nothing but the terminal echoing the command line back. A
// command whose own text says "password:" - or prints the rejection a
// reader would, "Sorry, try again." - has that text echoed before it
// has run, and a dialog opened on it asks the user for a password
// nothing is waiting for; worse, the answer it collects is refused by
// the reader that asks later, and the user is told their password was
// rejected. Echo the shell has finished printing is dropped by clean;
// an echo still arriving can only be the last line, and is matched
// against the command directly.
func (r *ptyRunner) credPromptIsEcho(s ptyTerminal, echo []string) bool {
	lines := strings.Split(r.clean(string(s.Pending()), echo), "\n")
	for i, line := range lines {
		if !credPromptRe.MatchString(line) {
			continue
		}
		if i == len(lines)-1 && echoStarted(line, echo) {
			continue
		}
		return false
	}
	return true
}

// echoStarted reports whether a line is the beginning of the command
// the terminal is echoing back: as much of it as has arrived, on its
// own or after the prompt the shell printed in front of it (which ends
// in whitespace, as in echoedLine).
func echoStarted(line string, echo []string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	for _, sent := range echo {
		sent = strings.TrimSpace(sent)
		if sent == "" {
			continue
		}
		for i := range len(line) {
			if i > 0 && line[i-1] != ' ' {
				continue
			}
			if strings.HasPrefix(sent, line[i:]) {
				return true
			}
		}
	}
	return false
}

// confirmCredentialPrompt decides whether prompt-shaped text on the
// wire is a credential reader waiting, by sampling the terminal's input
// discipline over the window. A hidden-line read settles it at once. A
// terminal whose echo stays on for the whole window is not reading a
// secret, whatever its output looked like; a reader that just printed
// its prompt may not have cleared echo yet, hence the window. A raw
// read with echo off is sudo - or the shell's own line editor, back at
// its prompt after a command that merely printed the word: the prompt
// marker in the undrained output is what tells those apart, and the
// window is what gives that marker time to land.
func (r *ptyRunner) confirmCredentialPrompt(s ptyTerminal, window time.Duration) bool {
	var promptRe *regexp.Regexp
	r.setState(func() { promptRe = r.promptRe })
	shellBack := func() bool { return promptRe != nil && promptRe.Match(s.Pending()) }
	deadline := time.Now().Add(window)
	for {
		switch s.SecretRead() {
		case term.SecretReadYes, term.SecretReadUnknown:
			return true
		case term.SecretReadRaw:
			if shellBack() {
				return false
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return s.SecretRead() == term.SecretReadRaw && !shellBack()
}

// credentialRead reports whether the foreground job, found blocked
// reading the terminal after output went quiet, is reading a secret. A
// hidden line (echo off, canonical) is proof by itself: nothing else
// reads that way. A raw read with echo off is how sudo reads, but also
// how every line editor and full-screen program does, so it counts only
// when the last thing printed is shaped like a credential prompt.
func (r *ptyRunner) credentialRead(s ptyTerminal) bool {
	switch s.SecretRead() {
	case term.SecretReadYes:
		return true
	case term.SecretReadRaw:
		return credPromptRe.MatchString(lastLine(r.clean(string(s.Pending()), nil)))
	default:
		return false
	}
}

// lastLine is the last non-blank line of s, or the empty string.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, " \t\n"), "\n")
	return lines[len(lines)-1]
}

// keystrokes turns the newlines in typed text into the terminal's Enter:
// the same byte on Unix, a carriage return under ConPTY (see term.Enter).
func keystrokes(text string) string {
	return strings.ReplaceAll(text, "\n", term.Enter)
}

// typeInput writes the parsed input to the terminal. Plain text takes
// the paste path, so a multi-line block still arrives as one paste. A
// sequence with keys in it goes out one segment per write, with a gap
// between them, all under the write lock so nothing lands in the middle
// of it. Sent as a single burst, a leading escape is read as the meta
// prefix of whatever follows (ESC : is Alt-:, not "escape then colon"),
// and programs that poll their input can miss the tail of the burst.
func (r *ptyRunner) typeInput(s ptyTerminal, segs []inputSegment) error {
	if len(segs) == 1 && segs[0].key == nil {
		return r.paste(s, segs[0].text)
	}
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	for i, seg := range segs {
		b := seg.key
		if b == nil {
			b = []byte(keystrokes(seg.text))
		}
		if err := s.Send(b); err != nil {
			return fmt.Errorf("terminal session: %w", err)
		}
		if i < len(segs)-1 {
			time.Sleep(ptyKeyGap)
		}
	}
	return nil
}

// paste delivers text the way a terminal would. Several lines written
// straight into a shell or a REPL are read as several typed lines: the
// first one runs on its own, the rest arrive against auto-indent and
// history expansion. When the program has asked for bracketed paste,
// wrap the text in the paste markers so it is taken as one block, and
// submit it with a separate return - a paste that ends in a newline is
// a newline, not "run this".
func (r *ptyRunner) paste(s ptyTerminal, text string) error {
	body, submit := strings.CutSuffix(text, "\n")
	if !strings.Contains(body, "\n") || !s.BracketedPaste() {
		return r.send(s, []byte(keystrokes(text)))
	}
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	if err := s.Paste(keystrokes(body)); err != nil {
		return fmt.Errorf("terminal session: %w", err)
	}
	if submit {
		if err := s.Send([]byte("\r")); err != nil {
			return fmt.Errorf("terminal session: %w", err)
		}
	}
	return nil
}

// pasteCommand delivers a command as a terminal delivers a paste, and
// submits it only after the line editor's echo of the block has settled
// and been drained. A multiline command written straight in is read as
// many typed lines: the line editor echoes each one, redraws it with
// syntax highlighting, and toggles bracketed paste around every prompt -
// debris that survives cleaning as blank lines, bells and doubled
// fragments. Wrapped in the paste markers it is one block with one echo,
// and that echo is dropped whole before the return that runs the
// command, so the command's own output starts from a clean slate.
// Single-line commands and programs without bracketed paste keep the
// plain send path: its one-line echo the cleaner already strips.
func (r *ptyRunner) pasteCommand(ctx context.Context, s ptyTerminal, command string) error {
	body := strings.TrimSuffix(command, "\n")
	if !strings.Contains(body, "\n") || !s.BracketedPaste() {
		return r.send(s, []byte(keystrokes(body)+term.Enter))
	}
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	if err := s.Paste(keystrokes(body)); err != nil {
		return fmt.Errorf("terminal session: %w", err)
	}
	// Everything on the wire between the paste and this drain is the
	// line editor echoing the block back; once it has gone quiet, drop
	// it whole instead of trying to clean it line by line. The
	// echo-line stripping in clean goes with it: with the echo already
	// gone it would only eat command output that repeats a line of the
	// command - a heredoc body catted right back, for one.
	_ = s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 3*time.Second)
	s.Drain()
	r.setState(func() { r.lastEcho = nil })
	if err := s.Send([]byte("\r")); err != nil {
		return fmt.Errorf("terminal session: %w", err)
	}
	return nil
}

// Poll reads the terminal without typing anything. An idle session
// returns what is on it right away. A program an earlier call left
// running is different: its completion has no waiter, so instead of an
// instant snapshot the agent would have to keep re-taking, the poll
// waits for it like a fresh command - its exit code once it finishes,
// its question once it stops to ask, its screen once it has redrawn -
// so one poll is one event rather than a busy-loop of them.
func (r *ptyRunner) Poll(ctx context.Context) (PTYResult, error) {
	s, err := r.terminal(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	if r.beginRunningWait() {
		defer r.endRunningWait()
		// The program may have finished while nobody watched: its
		// prompt can already be sitting in the undrained output.
		s.RescanFromStart()
		var echo []string
		r.setState(func() { echo = r.lastEcho })
		res, err := r.awaitCompletion(ctx, s, echo, DefaultPollWaitSeconds)
		r.setState(func() { r.running = (res.Running || res.AltScreen) && s.Alive() })
		return res, err
	}
	return r.collect(ctx, s), nil
}

// DefaultPollWaitSeconds is the budget a poll gives a program it picks
// up: long enough for a build to land, short enough that a command that
// never finishes still comes back to the caller.
const DefaultPollWaitSeconds = 60

// beginRunningWait claims the wait for a program left running. The
// cheap state check comes first: a poll arriving while a call is in
// flight must not queue behind the command lock - it reports the screen
// right away instead. Only with something running and unwatched is the
// lock taken (re-checked under it, since a Type may have started
// meanwhile), so the wait is the only one on the session.
func (r *ptyRunner) beginRunningWait() bool {
	r.mu.Lock()
	ok := r.running && !r.inFlight
	r.mu.Unlock()
	if !ok {
		return false
	}
	r.cmdMu.Lock()
	r.mu.Lock()
	ok = r.running && !r.inFlight
	r.mu.Unlock()
	if !ok {
		r.cmdMu.Unlock()
		return false
	}
	return true
}

// endRunningWait releases the command lock taken by a successful
// beginRunningWait.
func (r *ptyRunner) endRunningWait() {
	r.cmdMu.Unlock()
}

// tookRestart reports - once - that the session's shell had exited and
// a fresh one was opened, so the caller can pass that on rather than
// leaving the agent to wonder where its working directory went.
func (r *ptyRunner) tookRestart() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	was := r.restarted
	r.restarted = false
	return was
}

// Size reports the session's terminal size.
func (r *ptyRunner) Size() (rows, cols int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil {
		return term.DefaultSize()
	}
	return r.session.Size()
}

// observe reports the terminal as it stands: a rendered screen while a
// full-screen program owns it, the new output otherwise.
func (r *ptyRunner) collect(ctx context.Context, s ptyTerminal) PTYResult {
	return r.collectBusy(ctx, s, r.commandInFlight())
}

// collectBusy is collect with the busy decision already made. A caller
// that sent something to a running command decides at send time, not
// after its settle wait: the command may well finish in between, and
// draining then would take the tail of its output away from the call
// that is about to report it.
func (r *ptyRunner) collectBusy(ctx context.Context, s ptyTerminal, busy bool) PTYResult {
	// The call waiting on that command owns the output stream, so show
	// the screen instead - a read, not a consume.
	if busy {
		res := r.screenResult(s)
		res.AltScreen = s.AltScreen()
		res.WhileBusy = true
		return res
	}

	if s.AltScreen() {
		s.Drain()
		return r.screenResult(s)
	}
	var echo []string
	raw := string(s.Drain())
	r.setState(func() {
		r.lastScreen = ""
		echo = r.lastEcho
		// Draining a session whose command was not in flight observes
		// whatever became of it. A prompt in the drained bytes means the
		// shell - not some program - has the terminal back, so the
		// program left running has finished and been seen: there is
		// nothing left for a poll to wait for. Without a prompt it may
		// still be running, and the flag stands.
		if r.running && r.promptRe.MatchString(raw) {
			r.running = false
		}
	})
	return PTYResult{Output: r.clean(raw, echo), Running: s.Alive()}
}

// clean normalizes terminal output for the model: CRLF and lone CR to
// LF, ANSI escape sequences stripped, backspaces resolved, echoed
// command lines and prompt residue removed, blank edges trimmed.
//
// Prompt markers are turned into line breaks first. A prompt is printed
// the moment a command finishes, so without this, output that did not
// end in a newline (printf %s, a progress line) glues onto the prompt
// and onto the echo of whatever is typed next - and since what is typed
// next is the exit sentinel, the line then matches the sentinel and the
// output is dropped along with it. Both markers have to be split on:
// the OSC 133 marker this session installs as its prompt, and the
// bracketed-paste enable that shells with a line editor send just
// before it. A shell that has neither - dash, or bash built without
// readline - otherwise loses every unterminated line it prints.
func (r *ptyRunner) clean(raw string, echo []string) string {
	var mark sentinel
	r.setState(func() { mark = r.sentinel })

	out := strings.ReplaceAll(raw, "\r\n", "\n")
	out = ptyPromptSplitRe.ReplaceAllString(out, "\n")
	out = strings.ReplaceAll(out, "\x1b[?2004h", "\n")
	out = strings.ReplaceAll(out, "\x1b[?2004l", "\n")
	out = strings.ReplaceAll(out, "\r", "\n")
	out = ansi.Strip(out)
	out = resolveBackspaces(out)

	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, mark.cmd) || mark.parse.MatchString(trimmed) || mark.loose.MatchString(trimmed) {
			continue
		}
		// A fence the session was too busy to answer runs late, once
		// whatever held it up is done; it is bookkeeping either way and
		// never the caller's output.
		if mark.begin != "" && (strings.Contains(trimmed, mark.begin) || mark.beginRe.MatchString(trimmed)) {
			continue
		}
		if m := promptPartialRe.FindStringSubmatch(line); m != nil {
			line = m[1]
		}
		lines = append(lines, line)
	}
	// Drop leading lines that echo what was last sent. The terminal
	// prefixes echoes with its prompt and interleaves paste-marker
	// blank lines, so scan forward and blank each matching line.
	// A line can be echoed twice: the tty driver echoes what was typed,
	// and a shell that regains the terminal mid-keystroke (right after a
	// full-screen program exits) redisplays the same line with its
	// prompt. So a match may stay on the current echo line rather than
	// always advancing, and the scan stops at the first line that is
	// neither - real output.
	if len(echo) > 0 {
		echoIdx := 0
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			matched := false
			for idx := echoIdx; idx < len(echo) && idx <= echoIdx+1; idx++ {
				line := strings.TrimSpace(echo[idx])
				if line == "" {
					continue
				}
				if echoDebris(trimmed, line) {
					lines[i] = ""
					echoIdx = idx
					matched = true
					break
				}
			}
			if !matched {
				break
			}
		}
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

// Close terminates the runner's terminal session.
func (r *ptyRunner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		r.session.Close()
		r.session = nil
	}
}

// cwdIfMoved returns cwd when it differs from the directory the model was
// last told about, and the empty string when it has not moved. The session
// is persistent, so its working directory is conversation state the model
// already holds: repeating it on every call restates what it knows and
// buries the one case that matters, a command that left the shell
// somewhere new.
//
// The baseline is the directory the session opened in -- the same one the
// system prompt names -- so the tag first appears on the call that actually
// moves away from it.
func (r *ptyRunner) cwdIfMoved(cwd string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.announcedCwd == "" {
		r.announcedCwd = r.cwd
	}
	if cwd == "" || cwd == r.announcedCwd {
		return ""
	}
	r.announcedCwd = cwd
	return cwd
}
