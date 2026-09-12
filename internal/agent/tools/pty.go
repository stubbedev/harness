package tools

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/term"
)

// The bash tool's synchronous execution runs inside one persistent
// interactive terminal per Harness process: the user's real shell in a
// pseudo-terminal (internal/term), kept alive for the whole session.
// Working directory, environment, virtualenvs and the sudo timestamp
// survive across calls; commands that would hang a pipe-based runner
// (interactive prompts, REPLs, TUIs) just run in the terminal.
//
// Exit codes and the working directory are recovered from a sentinel
// the shell prints after each single-line command. Sudo password
// prompts are detected in the output stream and answered through a
// masked TUI prompt, so the password goes from the user straight to
// the terminal and never enters the model's context; after the first
// authentication the credential stays valid on the session's tty.

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
	// ptyAltExitWait is how long a full-screen program gets to finish
	// quitting before its screen is reported as still current.
	ptyAltExitWait = 900 * time.Millisecond

	// ptyQuietMs is the silence window after which the runner stops
	// waiting blindly and looks at what the foreground job is doing:
	// blocked on the terminal means the command is waiting for input.
	ptyQuietMs = 1500
	// ptySettleForPromptMs is the grace given to a just-finished
	// command's prompt to land after the quiet window trips, before the
	// silence is read as anything else.
	ptySettleForPromptMs = 300
	// ptyMaxWait bounds how long one call keeps leasing patience
	// forward to a command that is still producing output, so a stream
	// that never ends cannot hold a call forever.
	ptyMaxWait = 15 * time.Minute

	// The sentinel prints the exit code and the working directory after
	// every command. The %d/%s format specifiers keep the echoed command
	// line from matching the parse pattern, and each session mixes a
	// random tag into the marker so a command that happens to print an
	// exit marker of its own (a log line, this file's own tests) cannot
	// be read as the shell's answer.
	ptySentinelFmt      = `printf '__exit_%s:%%d@%%s__' "$?" "$PWD"`
	ptySentinelReFmt    = `__exit_%s:(-?\d+)@(.*)__`
	ptySentinelLooseFmt = `__exit_%s:-?\d+@`

	// ptySetupCmd prepares the session: aliases are stripped so commands
	// run with their standard meanings (functions and environment from
	// the rc files are kept), and the prompt is replaced with an OSC 133
	// "prompt start" marker.
	//
	// The marker is what tells the runner a command has finished. It has
	// to be something only the shell emits: the previous heuristic
	// watched for bracketed-paste-enable (ESC [ ? 2004 h), which every
	// full-screen program and every readline REPL also sends, so
	// starting nvim looked exactly like a finished command and the
	// sentinel was typed into the editor.
	ptySetupCmd = `unalias -a 2>/dev/null; PROMPT_COMMAND=""; RPS1=""; RPROMPT=""; PS2=""; PS1="$(printf '\033]133;A\007')"`
)

// sentinel is one session's completion marker: the command that prints
// it, the pattern that parses it, and the loose pattern used to wait
// for it while its digits and path are still streaming in.
type sentinel struct {
	cmd   string
	parse *regexp.Regexp
	loose *regexp.Regexp
}

func newSentinel() sentinel {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A predictable tag still beats no tag: the point is to be
		// unlike anything a command prints, not to be unguessable.
		binary.BigEndian.PutUint64(b[:], uint64(time.Now().UnixNano()))
	}
	tag := hex.EncodeToString(b[:])
	return sentinel{
		cmd: fmt.Sprintf(ptySentinelFmt, tag),
		// The greedy (.*) takes everything up to the final "__" on the
		// line, so working directories containing underscores parse.
		parse: regexp.MustCompile(fmt.Sprintf(ptySentinelReFmt, tag)),
		loose: regexp.MustCompile(fmt.Sprintf(ptySentinelLooseFmt, tag)),
	}
}

