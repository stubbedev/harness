package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnumOptionsMatchSchema holds the schema's enum for every closed-set
// option to the list the loader validates against, so the editor and the
// loader never disagree on what a value may be.
func TestEnumOptionsMatchSchema(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../../schema.json")
	require.NoError(t, err)
	var schema struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(data, &schema))

	cfg := &Config{}
	cfg.NormalizeOptions()
	for _, opt := range cfg.enumOptions() {
		def := "Options"
		if strings.HasPrefix(opt.path, "options.tui.") {
			def = "TUIOptions"
		}
		prop := opt.path[strings.LastIndexByte(opt.path, '.')+1:]
		require.Equal(t, opt.allowed, schema.Defs[def].Properties[prop].Enum, opt.path)
	}
}

func TestUnknownEnumValueFallsBackToDefault(t *testing.T) {
	t.Parallel()

	cfg := &Config{Options: &Options{TUI: &TUIOptions{DialogPlacement: "Top", Scrollbar: "sometimes"}, Notifications: NotificationsBell}}
	cfg.NormalizeOptions()
	require.Empty(t, cfg.Options.TUI.DialogPlacement, "an unknown placement reads as the default")
	require.Equal(t, ScrollbarDefault, cfg.Options.TUI.Scrollbar)
	require.Equal(t, NotificationsBell, cfg.Options.Notifications, "a known value is kept")
}
