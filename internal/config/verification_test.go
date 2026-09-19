package config

import (
	"encoding/json"
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestVerificationConfiguration(t *testing.T) {
	t.Parallel()
	data, err := decodeConfig([]byte(`verification:
  require_on_completion: true
  max_repair_attempts: 2
  rules:
    - name: unit
      paths: ["**/*.go", go.mod]
      inputs: [go.sum]
      command: [go, test, ./internal/verification]
      timeout_seconds: 30
      max_output_bytes: 2048
`))
	require.NoError(t, err)
	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NoError(t, cfg.Verification.Validate())
	require.True(t, cfg.Verification.RequireOnCompletion)
	require.Equal(t, 2, cfg.Verification.MaxRepairAttempts)
	require.Len(t, cfg.Verification.Rules, 1)
	require.Equal(t, []string{"go", "test", "./internal/verification"}, cfg.Verification.Rules[0].Command)
	require.Equal(t, 30, cfg.Verification.Rules[0].TimeoutSeconds)
	require.Equal(t, 2048, cfg.Verification.Rules[0].MaxOutputBytes)
	require.Equal(t, cfg.Verification, cfg.cloneForWrite().Verification)
	encoded, err := json.Marshal(cfg)
	require.NoError(t, err)
	yaml, err := encodeConfig(encoded)
	require.NoError(t, err)
	data, err = decodeConfig(yaml)
	require.NoError(t, err)
	var restored Config
	require.NoError(t, json.Unmarshal(data, &restored))
	require.Equal(t, cfg.Verification, restored.Verification)
}

func TestVerificationSchema(t *testing.T) {
	t.Parallel()
	schema := new(jsonschema.Reflector).Reflect(&Config{})
	cfg := schema.Definitions["Config"]
	require.NotNil(t, cfg)
	property, ok := cfg.Properties.Get("verification")
	require.True(t, ok)
	require.Equal(t, "#/$defs/VerificationConfig", property.Ref)
	verification := schema.Definitions["VerificationConfig"]
	require.NotNil(t, verification)
	gate, ok := verification.Properties.Get("require_on_completion")
	require.True(t, ok)
	require.Equal(t, "boolean", gate.Type)
	rule := schema.Definitions["Rule"]
	require.NotNil(t, rule)
	require.ElementsMatch(t, []string{"name", "paths", "command"}, rule.Required)
	command, ok := rule.Properties.Get("command")
	require.True(t, ok)
	require.Equal(t, "array", command.Type)
	require.Equal(t, "string", command.Items.Type)
}