var (
	// sudoPromptRe matches the password prompt sudo prints when it
	// authenticates against the session's tty.
	sudoPromptRe = regexp.MustCompile(`\[sudo\] password for [^:]+:|[Ss]udo password for [^:]+:`)
	// ptyPromptRe matches the OSC 133 prompt marker installed by
	// ptySetupCmd. Seeing it after a command means the shell - not some
	// program the command started - has control back.
	ptyPromptRe = regexp.MustCompile("\x1b\\]133;A")
	// ptyPasteRe is the fallback prompt heuristic for a shell whose
	// prompt could not be replaced: bracketed-paste-enable, which POSIX
	// shells emit before a prompt. It over-matches (TUIs and REPLs send
	// it too), so it is only used together with the alt-screen guard.
	ptyPasteRe = regexp.MustCompile(`\x1b\[\?2004h`)
	// ptyAltExitRe matches a program restoring the main screen: the
	// editor or pager it was is gone and the shell has the terminal back.
	ptyAltExitRe = regexp.MustCompile(`\x1b\[\?(?:1049|1047|47)l`)
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
	Alive() bool
	AltScreen() bool
	Screen() string
	BracketedPaste() bool
	Paste(text string) error
	Resize(rows, cols int) error
	Size() (rows, cols int)
	IdleFor() time.Duration
	SampleJob() term.JobActivity
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
	// but keystrokes, polls and resizes do not, which is what lets a
	// prompt be answered while the command that asked is still going.
	// sendMu keeps two writers from interleaving bytes on the wire.
	mu     sync.Mutex
	cmdMu  sync.Mutex
	sendMu sync.Mutex
	// key is the registry key, cwd the directory the shell started in,
	// and slot tells apart the sessions sharing that directory: slot 0
	// is the primary one, higher slots are opened when it is busy
	// driving an interactive program.
	key     string
	cwd     string
	slot    int
	ask     question.Service
	session ptyTerminal

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
	// lastRunning records that the previous call left something running
	// here, so input and keys know which session to go to.
	lastRunning bool
	// inFlight is set while a command is running here and its output
	// belongs to the call waiting for it.
	inFlight bool
	// orphan is set when a command is still running here and no call is
	// waiting for it - the previous one returned without it finishing.
	// Its completion deserves to be waited for, so a poll picks it up
	// instead of snapshotting, and a new command goes to a sibling
	// session rather than being typed into it.
	orphan bool
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

// ptyMaxSlots bounds how many terminal sessions one working directory
// can hold. A second one appears when the first is busy driving an
// interactive program and a command needs to run anyway - an editor
// open in one terminal and a build running in another, the way a person
// would use two tabs.
const ptyMaxSlots = 4

// errAllSessionsBusy is returned when every session for a directory is
// occupied by a full-screen program.
var errAllSessionsBusy = errors.New(
	"every terminal session for this directory is running a full-screen program: " +
		"quit one (keys \"q\", or \"escape, :, q, !, enter\" in vim/nvim, or \"ctrl+c\") " +
		"before running another command",
)

// occupied reports whether a command sent here would go somewhere other
// than a waiting shell: a full-screen program has the terminal, another
// command is still running and owns the output, or a command this call
// abandoned is still going and would eat the keystrokes.
func (r *ptyRunner) occupied() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil || !r.session.Alive() {
		return false
	}
	return r.inFlight || r.orphan || r.session.AltScreen()
}

// hasAltScreen reports whether a full-screen program owns this session.
func (r *ptyRunner) hasAltScreen() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session != nil && r.session.Alive() && r.session.AltScreen()
}

// leftSomethingRunning reports whether something here is waiting to be
// typed at: a command still in flight, or a REPL or prompt the last
// call left behind. Input and keys belong to that session.
func (r *ptyRunner) leftSomethingRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil || !r.session.Alive() {
		return false
	}
	return r.inFlight || r.lastRunning
}

// freeAfterGrace waits briefly for a full-screen program to finish
// quitting. Programs take a moment to tear down, and a session that is
// about to be free is better than a second shell that starts with none
// of this one's state.
func (r *ptyRunner) freeAfterGrace(ctx context.Context) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil || !r.session.AltScreen() {
		return true
	}
	_ = r.session.WaitForAny(ctx, []*regexp.Regexp{ptyAltExitRe}, ptyAltExitWait)
	return !r.session.AltScreen()
}

