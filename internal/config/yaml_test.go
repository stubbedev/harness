package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"
)

// TestDecodeConfigEmptyDocuments verifies the shapes a config file can take
// while carrying nothing: absent content, whitespace, comments, and an
// explicit null all mean "nothing to merge" rather than an error.
func TestDecodeConfigEmptyDocuments(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "   \n", "# just a comment\n", "null\n", "---\n"} {
		out, err := decodeConfig([]byte(in))
		require.NoError(t, err, "%q", in)
		require.Empty(t, out, "%q", in)
	}
}

// TestDecodeConfigRejectsBadYAML verifies a malformed file fails the load
// instead of silently contributing nothing.
func TestDecodeConfigRejectsBadYAML(t *testing.T) {
	t.Parallel()

	_, err := decodeConfig([]byte("options:\n  debug: true\n   theme: nope\n"))
	require.Error(t, err)
}

// TestEncodeConfigRoundTrip verifies JSON written back out is YAML a person
// would be willing to read, and that it parses back to the same data.
func TestEncodeConfigRoundTrip(t *testing.T) {
	t.Parallel()

	in := []byte(`{"options":{"debug":true,"tui":{"theme":"gruvbox-dark"}},"models":{"large":{"provider":"anthropic","model":"claude"}}}`)
	out, err := encodeConfig(in)
	require.NoError(t, err)
	require.Contains(t, string(out), "theme: gruvbox-dark")

	back, err := decodeConfig(out)
	require.NoError(t, err)
	require.JSONEq(t, string(in), string(back))
}

// TestEncodeConfigEmpty verifies truncating a config leaves an empty file
// rather than the literal "null".
func TestEncodeConfigEmpty(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "null"} {
		out, err := encodeConfig([]byte(in))
		require.NoError(t, err)
		require.Empty(t, out, "%q", in)
	}
}

// TestLoadMergesYAMLLayers is the end-to-end check on the file layout: the
// user config, the machine state file, and a project config all load, and the
// project wins where they overlap.
func TestLoadMergesYAMLLayers(t *testing.T) {
	isolated := t.TempDir()
	configHome := filepath.Join(isolated, ".config", "harness")
	dataHome := filepath.Join(isolated, ".local", "share", "harness")
	require.NoError(t, os.MkdirAll(configHome, 0o755))
	require.NoError(t, os.MkdirAll(dataHome, 0o755))

	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("HARNESS_GLOBAL_CONFIG", configHome)
	t.Setenv("HARNESS_GLOBAL_DATA", dataHome)

	require.NoError(t, os.WriteFile(filepath.Join(configHome, userConfigFile), []byte(
		"options:\n  debug: true\n  initialize_as: USER.md\n  tui:\n    theme: charmtone\n",
	), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dataHome, stateConfigFile), []byte(
		"options:\n  tui:\n    theme: gruvbox-dark\n",
	), 0o644))

	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "harness.yaml"), []byte(
		"options:\n  initialize_as: PROJECT.md\n",
	), 0o644))

	store, err := Load(workDir, t.TempDir(), false)
	require.NoError(t, err)

	cfg := store.Config()
	require.True(t, cfg.Options.Debug, "user config still applies")
	require.Equal(t, "gruvbox-dark", cfg.Options.TUI.Theme, "state file overrides the user config")
	require.Equal(t, "PROJECT.md", cfg.Options.InitializeAs, "the project config wins")
}

// TestSetConfigFieldWritesYAML verifies the write path produces YAML on disk
// and that a later load reads it back.
func TestSetConfigFieldWritesYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statePath := filepath.Join(dir, stateConfigFile)
	store := &ConfigStore{config: &Config{}, globalDataPath: statePath}

	require.NoError(t, store.SetConfigField(ScopeGlobal, "options.tui.theme", "gruvbox-dark"))

	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)

	var parsed struct {
		Options struct {
			TUI struct {
				Theme string `yaml:"theme"`
			} `yaml:"tui"`
		} `yaml:"options"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &parsed))
	require.Equal(t, "gruvbox-dark", parsed.Options.TUI.Theme)

	// And the value is visible to the normal read path.
	require.True(t, store.HasConfigField(ScopeGlobal, "options.tui.theme"))
}

// TestSetConfigFieldPreservesHandWrittenValues verifies a write merges into
// the existing file instead of replacing it.
func TestSetConfigFieldPreservesHandWrittenValues(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statePath := filepath.Join(dir, stateConfigFile)
	require.NoError(t, os.WriteFile(statePath, []byte("options:\n  debug: true\n"), 0o600))

	store := &ConfigStore{config: &Config{}, globalDataPath: statePath}
	require.NoError(t, store.SetConfigField(ScopeGlobal, "options.tui.theme", "charmtone"))

	data, err := readConfigJSON(statePath)
	require.NoError(t, err)
	require.JSONEq(t, `{"options":{"debug":true,"tui":{"theme":"charmtone"}}}`, string(data))
}
