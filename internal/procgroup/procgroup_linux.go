//go:build linux

package procgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// descendants returns the pids of every process whose ancestor chain
// reaches root, following PPid links through /proc. A child that called
// setsid() leaves the process group - the group kill misses it - but it
// stays parented until its parent dies, so collecting the tree while
// the root is still alive catches it. Best effort: a process that forks
// and is re-parented between the walk and the kill can slip through;
// the tty sweep covers the ones that kept their terminal.
func descendants(root int) []int {
	children := map[int][]int{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == root {
			continue
		}
		ppid, ok := parentPid(pid)
		if !ok {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	return bfsDescendants(children, root)
}

// parentPid reads a process's parent pid from /proc/<pid>/stat. The comm
// field can contain spaces and parentheses, so the stat fields are
// parsed after its last closing parenthesis; PPid is the second field
// after it (state, then ppid).
func parentPid(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

// killHolders SIGKILLs every process still holding the given terminal
// device open, discovered through /proc/<pid>/fd symlinks. It runs at
// session teardown on the session's own slave device, so a holder is by
// definition attached to a terminal that is going away - typically a
// daemon that double-forked out of the process group and closed its
// PPid chain but kept its stdio. Processes that cannot be signalled
// (not ours) and transient /proc entries are skipped.
func killHolders(device string) {
	if device == "" {
		return
	}
	self := os.Getpid()
	deadline := time.Now().Add(2 * time.Second)
	for {
		killed := false
		entries, err := os.ReadDir("/proc")
		if err == nil {
			for _, e := range entries {
				pid, err := strconv.Atoi(e.Name())
				if err != nil || pid == self {
					continue
				}
				if holdsDevice(pid, device) {
					// SIGKILL directly: teardown semantics. The pid may
					// be gone or unsignallable by the time this runs;
					// both are fine here.
					_ = syscall.Kill(pid, syscall.SIGKILL)
					killed = true
				}
			}
		}
		if !killed || !time.Now().Before(deadline) {
			return
		}
		time.Sleep(pollInterval)
	}
}

// holdsDevice reports whether the process has any open descriptor
// resolving to device.
func holdsDevice(pid int, device string) bool {
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return false
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", fd.Name()))
		if err != nil {
			continue
		}
		if strings.TrimSuffix(target, " (deleted)") == device {
			return true
		}
	}
	return false
}
