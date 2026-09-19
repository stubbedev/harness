package verification

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/stubbedev/harness/internal/procgroup"
)

type boundedOutput struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(p), b.limit-len(b.data))
	b.data = append(b.data, p[:n]...)
	b.truncated = b.truncated || n < len(p)
	return len(p), nil
}

func (r *Runner) runCheck(ctx context.Context, rule Rule) CheckResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, rule.timeout())
	defer cancel()
	result := CheckResult{Name: rule.Name, Command: append([]string{}, rule.Command...), Status: Blocked, ExitCode: -1}
	output := &boundedOutput{limit: rule.outputLimit()}
	cmd := exec.CommandContext(ctx, rule.Command[0], rule.Command[1:]...)
	cmd.Dir = r.root
	cmd.Stdout = output
	cmd.Stderr = output
	isolate(cmd)
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error { return nil }
	err := cmd.Start()
	if err == nil {
		job := procgroup.NewJob(cmd.Process)
		if runtime.GOOS == "windows" && job == 0 {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			result.Reason = "failed to isolate verification process tree"
			result.Duration = time.Since(started)
			return result
		}
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { procgroup.TerminateJob(job); procgroup.Kill(cmd.Process, 0); close(done) })
		err = cmd.Wait()
		if !stop() {
			<-done
		}
		procgroup.CloseJob(job)
	}
	result.Duration = time.Since(started)
	output.mu.Lock()
	result.Output = string(output.data)
	result.Truncated = output.truncated
	output.mu.Unlock()
	if ctx.Err() != nil {
		result.Reason = ctx.Err().Error()
		result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	} else if err == nil {
		result.Status = Passed
		result.ExitCode = 0
	} else if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		result.Status = Failed
		result.ExitCode = exitErr.ExitCode()
		result.Reason = err.Error()
	} else {
		result.Reason = err.Error()
	}
	return result
}
