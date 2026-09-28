package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFallbackSearchPrunesIgnoredDirectories pins the no-rg fallback to
// the same pruning rg does: always-ignored directories and directory-only
// gitignore rules must stop the walk, which they could not while the
// walker was never told an entry was a directory.
func TestFallbackSearchPrunesIgnoredDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	write(".gitignore", "generated/\n")
	write("node_modules/dep/index.txt", "needle\n")
	write("generated/out.txt", "needle\n")
	write("src/main.txt", "needle\n")

	matches, err := searchFilesWithRegex("needle", root, "")
	require.NoError(t, err)
	var paths []string
	for _, m := range matches {
		rel, relErr := filepath.Rel(root, m.path)
		require.NoError(t, relErr)
		paths = append(paths, filepath.ToSlash(rel))
	}
	require.Equal(t, []string{"src/main.txt"}, paths)
}