// ptyCommandRunner picks a session that can take a command now: the
// primary one, or - while that is busy with an editor, pager or TUI -
// the first sibling that is free, opening one if needed. Sibling
// sessions are separate shells: they do not share the cd, exports or
// virtualenv of the busy one.
func ptyCommandRunner(ctx context.Context, cwd string, ask question.Service) (*ptyRunner, error) {
	for slot := range ptyMaxSlots {
		r := ptyRunnerSlot(cwd, slot, ask)
		if !r.occupied() || r.freeAfterGrace(ctx) {
			return r, nil
		}
	}
	return nil, errAllSessionsBusy
}

// ptyInteractiveRunner picks the session that input, keys and polls are
// meant for: the one with a program in it. Nothing is opened here - a
// keystroke for a program that is not running belongs in the primary
// session, where the agent last was.
func ptyInteractiveRunner(cwd string, ask question.Service) *ptyRunner {
	// A full-screen program is the strongest claim on a keystroke, so
	// look for one of those before settling for a session that merely
	// has something running.
	for _, claims := range []func(*ptyRunner) bool{
		(*ptyRunner).hasAltScreen,
		(*ptyRunner).leftSomethingRunning,
	} {
		for slot := range ptyMaxSlots {
			ptyRunnersMu.Lock()
			r, ok := ptyRunners[slotKey(cwd, slot)]
			ptyRunnersMu.Unlock()
			if ok && claims(r) {
				return r
			}
		}
	}
	return ptyRunnerSlot(cwd, 0, ask)
}

// touch refreshes the runner's idle clock (called from Run/Input/Poll
// under r.mu).
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

// ptyRunnerFor returns the primary runner for workingDir; siblings for
// the same directory live in higher slots (see ptySessionFor).
func ptyRunnerFor(cwd string, ask question.Service) *ptyRunner {
	return ptyRunnerSlot(cwd, 0, ask)
}

// slotKey names a runner in the registry: one working directory can
// have several terminal sessions, so the directory alone is not enough.
func slotKey(cwd string, slot int) string {
	if slot == 0 {
		return cwd
	}
	return fmt.Sprintf("%s\x00#%d", cwd, slot)
}

