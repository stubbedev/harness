//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package term

import "golang.org/x/sys/unix"

// secretRead samples the slave's termios through the master fd. The
// master and the slave share one line discipline, so TIOCGETA here
// observes exactly what the foreground program set.
func (s *Session) secretRead() SecretReadState {
	state := SecretReadUnknown
	_ = s.masterControl(func(fd int) {
		if t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA); err == nil {
			state = secretReadFromLflag(uint32(t.Lflag))
		}
	})
	return state
}
