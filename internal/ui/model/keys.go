package model

import "github.com/stubbedev/harness/internal/ui/keys"

// KeyMap is the TUI keymap. It lives in [keys] so every package that binds
// a key — the model, the dialogs, the completions popup — reads its
// bindings from the same struct and the same user overrides.
type KeyMap = keys.KeyMap

// DefaultKeyMap returns the built-in bindings.
func DefaultKeyMap() KeyMap { return keys.DefaultKeyMap() }
