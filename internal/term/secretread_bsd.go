//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package term

import "golang.org/x/sys/unix"

// secretRead samples the slave's termios through the master fd. The
// master and the slave share one line discipline, so TIOCGETA here
// observes exactly what the foreground program set.
func (s *Session) secretRead() SecretReadState {
	t, err := unix.IoctlGetTermios(int(s.ptmx.Fd()), unix.TIOCGETA)
	if err != nil {
		return SecretReadUnknown
	}
	return secretReadFromLflag(uint32(t.Lflag))
}
