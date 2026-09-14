//go:build !windows

package term

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// parentProcessName returns the executable name of the process that
// launched Harness, or "" when it cannot be read. Linux answers from
// /proc; the BSDs and macOS have no such file, so they are asked through
// ps, which reports a login shell as "-zsh".
func parentProcessName() string {
	ppid := os.Getppid()
	if ppid <= 1 {
		return ""
	}
	if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", ppid)); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(string(comm)), "-")
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(ppid)).Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimPrefix(name, "-")
}
