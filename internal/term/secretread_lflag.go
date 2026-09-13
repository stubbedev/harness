//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package term

import "golang.org/x/sys/unix"

// secretReadFromLflag reads the local flags the secret-read detector
// turns on: ECHO cleared (the typed line is not printed) while ICANON
// stays set (the tty still buffers the line). Line editors clear
// ICANON to edit keystroke by keystroke, and ordinary questions leave
// ECHO alone, so neither is mistaken for a hidden-line read.
func secretReadFromLflag(lflag uint32) SecretReadState {
	const hiddenLine = unix.ECHO | unix.ICANON
	switch lflag & hiddenLine {
	case unix.ICANON:
		return SecretReadYes
	default:
		return SecretReadNo
	}
}
