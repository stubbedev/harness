// Package term provides a persistent pseudo-terminal session: a real
// interactive shell spawned in a PTY, kept alive for the lifetime of the
// process. It is the pure-Go foundation for running commands that might
// otherwise hang a harness - interactive prompts, REPLs, TUIs - because
// the harness can send raw input and read raw output at any time instead
// of blocking on pipes that a program is waiting to write into.
//
// The session is intentionally low-level: it deals in bytes, quiet
// windows and regex waits. Command/exit-code semantics live one layer up
// (the shell tool), which uses a printed sentinel to recover exit codes
// from the shell.
package term

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/hinshun/vt10x"
	"github.com/stubbedev/harness/internal/procgroup"
)

const (
	// DefaultRows and DefaultCols are the terminal size the session is
	// opened with. Generous width keeps build output from wrapping into
	// unreadable columns, and the extra rows give full-screen programs
	// (editors, pagers, TUIs) enough room that their panes are legible
	// in a rendered screen. Override per machine with HARNESS_PTY_ROWS
	// and HARNESS_PTY_COLS.
	DefaultRows = 80
	DefaultCols = 280

	// minRows/minCols/maxRows/maxCols bound the size an override can
	// ask for; a degenerate grid breaks the emulator and an enormous one
	// turns every screen read into a wall of blanks.
	minRows, minCols = 4, 20
	maxRows, maxCols = 400, 1000

	// maxPending caps the undrained output buffer so a runaway process
	// cannot grow memory without bound. This buffer is the session's
	// scrollback for ordinary output: everything a command printed is
	// here, not just the lines the current size can show.
	maxPending = 16 << 20 // 16 MiB
)

// Session is a persistent PTY running an interactive shell.
type Session struct {
	mu     sync.Mutex
	notify chan struct{} // signalled (cap 1) whenever output arrives
	closed chan struct{}

	pty      pty.Pty
	proc     *os.Process
	pending  []byte    // output not yet drained
	scanFrom int       // where WaitFor pattern scanning continues from
	lastData time.Time // last time output arrived
	exited   bool
	err      error
	// exitCode/exitKnown are the shell process's own exit status,
	// recorded by reap once the process has been waited on: the code it
	// exited with, or -1 when a signal killed it. The read loop usually
	// declares the session over first, so this lands a moment after
	// exited does.
	exitCode  int
	exitKnown bool
	closeOnce sync.Once

	// job is the Windows job object the shell was assigned to at
	// start, so closing it in Close kills the whole tree. Zero (no
	// job) on non-Windows.
	job uintptr

	// emu is a headless terminal emulator fed the same bytes as pending.
	// The raw stream is the right view of a normal command (it keeps
	// everything, including output that scrolled past the screen), but
	// it is unreadable for a full-screen program: cursor addressing and
	// redraws only mean something once they have been applied to a grid.
	// Screen renders that grid; AltScreen says which view to use.
	emu        vt10x.Terminal
	replies    *replyWriter
	rows, cols int

	// waitSampleCPU/waitSampleRSS/waitSampleValid are the baseline the
	// SampleJob deltas are measured against: the foreground job's
	// accumulated CPU and resident set at the last sample, and whether
	// that sample exists yet.
	waitSampleCPU   uint64
	waitSampleRSS   int64
	waitSampleValid bool

	// bracketedPaste is whatever is running asking for pasted text to be
	// marked as a paste; modeCarry holds the tail of the last chunk so a
	// mode sequence split across two reads is still seen.
	bracketedPaste bool
	modeCarry      []byte
}

// Shell returns the shell the terminal session should run and whether
// one was identified at all: the shell Harness itself was launched from
// when the parent process is one, otherwise $SHELL or ComSpec when that
// names a shell this package knows how to drive.
//
// Nothing is guessed. A guess here is a terminal that silently fails to
// open or, worse, one driven with a protocol it does not speak, and the
// caller has a better answer for that than a wrong shell: not offering
// the tool. Every shell it can name is driven directly, PowerShell and
// cmd included -- the session protocol has a dialect per Kind, so the
// model is told which shell it has and writes for that one, which is
// the point of handing it a real shell rather than an interpreter.
func Shell() (string, bool) {
	if sh := parentShell(); sh != "" {
		return sh, true
	}
	for _, name := range []string{"SHELL", "ComSpec"} {
		if sh := os.Getenv(name); sh != "" && KindOf(sh) != KindUnknown {
			return sh, true
		}
	}
	return "", false
}

