//go:build linux

package term

import "golang.org/x/sys/unix"

// pendingInputRequest is the ioctl that reports the tty input queue's
// unread byte count on Linux.
const pendingInputRequest = unix.TIOCINQ

// discardInputRequest and discardInputArg flush the input queue on
// Linux: tcflush(fd, TCIFLUSH) as an ioctl.
const (
	discardInputRequest = unix.TCFLSH
	discardInputArg     = unix.TCIFLUSH
)
