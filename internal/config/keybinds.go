package config

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
)

// KeybindList is the list of keys bound to one TUI action. At the config
// boundary it accepts either a single key string or a list, so both
// `quit: ctrl+q` and `editor.newline: [shift+enter, ctrl+j]` are valid.
type KeybindList []string

// UnmarshalJSON accepts a JSON string or an array of strings.
func (k *KeybindList) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*k = KeybindList{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("keybind must be a key string or a list of key strings: %w", err)
	}
	*k = many
	return nil
}

// JSONSchema describes the string-or-list shape for the generated config
// schema, so editors accept both spellings.
func (KeybindList) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		OneOf: []*jsonschema.Schema{
			{Type: "string"},
			{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
		},
	}
}