// Kind is the dialect a shell speaks, which decides how the session
// asks it for an exit code and a working directory.
type Kind int

const (
	// KindUnknown is a shell this package cannot drive.
	KindUnknown Kind = iota
	// KindPosix is bash, zsh and the other Bourne-family shells.
	KindPosix
	// KindPowerShell is Windows PowerShell or PowerShell Core.
	KindPowerShell
	// KindCmd is the Windows command interpreter.
	KindCmd
)

// shellKinds maps a shell's executable name to the dialect it speaks.
var shellKinds = map[string]Kind{
	"bash":       KindPosix,
	"zsh":        KindPosix,
	"sh":         KindPosix,
	"dash":       KindPosix,
	"ksh":        KindPosix,
	"ash":        KindPosix,
	"fish":       KindPosix,
	"powershell": KindPowerShell,
	"pwsh":       KindPowerShell,
	"cmd":        KindCmd,
}

// KindOf reports the dialect of the shell at path, by executable name.
// Backslashes are cut as separators whatever the host is: a Windows
// shell path can be read on any platform (a recorded session, a test),
// and filepath.Base only knows the running platform's separator.
func KindOf(path string) Kind {
	base := strings.ToLower(filepath.Base(path))
	if i := strings.LastIndexByte(base, '\\'); i >= 0 {
		base = base[i+1:]
	}
	return shellKinds[strings.TrimSuffix(base, ".exe")]
}

// parentShell reports the shell hosting the Harness process by looking
// at the parent process name; empty when the parent is not a shell
// (terminal emulator, systemd, an editor task runner, ...) or when the
// platform offers no way to ask.
func parentShell() string {
	name := parentProcessName()
	if name == "" {
		return ""
	}
	if KindOf(name) == KindUnknown {
		return ""
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return ""
}

// Start spawns the user's shell in a new pseudo-terminal with the given
// working directory. The environment is the current process environment
// plus TERM; env entries of the form KEY=VALUE are appended last so they
// win. LINES and COLUMNS are dropped: a size exported there would be a
// lie told to any program that consults the environment instead of the
// terminal, so the window size stays authoritative.
// The shell is deliberately not bound to a caller context: the session
// outlives the request that opened it and is torn down by Close.
//
// The pseudo-terminal is a Unix PTY or, on Windows, a ConPTY; the
// session is the same either way - bytes in, bytes out, one shell
// process to wait on.
//
// It fails rather than guessing when no shell can be identified; callers
// that can avoid offering a terminal at all should check Shell first.
func Start(cwd string, env ...string) (*Session, error) {
	shell, ok := Shell()
	if !ok {
		return nil, errors.New("no shell could be identified for a terminal session")
	}
	rows, cols := DefaultSize()

	p, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("failed to open a pseudo-terminal: %w", err)
	}
	// Sized before the shell starts: a program reads the terminal size
	// once, on startup, and the shell's first prompt is laid out for it.
	_ = p.Resize(cols, rows)

	cmd := p.Command(shell)
	cmd.Dir = cwd
	cmd.Env = append(withoutSizeEnv(os.Environ()), withoutSizeEnv(env)...)
	cmd.Env = append(cmd.Env, "TERM="+termValue())
	if err := cmd.Start(); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("failed to start terminal session: %w", err)
	}
	afterStart(p)

	s := newSession(p, cmd.Process, rows, cols)
	// Assign the shell to a job object before it can spawn anything:
	// every descendant then dies with the session on Windows. A no-op
	// returning 0 elsewhere.
	s.job = procgroup.NewJob(cmd.Process)
	go s.reap(cmd)
	return s, nil
}

// reap waits for the shell process so it is not left a zombie, and
// records its exit. Where the read loop can see the exit on its own -
// on Unix the master reads EIO once the last slave descriptor closes -
// it stays the authority, so the shell's final output is drained before
// the session is declared over; the grace here covers the shell whose
// tty is still held open by a background child it left behind.
func (s *Session) reap(cmd *pty.Cmd) {
	err := cmd.Wait()
	s.mu.Lock()
	if ps := cmd.ProcessState; ps != nil {
		s.exitCode = ps.ExitCode()
		s.exitKnown = true
	}
	s.mu.Unlock()
	onExit(s.pty)
	select {
	case <-s.closed:
	case <-time.After(time.Second):
		s.finish(err)
	}
}

