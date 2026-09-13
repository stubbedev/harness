package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
	"github.com/spf13/cobra"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/discover"
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
