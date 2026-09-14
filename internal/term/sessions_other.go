//go:build !windows

package term

// SessionsSupported reports whether this platform can open a terminal
// session at all. The pty package this builds on has no Windows
// implementation, so a session there fails however the shell is found;
// callers ask this before offering anything that needs one.
const SessionsSupported = true