// withoutSizeEnv drops LINES and COLUMNS entries so nothing in the
// session inherits a terminal size that a resize has already falsified.
func withoutSizeEnv(entries []string) []string {
	filtered := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, _, _ := strings.Cut(entry, "=")
		if name == "LINES" || name == "COLUMNS" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// termValue is the TERM the session advertises. It is not the user's
// TERM: programs tailor their escape sequences to it, and the emulator
// behind this session is xterm-class, so inheriting a kitty, foot or
// wezterm TERM invites sequences it cannot parse and a screen that
// renders wrong. Override with HARNESS_PTY_TERM when a program needs
// something else.
func termValue() string {
	if t := strings.TrimSpace(os.Getenv("HARNESS_PTY_TERM")); t != "" {
		return t
	}
	return "xterm-256color"
}

// DefaultSize is the size new sessions open with: the defaults, unless
// HARNESS_PTY_ROWS/HARNESS_PTY_COLS override them.
func DefaultSize() (rows, cols int) {
	return clampDim(envInt("HARNESS_PTY_ROWS", DefaultRows), minRows, maxRows),
		clampDim(envInt("HARNESS_PTY_COLS", DefaultCols), minCols, maxCols)
}

func envInt(name string, fallback int) int {
	// Parsed at 16 bits: these are terminal dimensions, they end up in a
	// 16-bit ioctl struct, and a value that cannot fit one was never a
	// size. clampDim still narrows what is left to the sane range.
	v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 16)
	if err != nil {
		return fallback
	}
	return int(v)
}

func clampDim(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

func newSession(p pty.Pty, proc *os.Process, rows, cols int) *Session {
	replies := newReplyWriter()
	emu := vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithWriter(replies))
	s := &Session{
		pty:      p,
		proc:     proc,
		notify:   make(chan struct{}, 1),
		closed:   make(chan struct{}),
		lastData: time.Now(),
		emu:      emu,
		replies:  replies,
		rows:     rows,
		cols:     cols,
	}
	replies.session = s
	go s.readLoop()
	return s
}

// replyWriter carries the emulator's answers back to the program that
// asked. Programs query the terminal (device attributes, cursor
// position) and then wait for an answer on their input; this emulator
// is the only terminal the session has, so nothing else is going to
// answer, and a program left waiting stalls until its own timeout.
//
// Answers go to a goroutine rather than straight down the PTY: a write
// blocks when the program is not reading its input, and blocking here
// would stall the parser and, behind it, the reading of output. A
// dropped answer is a program falling back to its default; a stalled
// session is the whole tool wedged.
type replyWriter struct {
	session *Session
	replies chan []byte
}

func newReplyWriter() *replyWriter {
	w := &replyWriter{replies: make(chan []byte, 64)}
	go func() {
		for b := range w.replies {
			if w.session != nil {
				_ = w.session.Send(b)
			}
		}
	}()
	return w
}

func (w *replyWriter) Write(p []byte) (int, error) {
	reply := make([]byte, len(p))
	copy(reply, p)
	select {
	case w.replies <- reply:
	default:
	}
	return len(p), nil
}

func (w *replyWriter) close() {
	close(w.replies)
}

// readLoop pumps PTY output into the pending buffer until EOF or error.
func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.append(buf[:n])
		}
		if err != nil {
			s.finish(err)
			return
		}
	}
}

