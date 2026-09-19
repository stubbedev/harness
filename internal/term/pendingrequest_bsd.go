//go:build darwin || dragonfly || freebsd || netbsd || openbsd || solaris

package term

import "golang.org/x/sys/unix"

// pendingInputRequest is the ioctl that reports the tty input queue's
// unread byte count on the BSDs and Solaris: FIONREAD, which x/sys/unix
// does not re-export for these platforms.
const pendingInputRequest = 0x4004667f

// discardInputRequest and discardInputArg flush the input queue on the
// BSDs: TIOCFLUSH with the read flag.
const (
	discardInputRequest = unix.TIOCFLUSH
	discardInputArg     = 1
)
