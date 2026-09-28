package filetracker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEvidenceKeyFollowsSymlinks pins the case the shared key fixes: a
// file viewed through a symlinked directory and edited by its real path is
// one file, so the read counts for the edit.
func TestEvidenceKeyFollowsSymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	real := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(real, 0o755))
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	file := filepath.Join(real, "a.go")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	require.Equal(t, evidenceKey("s", file), evidenceKey("s", filepath.Join(link, "a.go")))
	require.Equal(t, evidenceKey("s", filepath.Join(real, "new.go")), evidenceKey("s", filepath.Join(link, "new.go")),
		"a file not written yet resolves through its existing directory")
}
