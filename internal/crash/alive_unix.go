//go:build !windows

package crash

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with this id exists. A process
// this user may not signal still exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
