//go:build darwin

package procgroup

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// descendants returns the pids of every process whose ancestor chain
// reaches root, from the kernel's process table via sysctl (macOS has
// no /proc). A child that called setsid() leaves the process group -
// the group kill misses it - but stays parented until its parent dies,
// so collecting the tree while the root is still alive catches it.
func descendants(root int) []int {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for i := range procs {
		pid := int(procs[i].Proc.P_pid)
		if pid == root {
			continue
		}
		ppid := int(procs[i].Eproc.Ppid)
		children[ppid] = append(children[ppid], pid)
	}
	return bfsDescendants(children, root)
}

// killHolders SIGKILLs every process still holding the given terminal
// device open, discovered through lsof (macOS has no /proc/<pid>/fd to
// walk). It runs at session teardown on the session's own slave device,
// so a holder is by definition attached to a terminal that is going
// away. lsof itself is skipped when absent, and this process is never a
// target - it holds the master and one slave copy legitimately.
func killHolders(device string) {
	if device == "" {
		return
	}
	bin, err := exec.LookPath("lsof")
	if err != nil {
		return
	}
	self := os.Getpid()
	deadline := time.Now().Add(4 * time.Second)
	for {
		// -t: terse, pids only. lsof is slow (hundreds of ms), so the
		// poll budget is larger than /proc platforms'.
		out, err := exec.Command(bin, "-t", "--", device).Output()
		if err == nil {
			for line := range strings.SplitSeq(string(out), "\n") {
				pid, err := strconv.Atoi(strings.TrimSpace(line))
				if err != nil || pid == self {
					continue
				}
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		if err != nil || !time.Now().Before(deadline) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
