package memory

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileKeyJoinsRelativePathsOnly(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	require.Equal(t, fileKey(filepath.Join(base, "remote.git"), ""), fileKey("remote.git", base))
	abs := filepath.Join(t.TempDir(), "remote.git")
	require.Equal(t, fileKey(abs, ""), fileKey(abs, base))
	if runtime.GOOS == "windows" {
		require.Equal(t, "file:/srv/remote", fileKey("/srv/remote.git", base))
	}
}