func (s *Session) append(b []byte) {
	// The emulator keeps its own lock; feed it outside s.mu so a slow
	// parse cannot stall a reader waiting on pending.
	_, _ = s.emu.Write(b)

	s.mu.Lock()
	s.trackBracketedPasteLocked(b)
	s.pending = append(s.pending, b...)
	if len(s.pending) > maxPending {
		// Keep the tail; the head is the oldest, least useful output.
		drop := len(s.pending) - maxPending
		s.pending = s.pending[drop:]
		s.scanFrom = max(0, s.scanFrom-drop)
	}
	s.lastData = time.Now()
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Bracketed paste: a program that turns it on wants pasted text
// wrapped in the paste markers, so it can tell "the user pasted this"
// from "the user typed this". Shells, editors and REPLs use that to
// skip auto-indent, history expansion and immediate execution - which
// is exactly what mangles multi-line text written straight into them.
// vt10x does not track the mode, so watch the stream for it.
var (
	bracketedPasteOn  = []byte("\x1b[?2004h")
	bracketedPasteOff = []byte("\x1b[?2004l")
)

// trackBracketedPasteLocked follows the mode across chunk boundaries by
// keeping the tail of the previous chunk: the sequence is 8 bytes and a
// read can split it anywhere. Callers must hold s.mu.
func (s *Session) trackBracketedPasteLocked(b []byte) {
	scan := b
	if len(s.modeCarry) > 0 {
		scan = append(append([]byte{}, s.modeCarry...), b...)
	}
	if on, off := bytes.LastIndex(scan, bracketedPasteOn), bytes.LastIndex(scan, bracketedPasteOff); on >= 0 || off >= 0 {
		s.bracketedPaste = on > off
	}
	carry := min(len(scan), len(bracketedPasteOn)-1)
	s.modeCarry = append(s.modeCarry[:0], scan[len(scan)-carry:]...)
}

// BracketedPaste reports whether whatever is running has asked for
// pasted text to be marked as a paste.
func (s *Session) BracketedPaste() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bracketedPaste
}

// Paste writes text the way a terminal delivers a paste: wrapped in the
// paste markers when the program asked for them, plain otherwise.
func (s *Session) Paste(text string) error {
	if !s.BracketedPaste() {
		return s.Send([]byte(text))
	}
	return s.Send(append(append(append([]byte{}, "\x1b[200~"...), text...), "\x1b[201~"...))
}

func (s *Session) finish(err error) {
	s.mu.Lock()
	if !s.exited {
		s.exited = true
		s.err = err
		close(s.closed)
	}
	s.mu.Unlock()
}

// Send writes raw bytes to the terminal (keystrokes, command lines).
// Sending counts as activity so a quiet-wait right after a write does
// not fire spuriously.
func (s *Session) Send(b []byte) error {
	s.mu.Lock()
	exited := s.exited
	if !exited {
		s.lastData = time.Now()
	}
	s.mu.Unlock()
	if exited {
		return errors.New("terminal session has exited")
	}
	_, err := s.pty.Write(b)
	return err
}

// WaitForAny blocks until one of the patterns appears in output that
// no earlier match has consumed, the session exits, the timeout
// elapses, or ctx is done. It returns the index of the pattern that
// matched earliest in the unconsumed output, or -1 if no pattern
// matched. Scanning starts where the last match ended (or where the
// last Drain/RescanFromStart left it), so bytes that arrive while no
// one is waiting are seen by the next call rather than skipped.
func (s *Session) WaitForAny(ctx context.Context, patterns []*regexp.Regexp, timeout time.Duration) int {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		s.mu.Lock()
		matched := s.scanLocked(patterns)
		exited := s.exited
		s.mu.Unlock()

		if matched >= 0 {
			return matched
		}

		if exited {
			return -1
		}

		select {
		case <-ctx.Done():
			return -1
		case <-deadline.C:
			return -1
		case <-s.closed:
			return -1
		case <-s.notify:
		}
	}
}

// scanLocked returns the index of the pattern that matches earliest in
// the output not yet consumed, advancing the scan position past the
// whole match so it cannot be reported twice.
// Callers must hold s.mu.
func (s *Session) scanLocked(patterns []*regexp.Regexp) int {
	rest := s.pending[min(s.scanFrom, len(s.pending)):]
	matched := -1
	var matchLoc []int
	for i, re := range patterns {
		if loc := re.FindIndex(rest); loc != nil && (matched < 0 || loc[0] < matchLoc[0]) {
			matched = i
			matchLoc = loc
		}
	}
	if matched >= 0 {
		s.scanFrom += matchLoc[1]
		return matched
	}
	// Nothing matched: skip what was scanned, but keep a tail overlap
	// so a pattern split across two reads is still seen. Without this,
	// a long streaming command would be rescanned from its start on
	// every wait.
	if skip := len(s.pending) - ptyScanOverlap; skip > s.scanFrom {
		s.scanFrom = skip
	}
	return matched
}

// ptyScanOverlap is how much unconsumed output scanning keeps around
// after a fruitless pass: comfortably more than the longest prompt or
// sudo pattern, so one split across chunk boundaries still matches.
const ptyScanOverlap = 128

