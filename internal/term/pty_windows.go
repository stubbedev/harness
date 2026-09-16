package term

import "github.com/aymanbagabas/go-pty"

// afterStart has nothing to release on Windows: ConPTY hands out pipes,
// not a slave descriptor.
func afterStart(pty.Pty) {}

// onExit closes the pseudo console once the shell has exited. ConPTY
// keeps the output pipe open until the console itself is closed, so
// without this the read loop would never see EOF and the session would
// look alive after its shell was gone. The remaining output drains
// before the EOF arrives.
func onExit(p pty.Pty) {
	_ = p.Close()
}
