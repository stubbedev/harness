package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathExistsJoinsRelativePathsOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), nil, 0o600))
	require.True(t, pathExists(root, "a.go", false))
	require.True(t, pathExists(root, filepath.Join(root, "a.go"), false))
	require.False(t, pathExists(root, filepath.Join(t.TempDir(), "a.go"), false))
	if runtime.GOOS == "windows" {
		require.False(t, pathExists(root, "/a.go", false))
	}
}
