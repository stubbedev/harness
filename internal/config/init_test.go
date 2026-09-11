package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

// TestProjectNeedsInitialization_InitPromptOption verifies that setting
// "init_prompt: false" in the project config suppresses the project
// initialization prompt even when the project would otherwise need it
// (no context file, no init flag, non-empty directory).
func TestProjectNeedsInitialization_InitPromptOption(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("HARNESS_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "harness"))
	t.Setenv("HARNESS_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "harness"))

	workDir := t.TempDir()
	dataDir := t.TempDir()
	// The working directory must have at least one visible file so it is
	// not treated as empty (empty projects are never initialized).
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\n"), 0o644))

	t.Run("needs initialization by default", func(t *testing.T) {
		store, err := config.Load(workDir, dataDir, false)
		require.NoError(t, err)

		needs, err := config.ProjectNeedsInitialization(store)
		require.NoError(t, err)
		require.True(t, needs)
	})

	t.Run("init-prompt false disables it", func(t *testing.T) {
		require.NoError(t, os.WriteFile(
			filepath.Join(workDir, "harness.yaml"),
			[]byte("options:\n  init_prompt: false\n"),
			0o644,
		))

		store, err := config.Load(workDir, dataDir, false)
		require.NoError(t, err)
		require.NotNil(t, store.Config().Options.InitPrompt)
		require.False(t, *store.Config().Options.InitPrompt)

		needs, err := config.ProjectNeedsInitialization(store)
		require.NoError(t, err)
		require.False(t, needs)
	})
}
