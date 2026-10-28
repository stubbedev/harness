//go:build windows

package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The job tests proper drive a POSIX shell, so they skip on Windows. These
// two cover what only Windows can: the job object the process tree is
// assigned to, without which a cancel could not reach a grandchild.

// A command that ends is reported with its output and its exit status.
func TestJobRunsUnderCmd(t *testing.T) {
	reg, logDir := newTestJobRegistry(t)
	job, err := reg.start("s", "echo hello", t.TempDir(), logDir)
	require.NoError(t, err)

	info := waitFinished(t, job)
	require.Equal(t, JobExited, info.State)
	require.NotNil(t, info.ExitCode)
	require.Equal(t, 0, *info.ExitCode)
	require.Contains(t, info.Output, "hello")
}

// Cancelling a job ends it: the job object takes the whole tree down, so
// the state is cancelled rather than the command's own exit.
func TestJobCancelEndsItUnderCmd(t *testing.T) {
	reg, logDir := newTestJobRegistry(t)
	job, err := reg.start("s", "ping -n 60 127.0.0.1", t.TempDir(), logDir)
	require.NoError(t, err)
	require.Equal(t, JobRunning, job.info().State)

	require.NoError(t, reg.cancel("s", job.info().ID))
	require.Equal(t, JobCancelled, waitFinished(t, job).State)
}
