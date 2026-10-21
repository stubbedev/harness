package fsext

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoveAllWritableRemovesReadOnlyTrees(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not restrict deletion on Windows")
	}
	root := filepath.Join(t.TempDir(), "tree")
	nested := filepath.Join(root, "mod", "x@v1")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "x.go"), []byte("x"), 0o444))
	require.NoError(t, os.Chmod(nested, 0o555))
	require.NoError(t, os.Chmod(filepath.Join(root, "mod"), 0o500))

	require.Error(t, os.RemoveAll(root))
	require.NoError(t, RemoveAllWritable(root))
	require.NoDirExists(t, root)
}

func TestRemoveAllWritableMissingPath(t *testing.T) {
	t.Parallel()
	require.NoError(t, RemoveAllWritable(filepath.Join(t.TempDir(), "missing")))
}
