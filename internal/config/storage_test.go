package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrateLegacyDataDir(t *testing.T) {
	t.Setenv("HARNESS_GLOBAL_DATA", t.TempDir())
	workingDir := t.TempDir()

	legacy := filepath.Join(workingDir, defaultDataDirectory)
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "state.yaml"), []byte("options:\n  data_directory: .harness\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "harness.db"), []byte("sqlite"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "harness.lock"), []byte("lock"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "commands", "review.md"), []byte("# /review\n"), 0o644))

	dir := DefaultWorkspaceDataDirectory(workingDir)
	migrateLegacyDataDir(workingDir, dir)

	require.FileExists(t, filepath.Join(dir, "state.yaml"))
	require.FileExists(t, filepath.Join(dir, "harness.db"))
	require.FileExists(t, filepath.Join(dir, "commands", "review.md"))
	require.NoFileExists(t, filepath.Join(dir, "harness.lock"), "the lock file must not be copied")
	// The legacy directory is left in place for the user to remove.
	require.DirExists(t, legacy)

	// Re-running with the destination populated is a no-op: a marker
	// file written into the destination survives untouched.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "marker"), []byte("new"), 0o644))
	migrateLegacyDataDir(workingDir, dir)
	require.FileExists(t, filepath.Join(dir, "marker"))
}

func TestMigrateLegacyDataDirSkipsWithoutLegacy(t *testing.T) {
	t.Setenv("HARNESS_GLOBAL_DATA", t.TempDir())
	workingDir := t.TempDir()
	dir := DefaultWorkspaceDataDirectory(workingDir)

	migrateLegacyDataDir(workingDir, dir)

	require.NoDirExists(t, dir, "no legacy directory means nothing to create")
}
