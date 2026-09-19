//go:build !windows

package term

import (
	"os"

	"golang.org/x/sys/unix"
)

// PendingInput reports how many bytes are sitting unread in the
// terminal's input queue: typed but not consumed by any program. The
// slave end is reopened by name for the probe (the startup copy was
// closed so the shell's exit stays visible) and closed again; opening
// O_NOCTTY|O_NONBLOCK acquires neither the controlling terminal nor a
// reader slot, so the probe moves nothing.
func (s *Session) PendingInput() int {
	f, err := os.OpenFile(s.pty.Name(), os.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return 0
	}
	defer f.Close()
	n, err := unix.IoctlGetInt(int(f.Fd()), pendingInputRequest)
	if err != nil {
		return 0
	}
	return n
}
