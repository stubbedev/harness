//go:build !windows

package agent

import (
	"context"
	"os/exec"
	"syscall"

	"github.com/stubbedev/harness/internal/procgroup"
)

// startJobProcess starts cmd as its own process group, detached from
// Harness's, and arranges for ctx cancellation to kill that whole group: a
// background job must not die with the call that started it, but it must
// not outlive a cancel or a shutdown either.
//
// The context is the job's own, never the tool call's (see
// jobRegistry.start). The returned stop must be called once the command
// has been waited for; Unix needs nothing there.
func startJobProcess(_ context.Context, cmd *exec.Cmd) (func(), error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Setpgid makes the command a group leader (pgid == pid), so the
	// kill can signal the negated pid and take its children with it.
	cmd.SysProcAttr.Setpgid = true
	cmd.WaitDelay = jobWaitDelay
	// Replaces os/exec's default cancel, which signals the direct child
	// only. grace 0 is the right call for a job nobody waits on: no traps
	// to run, just the tree gone before the record reports cancelled.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		procgroup.Kill(cmd.Process, 0)
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
