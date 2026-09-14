package completions

import (
	"charm.land/bubbles/v2/key"

	"github.com/stubbedev/harness/internal/ui/keys"
)

// KeyMap defines the key bindings for the completions component. The
// bindings live in [keys] with every other key the TUI reads, so they
// follow the user's options.tui.keybinds.
type KeyMap = keys.CompletionsKeys

// DefaultKeyMap returns the completions bindings in force.
func DefaultKeyMap() KeyMap {
	return keys.Active().Completions
}

// KeyBindings returns the bindings worth showing in help.
func KeyBindings(k KeyMap) []key.Binding {
	return []key.Binding{
		k.Down,
		k.Up,
		k.Select,
		k.Cancel,
	}
}

// FullHelp returns the full help for the key bindings.
func FullHelp(k KeyMap) [][]key.Binding {
	m := [][]key.Binding{}
	slice := KeyBindings(k)
	for i := 0; i < len(slice); i += 4 {
		end := min(i+4, len(slice))
		m = append(m, slice[i:end])
	}
	return m
}

// ShortHelp returns the short help for the key bindings.
func ShortHelp(k KeyMap) []key.Binding {
	return []key.Binding{k.Up, k.Down}
}
