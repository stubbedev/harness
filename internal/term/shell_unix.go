//go:build !windows

package term

import (
	"fmt"
	"os"
	"strings"
)

// parentProcessName returns the executable name of the process that
// launched Harness, or "" when it cannot be read. Linux answers from
// /proc; elsewhere the caller falls back to $SHELL.
func parentProcessName() string {
	ppid := os.Getppid()
	if ppid <= 1 {
		return ""
	}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", ppid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(comm))
}

// defaultShell is the last resort when neither the parent process nor
// $SHELL names a shell this package can drive.
func defaultShell() string { return "/bin/sh" }
