package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/envvars"
)

// TestProjectCommandsLiveInTheRepository pins the project sources to the
// repository, where they can be committed, and keeps the data directory as
// the fallback it used to be the only home of.
func TestProjectCommandsLiveInTheRepository(t *testing.T) {
	t.Setenv(envvars.CommandsDir, t.TempDir())
	repo, dataDir := t.TempDir(), t.TempDir()
	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	write(filepath.Join(repo, ".harness", "commands", "fixup.md"), "Fix up the history.")
	write(filepath.Join(dataDir, "commands", "legacy.md"), "Still here.")

	cfg := &config.Config{Options: &config.Options{DataDirectory: dataDir}}
	var names []string
	for _, cmd := range LoadCustomCommands(cfg, repo) {
		names = append(names, cmd.Name)
	}
	require.Contains(t, names, "project:fixup")
	require.Contains(t, names, "project:legacy")
}
