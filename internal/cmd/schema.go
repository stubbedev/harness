package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/invopop/jsonschema"
	"github.com/spf13/cobra"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/discover"
	"github.com/stubbedev/harness/internal/ui/keys"
)

var schemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Generate JSON schema for configuration",
	Long: "Generate the JSON Schema describing harness.yaml. YAML editors and\n" +
		"language servers consume JSON Schema directly, so this is what\n" +
		"drives completion and validation for the config file.",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		reflector := new(jsonschema.Reflector)
		schema := reflector.Reflect(&config.Config{})
		setProviderTypeEnum(schema)
		setKeybindActionEnum(schema)
		setHookEventNames(schema)
		bts, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal schema: %w", err)
		}
		fmt.Println(string(bts))
		return nil
	},
}

// setProviderTypeEnum overwrites the provider `type` enum with the live set
// of accepted values rather than a hand-maintained struct tag. The values
// must match exactly what load.go validates against: the catalog provider
// types, the Charm Hyper type, and any locally-discovered providers that
// self-register an enricher (e.g. ollama, omlx). Sourcing the enum here keeps
// the published schema from drifting as provider types are added or renamed.
func setProviderTypeEnum(schema *jsonschema.Schema) {
	def, ok := schema.Definitions["ProviderConfig"]
	if !ok || def.Properties == nil {
		return
	}
	typeProp, ok := def.Properties.Get("type")
	if !ok {
		return
	}

	var types []string
	for _, t := range catalog.KnownProviderTypes() {
		types = append(types, string(t))
	}
	types = append(types, discover.RegisteredProviderTypes()...)

	typeProp.Enum = make([]any, len(types))
	for i, t := range types {
		typeProp.Enum[i] = t
	}
}

// setKeybindActionEnum restricts options.tui.keybinds to the action names
// the TUI actually binds, sourced from the keymap itself. An editor then
// completes the names and flags a typo, and an action added to the keymap
// shows up here without a second list to maintain.
func setKeybindActionEnum(schema *jsonschema.Schema) {
	def, ok := schema.Definitions["TUIOptions"]
	if !ok || def.Properties == nil {
		return
	}
	keybinds, ok := def.Properties.Get("keybinds")
	if !ok {
		return
	}

	actions := keys.ActionNames()
	enum := make([]any, len(actions))
	for i, a := range actions {
		enum[i] = a
	}
	keybinds.PropertyNames = &jsonschema.Schema{Enum: enum}
}

// setHookEventNames restricts the keys of `hooks` to the events this build
// fires, sourced from config.HookEvents. Config accepts an event name in
// any case and with or without underscores between its words
// (PreToolUse, pretooluse, pre_tool_use), and JSON Schema patterns have no
// case-insensitive flag, so the pattern spells each letter as a class.
func setHookEventNames(schema *jsonschema.Schema) {
	def, ok := schema.Definitions["Config"]
	if !ok || def.Properties == nil {
		return
	}
	hooks, ok := def.Properties.Get("hooks")
	if !ok {
		return
	}
	events := config.HookEvents()
	alternatives := make([]string, len(events))
	for i, event := range events {
		alternatives[i] = hookEventPattern(event)
	}
	hooks.PropertyNames = &jsonschema.Schema{
		Pattern:     "^(" + strings.Join(alternatives, "|") + ")$",
		Description: "Hook event: " + strings.Join(events, ", "),
	}
}

// hookEventPattern matches event the way config.CanonicalHookEvent does:
// any case, an optional underscore before each capitalized word.
func hookEventPattern(event string) string {
	var b strings.Builder
	for i, r := range event {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteString("_?")
		}
		lower, upper := unicode.ToLower(r), unicode.ToUpper(r)
		b.WriteString("[" + string(upper) + string(lower) + "]")
	}
	return b.String()
}
