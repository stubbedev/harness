//go:build !windows

package term

import (
	"errors"

	"github.com/aymanbagabas/go-pty"
)

// afterStart releases the slave end once the shell holds it. The pty
// package keeps the slave open in this process, and while any slave
// descriptor is open the master never reads EOF - so a shell that exits
// would leave a session that still looks alive. Closing this copy makes
// the shell's own exit visible on the master as EIO.
func afterStart(p pty.Pty) {
	if u, ok := p.(pty.UnixPty); ok {
		_ = u.Slave().Close()
	}
}

// onExit is what the wait goroutine does once the shell has exited. On
// Unix the read loop notices on its own (see afterStart), so nothing is
// needed here.
func onExit(pty.Pty) {}

// masterControl runs f on the master descriptor without changing its
// blocking mode. Going through (*os.File).Fd would put the descriptor
// into blocking mode and take the read loop off the runtime poller,
// after which Close could no longer unblock it.
func (s *Session) masterControl(f func(fd int)) error {
	u, ok := s.pty.(pty.UnixPty)
	if !ok {
		return errors.New("not a unix pty")
	}
	return u.Control(func(fd uintptr) { f(int(fd)) })
}
