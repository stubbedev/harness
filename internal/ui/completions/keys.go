package completions

import (
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
