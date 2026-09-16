//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package term

import "golang.org/x/sys/unix"

// secretReadFromLflag reads the local flags into a SecretReadState:
// echo on is an ordinary terminal whatever else is set; echo off with
// the line discipline still canonical is a hidden-line read; echo off
// in raw mode is either a cbreak credential reader or a program doing
// its own echo, which the caller settles with the prompt text.
func secretReadFromLflag(lflag uint32) SecretReadState {
	switch {
	case lflag&unix.ECHO != 0:
		return SecretReadNo
	case lflag&unix.ICANON != 0:
		return SecretReadYes
	default:
		return SecretReadRaw
	}
}
