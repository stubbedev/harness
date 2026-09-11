// Package term provides a persistent pseudo-terminal session: a real
// interactive shell spawned in a PTY, kept alive for the lifetime of the
// process. It is the pure-Go foundation for running commands that might
// otherwise hang a harness - interactive prompts, REPLs, TUIs - because
// the harness can send raw input and read raw output at any time instead
// of blocking on pipes that a program is waiting to write into.
//
// The session is intentionally low-level: it deals in bytes, quiet
// windows and regex waits. Command/exit-code semantics live one layer up
// (the bash tool), which uses a printed sentinel to recover exit codes
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

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
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

	// minRows/minCols/maxRows/maxCols bound the size an override or a
	// Resize call can ask for; a degenerate grid breaks the emulator and
	// an enormous one turns every screen read into a wall of blanks.
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

	ptmx     *os.File
	proc     *os.Process
	pending  []byte    // output not yet drained
	scanFrom int       // where WaitFor pattern scanning continues from
	lastData time.Time // last time output arrived
	exited   bool
	err      error

	// emu is a headless terminal emulator fed the same bytes as pending.
	// The raw stream is the right view of a normal command (it keeps
	// everything, including output that scrolled past the screen), but
	// it is unreadable for a full-screen program: cursor addressing and
	// redraws only mean something once they have been applied to a grid.
	// Screen renders that grid; AltScreen says which view to use.
	emu        vt10x.Terminal
	replies    *replyWriter
	rows, cols int

	// bracketedPaste is whatever is running asking for pasted text to be
	// marked as a paste; modeCarry holds the tail of the last chunk so a
	// mode sequence split across two reads is still seen.
	bracketedPaste bool
	modeCarry      []byte
}

// Shell returns the shell the terminal session should run: the shell
// Harness itself was launched from when it can be identified (the parent
// process being one), then the user's login shell ($SHELL), then
// /bin/sh. Non-POSIX shells are not driven directly because the
// completion sentinel uses POSIX parameter expansion; they fall back
// to /bin/sh.
func Shell() string {
	if sh := parentShell(); sh != "" {
		return sh
	}
	if sh := os.Getenv("SHELL"); sh != "" && isPosixShell(sh) {
		return sh
	}
	return "/bin/sh"
}

// posixShells are the shells whose builtin printf understands the
// '__exit:%d@%s__' "$?" "$PWD" sentinel syntax.
var posixShells = map[string]bool{
	"bash": true,
	"zsh":  true,
	"sh":   true,
	"dash": true,
	"ksh":  true,
	"ash":  true,
}

func isPosixShell(path string) bool {
	base := strings.TrimSuffix(filepath.Base(path), ".exe")
	return posixShells[base]
}

// parentShell reports the shell hosting the Harness process by looking
// at the parent process name; empty when the parent is not a shell
// (terminal emulator, systemd, an editor task runner, ...).
func parentShell() string {
	ppid := os.Getppid()
	if ppid <= 1 {
		return ""
	}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", ppid))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(comm))
	name = strings.TrimSuffix(name, "\n")
	if !posixShells[name] {
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
// win.
// The shell is deliberately not bound to a caller context: the session
// outlives the request that opened it and is torn down by Close.
func Start(cwd string, env ...string) (*Session, error) {
	cmd := exec.CommandContext(context.Background(), Shell())
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "TERM="+termValue())

	rows, cols := DefaultSize()
	cmd.Env = append(cmd.Env,
		"LINES="+strconv.Itoa(rows),
		"COLUMNS="+strconv.Itoa(cols),
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start terminal session: %w", err)
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})

	return newSession(ptmx, cmd.Process, rows, cols), nil
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
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return fallback
	}
	return v
}

func clampDim(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

func newSession(ptmx *os.File, proc *os.Process, rows, cols int) *Session {
	replies := newReplyWriter()
	emu := vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithWriter(replies))
	s := &Session{
		ptmx:     ptmx,
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
		n, err := s.ptmx.Read(buf)
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
	_, err := s.ptmx.Write(b)
	return err
}

// WaitForAny blocks until one of the patterns appears in output that
// arrived after this call, the session exits, the timeout elapses, or
// ctx is done. It returns the index of the pattern that matched
// earliest in the output, or -1 if no pattern matched.
func (s *Session) WaitForAny(ctx context.Context, patterns []*regexp.Regexp, timeout time.Duration) int {
	s.mu.Lock()
	s.scanFrom = len(s.pending)
	s.mu.Unlock()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		s.mu.Lock()
		rest := s.pending[min(s.scanFrom, len(s.pending)):]
		matched := -1
		var matchStart int
		for i, re := range patterns {
			if loc := re.FindIndex(rest); loc != nil && (matched < 0 || loc[0] < matchStart) {
				matched = i
				matchStart = loc[0]
			}
		}
		if matched >= 0 {
			s.scanFrom += matchStart
			s.mu.Unlock()
			return matched
		}
		exited := s.exited
		s.mu.Unlock()

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

// Resize changes the terminal size, signalling SIGWINCH to whatever is
// running so it redraws at the new size. Dimensions are clamped to the
// supported range.
func (s *Session) Resize(rows, cols int) error {
	rows = clampDim(rows, minRows, maxRows)
	cols = clampDim(cols, minCols, maxCols)

	s.mu.Lock()
	exited := s.exited
	if !exited {
		s.rows, s.cols = rows, cols
	}
	s.mu.Unlock()
	if exited {
		return errors.New("terminal session has exited")
	}

	if err := pty.Setsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		return fmt.Errorf("resize terminal session: %w", err)
	}
	s.emu.Resize(cols, rows)
	return nil
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

// Close terminates the session process and releases the PTY.
func (s *Session) Close() {
	s.mu.Lock()
	exited := s.exited
	s.mu.Unlock()
	if !exited {
		_ = s.proc.Kill()
	}
	_ = s.ptmx.Close()
	// Ends the reply-forwarding goroutine.
	s.replies.close()
	select {
	case <-s.closed:
	case <-time.After(5 * time.Second):
	}
}

// PendingLen reports the size of undrained output.
func (s *Session) PendingLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}
