//go:build windows

package agent

import (
	"context"
	"errors"
	"os/exec"
	"syscall"

	"github.com/stubbedev/harness/internal/procgroup"
	"golang.org/x/sys/windows"
)

// startJobProcess starts cmd detached from this console and assigns it to
// a kill-on-close job object the moment it is running, so a cancel takes
// the whole tree - grandchildren included - rather than the direct child
// alone. Console ctrl events cannot be aimed at a detached process, so
// cancellation is a hard kill.
//
// The context is the job's own, never the tool call's (see
// jobRegistry.start). The returned stop ends the cancellation watch and
// releases the job handle; it must be called once the command has been
// waited for.
func startJobProcess(ctx context.Context, cmd *exec.Cmd) (func(), error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS
	cmd.WaitDelay = jobWaitDelay
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	job := procgroup.NewJob(cmd.Process)
	if job == 0 {
		// A job object is the only whole-tree kill Windows has; without
		// one the tree could not be cancelled, so the job never starts.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("could not isolate the background job's process tree")
	}
	stop := context.AfterFunc(ctx, func() {
		procgroup.TerminateJob(job)
	})
	return func() {
		stop()
		procgroup.CloseJob(job)
	}, nil
}
