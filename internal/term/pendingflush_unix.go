//go:build !windows

package term

import (
	"os"

	"golang.org/x/sys/unix"
)

// DiscardPendingInput drops every byte sitting unread in the terminal's
// input queue: typed but not consumed by any program, it would otherwise
// reach the shell once the current command exits. The slave end is
// reopened by name for the flush (the startup copy was closed so the
// shell's exit stays visible) and closed again; O_NOCTTY|O_NONBLOCK
// acquires neither the controlling terminal nor a reader slot.
func (s *Session) DiscardPendingInput() {
	f, err := os.OpenFile(s.pty.Name(), os.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	defer f.Close()
	_ = flushPendingInput(int(f.Fd()))
}
