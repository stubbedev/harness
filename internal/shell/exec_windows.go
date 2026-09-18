//go:build windows

package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/interp"

	"github.com/stubbedev/harness/internal/procgroup"
	"golang.org/x/sys/windows"
)

// defaultKillTimeout matches mvdan's DefaultExecHandler default; kept
// for symmetry with the Unix handler even though Windows cancellation
// is always a hard job kill.
const defaultKillTimeout = 2 * time.Second

// isolateProcess detaches the child from our console: a new process
// group keeps it from receiving our ctrl-c events, DETACHED_PROCESS
// gives it no console of ours to grab. The Unix Setsid equivalent.
func isolateProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS
}

// processGroupExecHandler mirrors the Unix handler's isolation and
// teardown guarantees with Windows primitives: the child runs detached
// in its own process group, is assigned to a kill-on-close job object
// the moment it starts, and a cancelled context terminates the job -
// taking the whole tree, grandchildren included - instead of leaving
// them orphaned like interp.DefaultExecHandler's direct-child kill.
// There is no graceful first signal here: console ctrl events cannot
// be targeted at a detached process, so cancellation is a hard kill.
func processGroupExecHandler(_ time.Duration) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)
		path, err := interp.LookPathDir(hc.Dir, hc.Env, args[0])
		if err != nil {
			fmt.Fprintln(hc.Stderr, err)
			return interp.ExitStatus(127)
		}

		cmd := newIsolatedCmd(hc, path, args, hc.Stdin, hc.Stdout, hc.Stderr)

		err = cmd.Start()
		if err == nil {
			job := procgroup.NewJob(cmd.Process)
			defer procgroup.CloseJob(job)
			stopf := context.AfterFunc(ctx, func() {
				procgroup.TerminateJob(job)
			})
			defer stopf()

			err = cmd.Wait()
		}

		return exitStatusFromError(ctx, hc.Stderr, err)
	}
}

// exitStatusFromError translates an exec error into an interp exit
// status, matching the conventions of interp.DefaultExecHandler.
func exitStatusFromError(ctx context.Context, stderr io.Writer, err error) error {
	if err == nil {
		return nil
	}
	var execErr *exec.ExitError
	if errors.As(err, &execErr) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return interp.ExitStatus(uint8(execErr.ExitCode()))
	}
	var pathErr *exec.Error
	if errors.As(err, &pathErr) {
		fmt.Fprintf(stderr, "%v\n", err)
		return interp.ExitStatus(127)
	}
	return err
}
