package fsext

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShouldSkipTreeHonoursNestedIgnoreFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for rel, content := range map[string]string{
		".harnessignore":     "top.txt\n",
		"sub/.harnessignore": "private/\n/local.txt\n",
		"sub/.gitignore":     "*.gen\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	require.Equal(t, []string{".gitignore", ".harnessignore"}, IgnoreFileNames)

	walker := NewFastGlobWalker(root)
	for rel, skip := range map[string]bool{
		"keep.txt":              false,
		"local.txt":             false,
		"top.txt":               true,
		"sub/top.txt":           true,
		"sub/local.txt":         true,
		"sub/deeper/local.txt":  false,
		"sub/private/x.txt":     true,
		"sub/private/deep/y.go": true,
		"sub/run.gen":           true,
		"run.gen":               false,
		"node_modules/a/b.js":   true,
	} {
		require.Equal(t, skip, walker.ShouldSkipTree(filepath.Join(root, filepath.FromSlash(rel))), rel)
	}
	require.True(t, walker.ShouldSkipTree(filepath.Join(t.TempDir(), "outside.txt")))
}