// ptyRunnerSlot returns the runner in one slot of workingDir, creating
// (and warm-starting) it on first use. The ask service collects sudo
// passwords from the user; nil disables prompting. Runners are reused
// until they idle out (ptyIdleTimeout) or are evicted at the cap, so
// shell state survives across calls without leaking one PTY per
// working directory forever.
func ptyRunnerSlot(cwd string, slot int, ask question.Service) *ptyRunner {
	key := slotKey(cwd, slot)

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
	r := &ptyRunner{key: key, cwd: cwd, slot: slot, ask: ask, lastUsed: time.Now()}
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
		r.orphan = false
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
	r.sentinel = newSentinel()
	// Let the shell settle past its startup output so the first
	// command's output starts clean.
	_ = s.WaitForQuiet(ctx, ptyStartupMs*time.Millisecond, 5*time.Second)
	s.Drain()

	// Strip aliases and install the prompt marker. If the marker never
	// arrives - an exotic shell, a prompt framework that reinstalls its
	// own PS1 - fall back to the bracketed-paste heuristic rather than
	// leaving every command waiting for a marker that will never come.
	r.promptRe = ptyPasteRe
	if err := s.Send([]byte(ptySetupCmd + "\n")); err == nil {
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

// Run sends a command and waits for it to reach a state the caller can
// act on, driven by events rather than a fixed timer: the shell's prompt
// returning (the command finished, and the completion sentinel recovers
// its exit code and working directory), a sudo password prompt, a
// full-screen takeover, output going quiet while the foreground job is
// blocked reading the terminal (the command stopped to ask something),
// or the wait budget running out. Sudo password prompts are answered via
// the masked TUI prompt along the way.
func (r *ptyRunner) Run(ctx context.Context, command string, waitSeconds int) (res PTYResult, err error) {
	// One command at a time in a session; everything else - keystrokes,
	// polls, resizes - stays free while this one waits.
	r.cmdMu.Lock()
	defer r.cmdMu.Unlock()

	s, err := r.terminal(ctx)
	if err != nil {
		return PTYResult{}, err
	}

	// A command sent while an editor or TUI owns the terminal is not a
	// command at all - it is keystrokes for that program, which is how
	// ":wq" ends up in a buffer and the shell never sees the command.
	// Refuse instead, and say what to do about it.
	if s.AltScreen() {
		return PTYResult{}, errAltScreenBusy
	}

	echo := strings.Split(command, "\n")
	r.setState(func() {
		r.lastEcho = echo
		r.inFlight = true
		r.orphan = false
	})
	defer r.setState(func() {
		r.inFlight = false
		// Remember whether this session still has something in it, so a
		// later keystroke goes to the right terminal. A command this call
		// left running is an orphan: nothing is waiting for its
		// completion, so the next poll waits for it instead of
		// snapshotting it over and over.
		r.lastRunning = res.Running || res.AltScreen
		r.orphan = res.Running && !res.AltScreen && s.Alive()
	})

	if err := r.send(s, []byte(command+"\n")); err != nil {
		return PTYResult{}, err
	}

	return r.awaitCompletion(ctx, s, echo, waitSeconds)
}

// awaitCompletion is the event loop behind Run (and behind a poll that
// picks up an orphaned command). It returns as soon as the terminal
// reaches a state worth the caller's attention:
//
//   - the shell's prompt marker: the command finished, and
//     collectResult recovers its exit code and working directory;
//   - a sudo password prompt: answered through the question service,
//     with the user's think time added to the budget;
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
	pats := []*regexp.Regexp{r.promptRe, sudoPromptRe, ptyAltScreenRe}
	budget := time.Duration(waitSeconds) * time.Second
	// Output within the lease window counts as progress; the window is
	// capped by the budget so a short-budget call still returns on time
	// behind a fast echo rather than leasing forever.
	lease := min(3*time.Second, budget/2)
	deadline := time.Now().Add(budget)
	hardDeadline := time.Now().Add(ptyMaxWait)

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
			} else if jobWaitingForInput(s.SampleJob()) {
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
		case 1: // sudo password prompt
			if err := r.answerSudoPrompt(ctx, s); err != nil {
				return PTYResult{}, err
			}
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

// parseTerminalSize parses a "COLSxROWS" size ("240x60"). Columns come
// first, the way terminal sizes are always written.
func parseTerminalSize(s string) (rows, cols int, err error) {
	colsStr, rowsStr, ok := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "x")
	if !ok {
		return 0, 0, fmt.Errorf("resize must be COLSxROWS (e.g. 240x60), got %q", s)
	}
	cols, err = strconv.Atoi(strings.TrimSpace(colsStr))
	if err != nil {
		return 0, 0, fmt.Errorf("resize columns: %w", err)
	}
	rows, err = strconv.Atoi(strings.TrimSpace(rowsStr))
	if err != nil {
		return 0, 0, fmt.Errorf("resize rows: %w", err)
	}
	return rows, cols, nil
}

// errAltScreenBusy is returned when a command is sent while a
// full-screen program still owns the terminal.
var errAltScreenBusy = errors.New(
	"a full-screen program (editor, pager, TUI) owns the terminal session: " +
		"quit it first (keys \"q\", or \"escape, :, q, !, enter\" in vim/nvim, " +
		"or \"ctrl+c\"), or drive it with keys/input instead of a command",
)

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

	if err := r.send(s, []byte(mark.cmd+"\n")); err != nil {
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
	}
	return res, nil
}

