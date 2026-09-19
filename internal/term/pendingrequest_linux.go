//go:build linux

package term

import "golang.org/x/sys/unix"

// pendingInputRequest is the ioctl that reports the tty input queue's
// unread byte count on Linux.
const pendingInputRequest = unix.TIOCINQ

// flushPendingInput drops the tty's input queue on Linux: TCFLSH takes
// the queue selector as the ioctl's direct argument, not through a
// pointer.
func flushPendingInput(fd int) error {
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}
