//go:build !unix

package term

// ForegroundIsShell has no process table answer on this platform; the
// caller falls back to its tty-state heuristics.
func (s *Session) ForegroundIsShell() bool {
	return false
}