// answerSudoPrompt drains the prompt output, asks the user for the
// password through a masked TUI prompt, and submits it to the
// terminal.
func (r *ptyRunner) answerSudoPrompt(ctx context.Context, s ptyTerminal) error {
	// Drain through the prompt so the answer is not blended into
	// stale output; the prompt itself is re-shown by the TUI.
	_ = s.Drain()

	if r.ask == nil {
		// No question service (e.g. headless run): send ctrl-c so sudo
		// fails fast instead of hanging the whole wait budget.
		_ = s.Send([]byte{0x03})
		return nil
	}

	answers, err := r.ask.Ask(ctx, question.Request{
		Questions: []question.Question{{
			ID:          "sudo_password",
			Type:        question.TypeFreeText,
			Text:        "sudo is asking for your password in the terminal session",
			Description: "The password is written directly to the terminal session and is never shown to the model.",
			Secret:      true,
		}},
	})
	if err != nil {
		// Cancel the sudo prompt so the shell returns to a prompt.
		_ = s.Send([]byte{0x03})
		return nil
	}
	_ = s.Send([]byte(answers[0].FillInText + "\n"))
	return nil
}

// Input sends raw text to the terminal without a sentinel: answers to
// prompts, keystrokes for TUIs, or multiline scripts. Returns the
// terminal after output settles.
func (r *ptyRunner) Input(ctx context.Context, text string) (res PTYResult, err error) {
	s, err := r.terminal(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	defer r.setState(func() { r.lastRunning = res.Running || res.AltScreen })

	r.setState(func() { r.lastEcho = strings.Split(strings.TrimSuffix(text, "\n"), "\n") })
	busy := r.commandInFlight()
	if err := r.paste(s, text); err != nil {
		return PTYResult{}, err
	}
	s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 5*time.Second)
	return r.collectBusy(ctx, s, busy), nil
}

// paste delivers text the way a terminal would. Several lines written
// straight into a shell or a REPL are read as several typed lines: the
// first one runs on its own, the rest arrive against auto-indent and
// history expansion, and what comes out is not what was sent. When the
// program has asked for bracketed paste, wrap the text in the paste
// markers so it is taken as one block, and submit it with a separate
// return - a paste that ends in a newline is a newline, not "run this".
func (r *ptyRunner) paste(s ptyTerminal, text string) error {
	body, submit := strings.CutSuffix(text, "\n")
	if !strings.Contains(body, "\n") || !s.BracketedPaste() {
		return r.send(s, []byte(text))
	}
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	if err := s.Paste(body); err != nil {
		return fmt.Errorf("terminal session: %w", err)
	}
	if submit {
		if err := s.Send([]byte("\r")); err != nil {
			return fmt.Errorf("terminal session: %w", err)
		}
	}
	return nil
}

// Keys sends named keys (escape, arrows, ctrl+c, f1-f12) to whatever is
// running, for the keystrokes that are awkward or error-prone to spell
// out as raw text.
func (r *ptyRunner) Keys(ctx context.Context, list string) (res PTYResult, err error) {
	keys, err := parseKeys(list)
	if err != nil {
		return PTYResult{}, err
	}

	s, err := r.terminal(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	defer r.setState(func() { r.lastRunning = res.Running || res.AltScreen })

	busy := r.commandInFlight()
	// One key per write, with a gap between them, and the whole sequence
	// under the write lock so nothing lands in the middle of it. Sent as
	// a single burst, a leading escape is read as the meta prefix of
	// whatever follows (ESC : is Alt-:, not "escape then colon"), and
	// programs that poll their input can miss the tail of the burst.
	if err := func() error {
		r.sendMu.Lock()
		defer r.sendMu.Unlock()
		for i, k := range keys {
			if err := s.Send(k); err != nil {
				return fmt.Errorf("terminal session: %w", err)
			}
			if i < len(keys)-1 {
				time.Sleep(ptyKeyGap)
			}
		}
		return nil
	}(); err != nil {
		return PTYResult{}, err
	}
	r.setState(func() { r.lastEcho = nil })
	s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 5*time.Second)
	return r.collectBusy(ctx, s, busy), nil
}

