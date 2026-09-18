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

func killGroup(proc *os.Process, grace time.Duration) {
	pgid := proc.Pid
	if grace <= 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = gone(pgid, 500*time.Millisecond)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGINT)
	if gone(pgid, grace) {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	_ = gone(pgid, 250*time.Millisecond)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
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
