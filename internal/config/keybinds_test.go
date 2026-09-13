package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// loadYAML runs a YAML document through the same YAML-to-JSON boundary the
// loader uses, then into a Config.
func loadYAML(t *testing.T, doc string) *Config {
	t.Helper()
	jsonBytes, err := decodeConfig([]byte(doc))
	require.NoError(t, err)
	cfg, err := loadFromBytes([][]byte{jsonBytes})
	require.NoError(t, err)
	return cfg
}

func TestTUIOptionsKeybinds(t *testing.T) {
	t.Parallel()

	t.Run("accepts a single key or a list of keys", func(t *testing.T) {
		t.Parallel()

		cfg := loadYAML(t, `
options:
  tui:
    keybinds:
      quit: ctrl+q
      editor.newline: [shift+enter, ctrl+j]
      chat.copy:
        - c
        - y
`)
		require.NotNil(t, cfg.Options.TUI)
		require.Equal(t, map[string]KeybindList{
			"quit":           {"ctrl+q"},
			"editor.newline": {"shift+enter", "ctrl+j"},
			"chat.copy":      {"c", "y"},
		}, cfg.Options.TUI.Keybinds)
	})

	t.Run("rejects non-string keybind values", func(t *testing.T) {
		t.Parallel()

		jsonBytes, err := decodeConfig([]byte("options:\n  tui:\n    keybinds:\n      quit: 3\n"))
		require.NoError(t, err)
		_, err = loadFromBytes([][]byte{jsonBytes})
		require.Error(t, err)
	})

	t.Run("KeybindOverrides is nil-safe and converts", func(t *testing.T) {
		t.Parallel()

		var nilTUI *TUIOptions
		require.Nil(t, nilTUI.KeybindOverrides())
		require.Nil(t, (&TUIOptions{}).KeybindOverrides())

		tui := &TUIOptions{Keybinds: map[string]KeybindList{
			"quit": {"ctrl+q"},
		}}
		require.Equal(t, map[string][]string{"quit": {"ctrl+q"}}, tui.KeybindOverrides())
	})
}

func TestConfigCloneForWriteKeybinds(t *testing.T) {
	t.Parallel()

	cfg := loadYAML(t, `
options:
  tui:
    keybinds:
      quit: ctrl+q
      chat.copy: [c, y]
`)

	clone := cfg.cloneForWrite()

	// The clone's keybinds are independent: mutating them must not leak
	// into the published config.
	clone.Options.TUI.Keybinds["quit"] = KeybindList{"ctrl+w"}
	clone.Options.TUI.Keybinds["chat.copy"][0] = "z"

	require.Equal(t, KeybindList{"ctrl+q"}, cfg.Options.TUI.Keybinds["quit"])
	require.Equal(t, KeybindList{"c", "y"}, cfg.Options.TUI.Keybinds["chat.copy"])
}
