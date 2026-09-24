//go:build windows

package crash

import "os"

// processAlive reports whether a process with this id exists: on
// Windows, finding a process opens it, which fails once it is gone.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
