//go:build linux

package procgroup

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParentPid(t *testing.T) {
	t.Parallel()

	ppid, ok := parentPid(os.Getpid())
	require.True(t, ok)
	require.Positive(t, ppid)
}

func TestDescendantsFindsChild(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), "sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	children := map[int][]int{}
	ppid, ok := parentPid(cmd.Process.Pid)
	require.True(t, ok)
	children[ppid] = append(children[ppid], cmd.Process.Pid)
	require.Equal(t, []int{cmd.Process.Pid}, bfsDescendants(children, os.Getpid()))
}

// TestPinnedStrayOnlyKillsItsOwnProcess pins a live process, lets it exit
// and be reaped, and requires the pinned kill to be a no-op rather than
// signalling whatever might now own the pid.
func TestPinnedStrayOnlyKillsItsOwnProcess(t *testing.T) {
	t.Parallel()

	live := exec.CommandContext(t.Context(), "sleep", "30")
	require.NoError(t, live.Start())
	s := pin(live.Process.Pid)
	defer s.release()
	start, ok := startTime(live.Process.Pid)
	require.True(t, ok)
	require.NotZero(t, start)

	s.kill()
	err := live.Wait()
	require.Error(t, err, "the pinned process is killed")

	s.kill() // Its pid is free now; this must not signal anything.
}
