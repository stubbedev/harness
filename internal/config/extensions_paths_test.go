package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGlobalExtensionsDirs_EnvOverride verifies that
// HARNESS_EXTENSIONS_DIR, when set to a non-empty value, replaces the
// default global extension directories entirely, mirroring the
// HARNESS_SKILLS_DIR override on GlobalSkillsDirs.
func TestGlobalExtensionsDirs_EnvOverride(t *testing.T) {
	override := t.TempDir()
	t.Setenv("HARNESS_EXTENSIONS_DIR", override)

	require.Equal(t, []string{override}, GlobalExtensionsDirs(),
		"HARNESS_EXTENSIONS_DIR must fully replace the default extension dirs")

	t.Setenv("HARNESS_EXTENSIONS_DIR", "")

	dirs := GlobalExtensionsDirs()
	found := false
	for _, dir := range dirs {
		if strings.HasSuffix(dir, filepath.Join("harness", "extensions")) {
			found = true
			break
		}
	}
	require.True(t, found,
		"expected a default path ending in harness/extensions when the override is empty; got %v", dirs)
}

// TestProjectExtensionsDir_OrdersWorkingDirLast checks the ordering the
// discovery relies on: the working directory comes last so a
// working-directory extension shadows a monorepo-root one of the same
// name.
func TestProjectExtensionsDir_OrdersWorkingDirLast(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	dirs := ProjectExtensionsDir(workingDir)

	require.NotEmpty(t, dirs)
	require.Equal(t, filepath.Join(workingDir, ".harness/extensions"), dirs[0])
	for _, dir := range dirs {
		require.True(t, strings.HasPrefix(dir, workingDir),
			"expected every path under the working directory outside a repo; got %q", dir)
	}
}

func TestOptions_ExtensionsPaths_JSONRoundtrip(t *testing.T) {
	t.Parallel()

	original := Options{
		ExtensionsPaths:    []string{"/a", "/b"},
		DisabledExtensions: []string{"noisy"},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored Options
	require.NoError(t, json.Unmarshal(data, &restored))

	require.Equal(t, original.ExtensionsPaths, restored.ExtensionsPaths)
	require.Equal(t, original.DisabledExtensions, restored.DisabledExtensions)
}
