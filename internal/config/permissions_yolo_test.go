package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// TestPermissionsYoloDefault verifies the fork's yolo-by-default
// behavior: an unset permissions block skips prompts, an explicit
// permissions.yolo false restores them, and the crushrc builtin maps
// onto the same field.
func TestPermissionsYoloDefault(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "crush"))

	workDir := t.TempDir()
	dataDir := t.TempDir()

	t.Run("yolo on by default", func(t *testing.T) {
		store, err := config.Load(workDir, dataDir, false)
		require.NoError(t, err)
		require.True(t, store.Config().Permissions.YoloEnabled())
	})

	t.Run("permissions yolo false restores prompts", func(t *testing.T) {
		require.NoError(t, os.WriteFile(
			filepath.Join(workDir, "crushrc"),
			[]byte("permissions yolo false\n"),
			0o644,
		))

		store, err := config.Load(workDir, dataDir, false)
		require.NoError(t, err)
		require.NotNil(t, store.Config().Permissions)
		require.NotNil(t, store.Config().Permissions.Yolo)
		require.False(t, store.Config().Permissions.YoloEnabled())
	})
}
