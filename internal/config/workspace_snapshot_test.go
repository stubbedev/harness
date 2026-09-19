package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceSnapshotDoesNotChangeParent(t *testing.T) {
	t.Parallel()
	parentRoot, childRoot := t.TempDir(), t.TempDir()
	parent := NewTestStoreWithWorkingDir(&Config{Options: &Options{}}, parentRoot)
	child := parent.WorkspaceSnapshot(childRoot)
	require.Equal(t, parentRoot, parent.WorkingDir())
	require.Equal(t, childRoot, child.WorkingDir())
	require.NotSame(t, parent.Config(), child.Config())
	require.NotSame(t, parent.Config().Options, child.Config().Options)
}
