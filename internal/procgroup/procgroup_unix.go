//go:build !windows

package procgroup

import (
	"os"
	"syscall"
	"time"
)

// pollInterval is how often the group is checked while waiting for it
// to die. Coarse on purpose: the waits are seconds-long bounds, not
// latency paths.
const pollInterval = 20 * time.Millisecond

// killStrays SIGKILLs the collected setsid escapees alongside the group.
func killStrays(strays []int) {
	for _, pid := range strays {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// bfsDescendants expands a pid -> children map into root's full
// descendant list. Shared by the /proc and sysctl tree builders.
func bfsDescendants(children map[int][]int, root int) []int {
	var out []int
	queue := []int{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range children[cur] {
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}

func killGroup(proc *os.Process, grace time.Duration) {
	pgid := proc.Pid
	// Collect the descendant tree while the root is alive: a child
	// that called setsid() is outside the group and survives its kill,
	// but stays parented until the root dies, so PPid links still reach
	// it here (see descendants).
	strays := descendants(pgid)
	kill := func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		killStrays(strays)
	}
	if grace <= 0 {
		kill()
		_ = gone(pgid, 500*time.Millisecond)
		kill()
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGINT)
	if gone(pgid, grace) {
		// The group died of the interrupt; setsid strays were never
		// signalled, so kill them now.
		killStrays(strays)
		return
	}
	kill()
	_ = gone(pgid, 250*time.Millisecond)
	kill()
}

// gone reports whether no process remains in the group, polling until
// it is empty or d passes. Signal 0 to the negative pid probes the
// group as a whole: it fails with ESRCH once the last member is reaped.
func gone(pgid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if syscall.Kill(-pgid, 0) != nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// Job objects are a Windows concept; elsewhere NewJob reports none and
// the job calls are no-ops (term sessions store the 0 handle).

// NewJob is the Windows job-object constructor; 0 on non-Windows.
func NewJob(_ *os.Process) uintptr { return 0 }

// TerminateJob kills a job's whole tree; a no-op on non-Windows.
func TerminateJob(_ uintptr) {}

// CloseJob releases a job handle; a no-op on non-Windows.
func CloseJob(_ uintptr) {}
