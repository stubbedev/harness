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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

const (
	// DefaultRows and DefaultCols are the terminal size the session is
	// opened with. Generous width keeps build output from wrapping into
	// unreadable columns.
	DefaultRows = 50
	DefaultCols = 200

	// maxPending caps the undrained output buffer so a runaway process
	// cannot grow memory without bound.
	maxPending = 4 << 20 // 4 MiB
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
}

// Shell returns the shell the terminal session should run: the shell
// Crush itself was launched from when it can be identified (the parent
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

// parentShell reports the shell hosting the Crush process by looking
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
func Start(cwd string, env ...string) (*Session, error) {
	cmd := exec.Command(Shell())
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "TERM="+termValue())

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start terminal session: %w", err)
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: DefaultRows, Cols: DefaultCols})

	return newSession(ptmx, cmd.Process), nil
}

func termValue() string {
	if t := os.Getenv("TERM"); t != "" {
		return t
	}
	return "xterm-256color"
}

func newSession(ptmx *os.File, proc *os.Process) *Session {
	s := &Session{
		ptmx:     ptmx,
		proc:     proc,
		notify:   make(chan struct{}, 1),
		closed:   make(chan struct{}),
		lastData: time.Now(),
	}
	go s.readLoop()
	return s
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
	s.mu.Lock()
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