// WaitForPattern blocks until the pattern appears in output that arrived
// after this call, the session exits, the timeout elapses, or ctx is
// done. It reports whether the pattern was found.
func (s *Session) WaitForPattern(ctx context.Context, pattern *regexp.Regexp, timeout time.Duration) bool {
	s.mu.Lock()
	s.scanFrom = len(s.pending)
	s.mu.Unlock()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		s.mu.Lock()
		rest := s.pending[min(s.scanFrom, len(s.pending)):]
		loc := pattern.FindIndex(rest)
		if loc != nil {
			s.scanFrom += loc[1]
			s.mu.Unlock()
			return true
		}
		exited := s.exited
		s.mu.Unlock()

		if exited {
			return false
		}

		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-s.closed:
			return false
		case <-s.notify:
		}
	}
}

// WaitForQuiet blocks until output has been quiet for the given window,
// the session exits, the timeout elapses, or ctx is done. It reports
// whether the quiet window (or session exit) was reached.
func (s *Session) WaitForQuiet(ctx context.Context, quiet, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		s.mu.Lock()
		idleFor := time.Since(s.lastData)
		exited := s.exited
		s.mu.Unlock()

		if exited || idleFor >= quiet {
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-s.closed:
			return true
		case <-time.After(quiet - idleFor):
			// Re-check: output may have arrived meanwhile.
		}
	}
}

// WaitForAnyOrQuiet blocks until one of the patterns appears in output
// that no earlier match has consumed, output has stayed silent for the
// quiet window, the session exits, the timeout elapses, or ctx is done.
// It returns the index of the pattern that matched earliest (-1 when
// none matched) and whether the quiet window elapsed instead. Everything
// is driven by the arrival of output: no timers tick while bytes keep
// coming, and bytes that arrive between waits are not skipped.
func (s *Session) WaitForAnyOrQuiet(ctx context.Context, patterns []*regexp.Regexp, quiet, timeout time.Duration) (int, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		s.mu.Lock()
		matched := s.scanLocked(patterns)
		exited := s.exited
		idleFor := time.Since(s.lastData)
		s.mu.Unlock()

		if matched >= 0 {
			return matched, false
		}
		if exited {
			return -1, false
		}
		if idleFor >= quiet {
			return -1, true
		}

		select {
		case <-ctx.Done():
			return -1, false
		case <-deadline.C:
			return -1, false
		case <-s.closed:
			return -1, false
		case <-s.notify:
		case <-time.After(quiet - idleFor):
			// Re-check: a pattern may have matched in what arrived.
		}
	}
}

// WaitForOutput blocks until output arrives after this call, the session
// exits, the timeout elapses, or ctx is done. It reports whether new
// output arrived.
func (s *Session) WaitForOutput(ctx context.Context, timeout time.Duration) bool {
	s.mu.Lock()
	mark := len(s.pending)
	s.mu.Unlock()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		s.mu.Lock()
		grew := len(s.pending) > mark
		exited := s.exited
		s.mu.Unlock()
		if grew || exited {
			return grew
		}

		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-s.closed:
			return false
		case <-s.notify:
		}
	}
}

// IdleFor reports how long it has been since output last arrived. A
// send counts as activity.
func (s *Session) IdleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastData)
}

// RescanFromStart rewinds pattern scanning to the start of the
// undrained output, so the next WaitFor* call sees bytes that arrived
// before it began. Waiting only ever looks forward; this is for a
// caller that knows something may already have happened while nobody
// was watching.
func (s *Session) RescanFromStart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scanFrom = 0
}

// ResetWaitSample clears the CPU baseline used by WaitingForInput, so
// the next sample is compared against the moment the new wait began
// rather than an older one.
func (s *Session) ResetWaitSample() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitSampleValid = false
}

// JobActivity is what the terminal's foreground job is doing, measured
// from the OS rather than inferred from time passing. Callers sample it
// when output goes quiet to tell "stopped and waiting for input" from
// "working silently", and when a wait budget expires to tell "making
// progress" from "genuinely idle".
type JobActivity struct {
	// Observed: a foreground job other than the shell exists and could
	// be inspected.
	Observed bool
	// Asleep: every member process is sleeping - none running, stuck on
	// disk, or stopped.
	Asleep bool
	// InputWait: at least one member is blocked in a wait that input can
	// satisfy - a terminal read, or the generic interruptible wait points
	// (select/poll/wait_woken) a reader or multiplexer sits in. Kernels
	// differ in how much they expose; timers and child-reaping waits do
	// not count.
	InputWait bool
	// WchanReadable: kernel wait points were observable at all; without
	// them, a fully asleep job cannot be told apart from a tty read.
	WchanReadable bool
	// CPUDelta: CPU ticks the job consumed since the previous sample.
	CPUDelta uint64
	// RSSKB / RSSDeltaKB: the job's current resident set in KB, and how
	// much it grew since the previous sample (negative when shrunken).
	RSSKB      int64
	RSSDeltaKB int64
}

