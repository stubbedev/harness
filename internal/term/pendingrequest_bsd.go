//go:build darwin || dragonfly || freebsd || netbsd || openbsd || solaris

package term

import "golang.org/x/sys/unix"

// pendingInputRequest is the ioctl that reports the tty input queue's
// unread byte count on the BSDs and Solaris: FIONREAD, which x/sys/unix
// does not re-export for these platforms.
const pendingInputRequest = 0x4004667f

// flushPendingInput drops the tty's input queue on the BSDs. TIOCFLUSH
// takes a pointer to the flags to flush - FREAD being the input queue -
// not the value itself: passing the value would have the kernel copy in
// from that address, and the flush would fail silently (EFAULT).
func flushPendingInput(fd int) error {
	return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, 1)
}
