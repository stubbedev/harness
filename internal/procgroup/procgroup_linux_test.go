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
