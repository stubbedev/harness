//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package vt10x

import (
	"os"

	"golang.org/x/sys/unix"
)

func ResizePty(pty *os.File, cols, rows int) error {
	return unix.IoctlSetWinsize(int(pty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row:    uint16(rows),
		Col:    uint16(cols),
		Xpixel: 16 * uint16(cols),
		Ypixel: 16 * uint16(rows),
	})
}
