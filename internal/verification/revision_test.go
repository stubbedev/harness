package verification

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRevisionHashesContentNotJustMetadata(t *testing.T) {
	t.Parallel()
	root := repository(t)
	path := filepath.Join(root, "src", "main.go")
	before, err := os.Stat(path)
	require.NoError(t, err)
	runner, err := New(root, testConfig(t, "pass"))
	require.NoError(t, err)
	result := runner.Run(t.Context(), []string{"src/main.go"})
	require.Equal(t, Passed, result.Status)
	require.NoError(t, os.WriteFile(path, []byte("modified"), 0o644))
	require.NoError(t, os.Chtimes(path, before.ModTime(), before.ModTime()))
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, before.Size(), after.Size())
	current, err := runner.Current(t.Context(), []string{"src/main.go"}, result)
	require.NoError(t, err)
	require.False(t, current)
}

func TestSymlinkInputsBlockButNoRulesSkip(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional privileges")
	}
	root := repository(t)
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "linked")))
	runner, err := New(root, testConfig(t, "pass"))
	require.NoError(t, err)
	result := runner.Run(t.Context(), []string{"src/main.go"})
	require.Equal(t, Blocked, result.Status)
	require.Contains(t, result.Reason, "directory symlink")
	require.Empty(t, result.Checks)
	require.Equal(t, Skipped, runner.Run(t.Context(), []string{"README.md"}).Status)
	require.NoError(t, os.Remove(filepath.Join(root, "linked")))
	require.NoError(t, os.Symlink(filepath.Join(root, "src", "main.go"), filepath.Join(root, "src", "link.go")))
	result = runner.Run(t.Context(), []string{"src/main.go"})
	require.Equal(t, Blocked, result.Status)
	require.Contains(t, result.Reason, "non-regular")
}
