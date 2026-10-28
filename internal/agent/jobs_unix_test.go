//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Cancelling a job kills its whole process group: a child the command
// spawned dies with it, not just the shell that spawned it.
func TestJobCancelKillsSpawningChild(t *testing.T) {
	t.Parallel()

	reg, logDir := newTestJobRegistry(t)
	workDir := t.TempDir()
	pidFile := filepath.Join(workDir, "child.pid")
	job, err := reg.start("s", "sleep 300 & echo $! > "+pidFile+"; wait", workDir, logDir)
	require.NoError(t, err)

	pid := waitChildPid(t, pidFile)
	require.NoError(t, reg.cancel("s", job.info().ID))
	require.Equal(t, JobCancelled, waitFinished(t, job).State)

	require.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) != nil
	}, 30*time.Second, 10*time.Millisecond, "the child of a cancelled job must die with it")
}

// waitChildPid reads the pid a job's command wrote, once it is there.
func waitChildPid(t *testing.T, path string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pid > 0
	}, 30*time.Second, 10*time.Millisecond, "the job never wrote its child's pid")
	return pid
}
