package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

// TestPermissionsYoloDefault verifies the fork's yolo-by-default
// behavior: an unset permissions block skips prompts, an explicit
// permissions.yolo false restores them.
func TestPermissionsYoloDefault(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("HARNESS_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "harness"))
	t.Setenv("HARNESS_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "harness"))

	workDir := t.TempDir()
	dataDir := t.TempDir()

	t.Run("yolo on by default", func(t *testing.T) {
		store, err := config.Load(workDir, dataDir, false)
		require.NoError(t, err)
		require.True(t, store.Config().Permissions.YoloEnabled())
	})

	t.Run("permissions yolo false restores prompts", func(t *testing.T) {
		require.NoError(t, os.WriteFile(
			filepath.Join(workDir, "harness.yaml"),
			[]byte("permissions:\n  yolo: false\n"),
			0o644,
		))

		store, err := config.Load(workDir, dataDir, false)
		require.NoError(t, err)
		require.NotNil(t, store.Config().Permissions)
		require.NotNil(t, store.Config().Permissions.Yolo)
		require.False(t, store.Config().Permissions.YoloEnabled())
	})
}
