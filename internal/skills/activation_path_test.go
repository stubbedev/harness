package skills

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActivationPathJoinsRelativePathsOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rel, ok := activationPath(root, filepath.Join("sub", "a.go"))
	require.True(t, ok)
	require.Equal(t, "sub/a.go", rel)
	rel, ok = activationPath(root, filepath.Join(root, "sub", "a.go"))
	require.True(t, ok)
	require.Equal(t, "sub/a.go", rel)
	_, ok = activationPath(root, filepath.Join(t.TempDir(), "a.go"))
	require.False(t, ok)
	if runtime.GOOS == "windows" {
		_, ok = activationPath(root, "/elsewhere/a.go")
		require.False(t, ok)
	}
}