// Drain returns and clears the accumulated output.
func (s *Session) Drain() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending
	s.pending = nil
	s.scanFrom = 0
	return out
}

// AltScreen reports whether a full-screen program (an editor, a pager,
// a TUI) currently owns the terminal. While it does, the raw byte
// stream is a redraw log rather than output: read Screen instead.
func (s *Session) AltScreen() bool {
	s.emu.Lock()
	defer s.emu.Unlock()
	return s.emu.Mode()&vt10x.ModeAltScreen != 0
}

// Screen renders what a human would see right now: the emulator's grid
// as plain text, trailing blanks and trailing empty lines trimmed.
//
// The grid is everything a full-screen program leaves behind - it
// redraws rather than scrolling, so there is no history above its top
// line to recover. Ordinary command output does not come from here at
// all: the raw stream keeps every line, including the ones that
// scrolled past, so output is never limited to one screenful.
func (s *Session) Screen() string {
	s.emu.Lock()
	defer s.emu.Unlock()

	cols, rows := s.emu.Size()
	lines := make([]string, 0, rows)
	var sb strings.Builder
	for y := range rows {
		sb.Reset()
		sb.Grow(cols)
		for x := range cols {
			sb.WriteRune(s.emu.Cell(x, y).Char)
		}
		lines = append(lines, sb.String())
	}
	return trimScreen(lines)
}

// trimScreen drops trailing whitespace and trailing blank lines: an
// idle screen is a few lines of content, not a page of blanks.
func trimScreen(lines []string) string {
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// Cursor is the cursor's 0-indexed position on the rendered screen.
func (s *Session) Cursor() (row, col int) {
	s.emu.Lock()
	defer s.emu.Unlock()
	c := s.emu.Cursor()
	return c.Y, c.X
}

// Size is the session's current terminal size.
func (s *Session) Size() (rows, cols int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows, s.cols
}

// Alive reports whether the session process is still running.
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.exited
}

// Err returns the read-loop termination error, if any.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// ExitStatus returns the shell process's exit status once the process
// has been waited on: the code it exited with, -1 when a signal killed
// it, and whether the status is known at all. It can lag the session's
// own exit by a moment, since the read loop usually sees the exit
// first.
func (s *Session) ExitStatus() (code int, known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode, s.exitKnown
}

// Close terminates the session process and releases the PTY. It is
// safe to call more than once and from more than one goroutine: the
// wait goroutine releases the PTY when the shell exits on its own
// (onExit), and a runner being torn down closes the session it holds,
// so the same session can arrive here twice.
//
// The whole process tree is killed, not just the shell: a background
// or disowned child that stayed in the group would otherwise survive
// holding the slave end open, and with no reader left on the master
// the session never sees EIO - the wedged-terminal failure mode.
// A child that escaped the group with setsid is caught two ways: the
// PPid walk inside the group kill (while the shell still parents it),
// and a sweep of every process still holding the slave device, which
// also reaches double-forked daemons that kept their stdio.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		if s.job != 0 {
			// Kill-on-close: the job takes the shell and every
			// descendant assigned to it (Windows only).
			procgroup.CloseJob(s.job)
			s.job = 0
		}
		s.mu.Lock()
		exited := s.exited
		s.mu.Unlock()
		if !exited {
			// The shell is a session leader (Setsid), so its pid is the
			// group id and the group holds every descendant that did not
			// escape into a session of its own. Teardown skips the
			// interrupt grace: idle reapers close live shells routinely.
			procgroup.Kill(s.proc, 0)
		}
		procgroup.KillHolders(s.pty.Name())
		_ = s.pty.Close()
		// Ends the reply-forwarding goroutine.
		s.replies.close()
	})
	select {
	case <-s.closed:
	case <-time.After(5 * time.Second):
	}
}

// Pending returns a copy of the undrained output without consuming it,
// for a caller that has to look at what is on the wire before deciding
// what it is.
func (s *Session) Pending() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.pending...)
}

// PendingLen reports the size of undrained output.
func (s *Session) PendingLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}
