package catalog

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

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
		if known.RoutesPerModel {
			require.True(t, slices.ContainsFunc(p.Models, func(m Model) bool { return m.Wire != "" }),
				"seed of %s carries no per-model routes", p.ID)
		}
	}
}

func openCodeGoFixture() modelsDev {
	const base = "https://opencode.ai/inference/go"
	model := func(id string, route modelsDevModelRoute) modelsDevModel {
		return modelsDevModel{ID: id, Name: id, ToolCall: true, Limit: modelsDevLimit{Context: 1000, Output: 100}, Provider: route}
	}
	return modelsDev{
		"opencode-go": {
			ID:  "opencode-go",
			NPM: "@ai-sdk/openai-compatible",
			API: base + "/openai/v1",
			Env: []string{"OPENCODE_API_KEY"},
			Models: map[string]modelsDevModel{
				"kimi-k3":                    model("kimi-k3", modelsDevModelRoute{API: base + "/openai/v1"}),
				"gpt-6-luna":                 model("gpt-6-luna", modelsDevModelRoute{NPM: "@ai-sdk/openai", API: base + "/openai/v1"}),
				"minimax-m3":                 model("minimax-m3", modelsDevModelRoute{NPM: "@ai-sdk/anthropic", API: base + "/anthropic/v1"}),
				"claude-haiku-5-5":           model("claude-haiku-5-5", modelsDevModelRoute{NPM: "@ai-sdk/anthropic", API: base + "/anthropic/v1"}),
				"gemini-4-argon-contributor": model("gemini-4-argon-contributor", modelsDevModelRoute{NPM: "@ai-sdk/google", API: base + "/google/v1beta"}),
				"no-route":                   model("no-route", modelsDevModelRoute{}),
			},
		},
		"plain-gateway": {
			ID:  "plain-gateway",
			NPM: "@ai-sdk/openai-compatible",
			API: "https://gw.example/v1",
			Models: map[string]modelsDevModel{
				"claude": model("claude", modelsDevModelRoute{NPM: "@ai-sdk/anthropic", API: "https://gw.example/anthropic/v1"}),
			},
		},
	}
}

func TestTranslateModelsDevRoutesOpenCodeModelsPerModel(t *testing.T) {
	t.Parallel()

	p, ok := findProvider(translateModelsDev(openCodeGoFixture()), InferenceProviderOpenCodeGo)
	require.True(t, ok)
	require.Equal(t, TypeOpenAICompat, p.Type)

	routes := make(map[string][2]string, len(p.Models))
	for _, m := range p.Models {
		routes[m.ID] = [2]string{string(m.Wire), m.APIEndpoint}
	}
	require.Equal(t, map[string][2]string{
		"kimi-k3":                    {"", ""},
		"no-route":                   {"", ""},
		"gpt-6-luna":                 {"openai", ""},
		"minimax-m3":                 {"anthropic", "https://opencode.ai/inference/go/anthropic/v1"},
		"claude-haiku-5-5":           {"anthropic", "https://opencode.ai/inference/go/anthropic/v1"},
		"gemini-4-argon-contributor": {"google", "https://opencode.ai/inference/go/google/v1beta"},
	}, routes)
}

func TestTranslateModelsDevIgnoresModelRoutesOfOtherProviders(t *testing.T) {
	t.Parallel()

	p, ok := findProvider(translateModelsDev(openCodeGoFixture()), "plain-gateway")
	require.True(t, ok)
	require.Len(t, p.Models, 1)
	require.Empty(t, p.Models[0].Wire)
	require.Empty(t, p.Models[0].APIEndpoint)
}
