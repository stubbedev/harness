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
}

// ptyTerminal is the slice of term.Session the runner uses; it exists
// so tests can substitute a scripted terminal.
type ptyTerminal interface {
	Send(b []byte) error
	WaitForAny(ctx context.Context, patterns []*regexp.Regexp, timeout time.Duration) int
	WaitForQuiet(ctx context.Context, quiet, timeout time.Duration) bool
	Drain() []byte
	Alive() bool
	AltScreen() bool
	Screen() string
	Resize(rows, cols int) error
	Size() (rows, cols int)
	Close()
}

// ptyRunner owns the single terminal session for one working directory
// and serializes access: commands run one at a time, like a person
// typing in a terminal.
type ptyRunner struct {
	mu sync.Mutex
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

// occupied reports whether a full-screen program owns this session, so
// a command sent to it would be keystrokes for that program instead.
// A session mid-command counts as occupied too: its output belongs to
// the call that is waiting for it.
func (r *ptyRunner) occupied() bool {
	if !r.mu.TryLock() {
		return true
	}
	defer r.mu.Unlock()
	return r.session != nil && r.session.Alive() && r.session.AltScreen()
}

// hasAltScreen reports whether a full-screen program owns this session.
// Locked out means a call is in flight; that session is somebody's, so
// treat it as taken rather than guessing.
func (r *ptyRunner) hasAltScreen() bool {
	if !r.mu.TryLock() {
		return true
	}
	defer r.mu.Unlock()
	return r.session != nil && r.session.Alive() && r.session.AltScreen()
}

// leftSomethingRunning reports whether the last call here ended with a
// program still going - a REPL, a command past its wait budget, an
// answer typed at a prompt. Input and keys belong to that session.
func (r *ptyRunner) leftSomethingRunning() bool {
	if !r.mu.TryLock() {
		return true
	}
	defer r.mu.Unlock()
	return r.session != nil && r.session.Alive() && r.lastRunning
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

// Run sends a command and waits for its completion sentinel, handling
// sudo password prompts along the way. When the wait budget expires it
// reports the terminal output so far with Running set.
// Run sends a command, waits for the shell to hand control back (the
// bracketed-paste prompt marker), then asks the shell for the
// completion sentinel. The sentinel is only written once the prompt
// returns, so commands that read stdin (read, REPLs, hidden password
// prompts) never swallow it. Sudo password prompts are answered via
// the masked TUI prompt along the way. When the wait budget expires
// the terminal output so far is reported with Running set.
func (r *ptyRunner) Run(ctx context.Context, command string, waitSeconds int) (res PTYResult, err error) {
	r.mu.Lock()
	r.touch()
	defer r.mu.Unlock()
	// Remember whether this session still has something in it, so a
	// later keystroke goes to the right terminal.
	defer func() { r.lastRunning = res.Running || res.AltScreen }()

	s, err := r.ensureSessionLocked(ctx)
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

	if err := s.Send([]byte(command + "\n")); err != nil {
		return PTYResult{}, fmt.Errorf("terminal session: %w", err)
	}
	r.lastEcho = strings.Split(command, "\n")

	deadline := time.Now().Add(time.Duration(waitSeconds) * time.Second)

	for {
		if ctx.Err() != nil {
			// The caller gave up (the user interrupted the turn). Stop
			// the command rather than leaving it running into the next
			// call's output.
			return r.interruptLocked(s), nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		matched := s.WaitForAny(ctx, []*regexp.Regexp{r.promptRe, sudoPromptRe, ptyAltScreenRe}, remaining)
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
		break // timeout or session exit
	}

	if s.AltScreen() {
		s.Drain()
		return r.screenResult(s), nil
	}
	return PTYResult{Output: r.clean(string(s.Drain())), Running: s.Alive()}, nil
}

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

// interruptLocked stops whatever is running with ctrl-c and reports the
// terminal afterwards. Callers must hold r.mu. The context is already
// done at this point, so waits here use a fresh short-lived one.
func (r *ptyRunner) interruptLocked(s ptyTerminal) PTYResult {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 5*time.Second)
	defer cancel()

	_ = s.Send([]byte{0x03})
	s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 3*time.Second)
	res := r.observe(ctx, s)
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
	if screen == r.lastScreen {
		res.Unchanged = true
		res.Output = ""
	}
	r.lastScreen = screen
	return res
}

// collectResult writes the sentinel now that the shell is at a prompt,
// waits for it, and parses exit code and cwd out of the drained
// output.
func (r *ptyRunner) collectResult(ctx context.Context, s ptyTerminal) (PTYResult, error) {
	if err := s.Send([]byte(r.sentinel.cmd + "\n")); err != nil {
		return PTYResult{}, fmt.Errorf("terminal session: %w", err)
	}
	if s.WaitForAny(ctx, []*regexp.Regexp{r.sentinel.loose}, 10*time.Second) != 0 {
		// Sentinel never printed: something is still holding the
		// terminal after all. Report it as running - with no exit code
		// and no claim that the command finished - so the caller keeps
		// interacting instead of treating this as a clean result.
		if s.AltScreen() {
			s.Drain()
			return r.screenResult(s), nil
		}
		return PTYResult{Output: r.clean(string(s.Drain())), Running: s.Alive()}, nil
	}

	drained := string(s.Drain())
	cut := drained
	var match []string
	if loc := r.sentinel.parse.FindStringIndex(drained); loc != nil {
		match = r.sentinel.parse.FindStringSubmatch(drained)
		cut = drained[:loc[0]]
	}
	res := PTYResult{Output: r.clean(cut)}
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
	r.mu.Lock()
	r.touch()
	defer r.mu.Unlock()
	defer func() { r.lastRunning = res.Running || res.AltScreen }()

	s, err := r.ensureSessionLocked(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	if err := s.Send([]byte(text)); err != nil {
		return PTYResult{}, fmt.Errorf("terminal session: %w", err)
	}
	r.lastEcho = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 5*time.Second)
	return r.observe(ctx, s), nil
}

// Keys sends named keys (escape, arrows, ctrl+c, f1-f12) to whatever is
// running, for the keystrokes that are awkward or error-prone to spell
// out as raw text.
func (r *ptyRunner) Keys(ctx context.Context, list string) (res PTYResult, err error) {
	keys, err := parseKeys(list)
	if err != nil {
		return PTYResult{}, err
	}

	r.mu.Lock()
	r.touch()
	defer r.mu.Unlock()
	defer func() { r.lastRunning = res.Running || res.AltScreen }()

	s, err := r.ensureSessionLocked(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	// One key per write, with a gap between them. Sent as a single burst,
	// a leading escape is read as the meta prefix of whatever follows
	// (ESC : is Alt-:, not "escape then colon"), and programs that poll
	// their input can miss the tail of the burst.
	for i, k := range keys {
		if err := s.Send(k); err != nil {
			return PTYResult{}, fmt.Errorf("terminal session: %w", err)
		}
		if i < len(keys)-1 {
			time.Sleep(ptyKeyGap)
		}
	}
	r.lastEcho = nil
	s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 5*time.Second)
	return r.observe(ctx, s), nil
}

// Poll reads the current terminal state without sending anything.
func (r *ptyRunner) Poll(ctx context.Context) (PTYResult, error) {
	r.mu.Lock()
	r.touch()
	defer r.mu.Unlock()

	s, err := r.ensureSessionLocked(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	return r.observe(ctx, s), nil
}

// Resize changes the terminal size for the whole session. A program
// already running redraws at the new size (SIGWINCH).
func (r *ptyRunner) Resize(ctx context.Context, rows, cols int) (PTYResult, error) {
	r.mu.Lock()
	r.touch()
	defer r.mu.Unlock()

	s, err := r.ensureSessionLocked(ctx)
	if err != nil {
		return PTYResult{}, err
	}
	if err := s.Resize(rows, cols); err != nil {
		return PTYResult{}, err
	}
	s.WaitForQuiet(ctx, ptySettleMs*time.Millisecond, 5*time.Second)
	// The redraw invalidates the dedup baseline: the same UI at a new
	// size is new information.
	r.lastScreen = ""
	return r.observe(ctx, s), nil
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
func (r *ptyRunner) observe(ctx context.Context, s ptyTerminal) PTYResult {
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
	r.lastScreen = ""
	return PTYResult{Output: r.clean(string(s.Drain())), Running: s.Alive()}
}

// clean normalizes terminal output for the model: CRLF and lone CR to
// LF, ANSI escape sequences stripped, echoed command lines removed,
// blank edges trimmed. Bracketed-paste markers are turned into line
// breaks first: a prompt is printed right after the closing marker, so
// without this, unterminated output (printf %s, ...) glues onto the
// prompt and the next echoed command.
func (r *ptyRunner) clean(raw string) string {
	out := strings.ReplaceAll(raw, "\r\n", "\n")
	out = strings.ReplaceAll(out, "\r", "\n")
	out = strings.ReplaceAll(out, "\x1b[?2004h", "\n")
	out = strings.ReplaceAll(out, "\x1b[?2004l", "\n")
	out = ansi.Strip(out)

	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, r.sentinel.cmd) || r.sentinel.parse.MatchString(trimmed) || r.sentinel.loose.MatchString(trimmed) {
			continue
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
	if len(r.lastEcho) > 0 {
		echoIdx := 0
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			matched := false
			for idx := echoIdx; idx < len(r.lastEcho) && idx <= echoIdx+1; idx++ {
				echo := strings.TrimSpace(r.lastEcho[idx])
				if echo == "" {
					continue
				}
				if echoedLine(trimmed, echo) {
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
