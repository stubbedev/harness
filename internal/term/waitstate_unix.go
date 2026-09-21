//go:build unix

package term

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// foregroundPgrp returns the process group currently reading the
// terminal, or 0 when it cannot be determined.
func (s *Session) foregroundPgrp() int {
	pgrp := 0
	_ = s.masterControl(func(fd int) {
		if g, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err == nil {
			pgrp = g
		}
	})
	return pgrp
}

// KillForeground sends SIGTERM to the foreground process group when it
// is a job rather than the shell itself. It is the step after a ctrl-c
// the program ignored: a job that survives its interrupt would otherwise
// run on into the next call's output.
func (s *Session) KillForeground() error {
	pgrp := s.foregroundPgrp()
	if pgrp <= 0 {
		return errors.New("no foreground process group")
	}
	if s.proc != nil && pgrp == s.proc.Pid {
		return errors.New("the shell is in the foreground")
	}
	return unix.Kill(-pgrp, syscall.SIGTERM)
}