// Poll reads the current terminal state without sending anything. A
// session idle or showing a full-screen program returns what is on it
// right away. A command an earlier call left running is different: its
// completion has no waiter, so instead of an instant snapshot the agent
// would have to keep re-taking, the poll waits for it like a fresh
// command - its exit code once it finishes, its question once it stops
// to ask - so one poll is one event rather than a busy-loop of them.
func (r *ptyRunner) Poll(ctx context.Context) (PTYResult, error) {
	s, err := r.terminal(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	if r.beginOrphanWait() {
		defer r.endOrphanWait()
		// The orphan may have finished while nobody watched: its prompt
		// can already be sitting in the undrained output.
		s.RescanFromStart()
		var echo []string
		r.setState(func() { echo = r.lastEcho })
		res, err := r.awaitCompletion(ctx, s, echo, DefaultPollWaitSeconds)
		r.setState(func() {
			if res.Running {
				// Still unobserved: a later poll can pick it up again.
				r.orphan = true
			}
		})
		return res, err
	}
	return r.collect(ctx, s), nil
}

// DefaultPollWaitSeconds is the budget a poll gives an orphaned command
// it picks up: long enough for a build to land, short enough that a
// command that never finishes still comes back to the caller.
const DefaultPollWaitSeconds = 60

// beginOrphanWait claims the wait for an orphaned command. The cheap
// state check comes first: a poll arriving while a command is in flight
// must not queue behind the command lock - it reports the screen right
// away instead. Only with an orphan on the books is the command lock
// taken (re-checked under it, since a Run may have started meanwhile),
// so the wait is the only one on the session.
func (r *ptyRunner) beginOrphanWait() bool {
	r.mu.Lock()
	ok := r.orphan && !r.inFlight
	r.mu.Unlock()
	if !ok {
		return false
	}
	r.cmdMu.Lock()
	r.mu.Lock()
	ok = r.orphan && !r.inFlight
	r.mu.Unlock()
	if !ok {
		r.cmdMu.Unlock()
		return false
	}
	return true
}

// endOrphanWait releases the command lock taken by a successful
// beginOrphanWait.
func (r *ptyRunner) endOrphanWait() {
	r.cmdMu.Unlock()
}

// Resize changes the terminal size for the whole session. A program
// already running redraws at the new size (SIGWINCH).
func (r *ptyRunner) Resize(ctx context.Context, rows, cols int) (PTYResult, error) {
	s, err := r.terminal(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	if err := s.Resize(rows, cols); err != nil {
		return PTYResult{}, err
	}
	s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 5*time.Second)
	// The redraw invalidates the dedup baseline: the same UI at a new
	// size is new information.
	r.setState(func() { r.lastScreen = "" })
	return r.collect(ctx, s), nil
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

	// Quitting a full-screen program takes longer than the settle
	// window: it goes quiet while it tears down, then restores the main
	// screen. Give it that moment, or the call after ":q" still reports
	// an editor that is already gone.
	if s.AltScreen() {
		_ = s.WaitForAny(ctx, []*regexp.Regexp{ptyAltExitRe}, ptyAltExitWait)
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
		// shell - not some program - has the terminal back, so an
		// orphaned command has finished and been seen: there is nothing
		// left for a poll to wait for. Without a prompt it may still be
		// running, and the orphan flag stands.
		if r.orphan && r.promptRe.MatchString(raw) {
			r.orphan = false
		}
	})
	return PTYResult{Output: r.clean(raw, echo), Running: s.Alive()}
}

// clean normalizes terminal output for the model: CRLF and lone CR to
// LF, ANSI escape sequences stripped, backspaces resolved, echoed
// command lines and prompt residue removed, blank edges trimmed.
// Bracketed-paste markers are turned into line breaks first: a prompt is
// printed right after the closing marker, so without this, unterminated
// output (printf %s, ...) glues onto the prompt and the next echoed
// command.
func (r *ptyRunner) clean(raw string, echo []string) string {
	var mark sentinel
	r.setState(func() { mark = r.sentinel })

	out := strings.ReplaceAll(raw, "\r\n", "\n")
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
