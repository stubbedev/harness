package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsesMessagesAPI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider string
		model    string
		expected bool
	}{
		{"opencode", "qwen3.7-max", true},
		{"opencode", "qwen3.5-plus", true},
		{"opencode", "qwen3.6-plus", true},
		{"opencode", "qwen3.7-plus", true},
		{"opencode", "claude-opus-4-5", true},
		{"opencode", "claude-sonnet-5", true},
		{"opencode", "muse-spark-1.3-contributor-free", false},
		{"opencode", "gpt-5.6-luna", false},
		{"opencode", "grok-4.5", false},
		{"opencode", "minimax-m3", false},
		{"opencode", "kimi-k3", false},
		{"opencode", "big-pickle", false},
		{"opencode-go", "minimax-m2.7", true},
		{"opencode-go", "minimax-m3", true},
		{"opencode-go", "qwen3.7-max", true},
		{"opencode-go", "qwen3.7-plus", true},
		{"opencode-go", "qwen3.6-plus", true},
		{"opencode-go", "qwen3.8-flash", true},
		{"opencode-go", "qwen3.8-max", true},
		{"opencode-go", "muse-spark-1.3-contributor", false},
		{"opencode-go", "gpt-5.6-luna", false},
		{"opencode-go", "glm-5.3", false},
		{"opencode-go", "kimi-k3", false},
		{"opencode-go", "longcat-2.0", false},
		{"opencode-go", "ox-alpha-free", false},
		{"opencode-go", "minimax", false},
		{"other", "claude-opus-4-5", false},
		{"other", "qwen3.7-max", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.expected, UsesMessagesAPI(InferenceProvider(tt.provider), tt.model), "%s/%s", tt.provider, tt.model)
	}
}

func TestOpenCodeResponsesAPIRouter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model    string
		expected bool
	}{
		{"muse-spark-1.3-contributor-free", true},
		{"muse-spark-1.2", true},
		{"grok-4.5", true},
		{"grok-4.6", true},
		{"grok-build-0.1", true},
		{"gpt-5.6-luna", true},
		{"gpt-5.5", true},
		{"gpt-5.3-codex", true},
		{"minimax-m3", false},
		{"qwen3.7-max", false},
		{"kimi-k3", false},
		{"glm-5.3", false},
		{"big-pickle", false},
		{"ox-alpha-free", false},
		{"hy3", false},
		{"longcat-2.0", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.expected, ResponsesAPIRouter(InferenceProviderOpenCodeZen)(tt.model), tt.model)
	}
}

func TestCopilotResponsesModels(t *testing.T) {
	t.Parallel()

	for _, modelID := range []string{"gpt-6-astra", "grok-4.5", "grok-4.6"} {
		require.True(t, ResponsesAPIRouter(InferenceProviderCopilot)(modelID), modelID)
	}
	require.False(t, ResponsesAPIRouter(InferenceProviderCopilot)("gpt-4.1"))
	require.Nil(t, ResponsesAPIRouter(InferenceProviderOpenAI))
}

// TestSeedAgreesWithKnownProviders catches a seed.json.gz generated before
// the known-provider table last changed: every fact the table dictates
// (protocol, headers) must already be in the bundled snapshot. Regenerate
// with go generate ./internal/catalog when it fails.
func TestSeedAgreesWithKnownProviders(t *testing.T) {
	t.Parallel()

	for _, p := range Seed() {
		known, ok := knownProviders[p.ID]
		if !ok {
			continue
		}
		if known.Type != "" {
			require.Equal(t, known.Type, p.Type, "seed type of %s", p.ID)
		}
		require.Equal(t, knownHeaders(p.ID), p.DefaultHeaders, "seed headers of %s", p.ID)
	}
}
