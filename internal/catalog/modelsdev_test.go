package catalog

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func fixtureModelsDev() modelsDev {
	return modelsDev{
		"anthropic": {
			ID:   "anthropic",
			Name: "Anthropic",
			NPM:  "@ai-sdk/anthropic",
			Env:  []string{"ANTHROPIC_API_KEY"},
			Models: map[string]modelsDevModel{
				"claude-x": {
					ID: "claude-x", Name: "Claude X", Reasoning: true,
					Limit:      modelsDevLimit{Context: 200000, Output: 8192},
					Cost:       modelsDevCost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
					Modalities: modelsDevModalities{Input: []string{"text", "image"}, Output: []string{"text"}},
				},
				"claude-old": {
					ID: "claude-old", Name: "Claude Old", Status: "deprecated",
					Limit: modelsDevLimit{Context: 200000, Output: 8192},
				},
			},
		},
		"deepinfra": {
			ID:   "deepinfra",
			Name: "DeepInfra",
			NPM:  "@ai-sdk/deepinfra",
			API:  "https://api.deepinfra.com/v1/openai",
			Env:  []string{"DEEPINFRA_API_KEY"},
			Models: map[string]modelsDevModel{
				"meta/llama": {ID: "meta/llama", Name: "Llama", Limit: modelsDevLimit{Context: 32000, Output: 4096}},
			},
		},
		"zai": {
			ID:   "zai",
			Name: "Z.AI",
			NPM:  "@ai-sdk/openai-compatible",
			API:  "https://api.z.ai/api/paas/v4",
			Env:  []string{"ZHIPU_API_KEY"},
			Models: map[string]modelsDevModel{
				"glm-x": {ID: "glm-x", Name: "GLM X", Limit: modelsDevLimit{Context: 128000, Output: 4096}},
			},
		},
		"sdk-only": {
			ID:   "sdk-only",
			Name: "SDK Only",
			NPM:  "@ai-sdk/mistral",
			Models: map[string]modelsDevModel{
				"m": {ID: "m", Name: "M"},
			},
		},
		"empty": {
			ID:     "empty",
			Name:   "Empty",
			NPM:    "@ai-sdk/openai-compatible",
			API:    "https://api.empty.dev/v1",
			Models: map[string]modelsDevModel{},
		},
	}
}

func findProvider(providers []Provider, id InferenceProvider) (Provider, bool) {
	for _, p := range providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

func TestTranslateModelsDevNativeProtocol(t *testing.T) {
	t.Parallel()

	providers := translateModelsDev(fixtureModelsDev())

	anthropic, ok := findProvider(providers, "anthropic")
	require.True(t, ok)
	require.Equal(t, TypeAnthropic, anthropic.Type)
	require.Equal(t, "$ANTHROPIC_API_KEY", anthropic.APIKey)
	// Deprecated models are dropped from the entry.
	require.Len(t, anthropic.Models, 1)
	require.Equal(t, "claude-x", anthropic.Models[0].ID)
	require.True(t, anthropic.Models[0].SupportsImages)
	require.True(t, anthropic.Models[0].CanReason)
	// Native-protocol entries carry no endpoint; the SDK default
	// applies and users can override via config.
	require.Empty(t, anthropic.APIEndpoint)
}

func TestTranslateModelsDevAdoptsOpenAICompatProviders(t *testing.T) {
	t.Parallel()

	providers := translateModelsDev(fixtureModelsDev())

	deepinfra, ok := findProvider(providers, "deepinfra")
	require.True(t, ok, "an entry with a base URL is adopted")
	require.Equal(t, TypeOpenAICompat, deepinfra.Type, "unmapped npm falls back to openai-compat")
	require.Equal(t, "https://api.deepinfra.com/v1/openai", deepinfra.APIEndpoint)
	require.Equal(t, "$DEEPINFRA_API_KEY", deepinfra.APIKey)
	require.Len(t, deepinfra.Models, 1)
	require.Equal(t, int64(32000), deepinfra.Models[0].ContextWindow)

	zai, ok := findProvider(providers, "zai")
	require.True(t, ok)
	require.Equal(t, "https://api.z.ai/api/paas/v4", zai.APIEndpoint)
	require.Equal(t, "$ZHIPU_API_KEY", zai.APIKey)
}

func TestTranslateModelsDevSkipsUnusableProviders(t *testing.T) {
	t.Parallel()

	providers := translateModelsDev(fixtureModelsDev())

	_, ok := findProvider(providers, "sdk-only")
	require.False(t, ok, "an entry without a mappable protocol or base URL is skipped")

	_, ok = findProvider(providers, "empty")
	require.False(t, ok, "an entry without models is skipped")
}

func TestTranslateModelsDevNativeNpmMapping(t *testing.T) {
	t.Parallel()

	md := modelsDev{
		"some-bedrock-gateway": {
			ID:   "some-bedrock-gateway",
			Name: "Bedrock Gateway",
			NPM:  "@ai-sdk/amazon-bedrock",
			Models: map[string]modelsDevModel{
				"b": {ID: "b", Name: "B", Limit: modelsDevLimit{Context: 1000, Output: 100}},
			},
		},
	}

	providers := translateModelsDev(md)
	p, ok := findProvider(providers, "some-bedrock-gateway")
	require.True(t, ok, "a native-protocol npm mapping adopts without a base URL")
	require.Equal(t, TypeBedrock, p.Type)
}

func TestTranslateModelsDevDeterministicOrder(t *testing.T) {
	t.Parallel()

	a := translateModelsDev(fixtureModelsDev())
	b := translateModelsDev(fixtureModelsDev())

	require.Equal(t, a, b, "the catalog sorts deterministically")
	require.True(t, slices.IsSortedFunc(a, func(x, y Provider) int {
		return int(x.ID[0]) - int(y.ID[0])
	}) || len(a) <= 1)
}

func TestNpmToType(t *testing.T) {
	t.Parallel()

	for npm, want := range map[string]Type{
		"@ai-sdk/anthropic":               TypeAnthropic,
		"@ai-sdk/openai":                  TypeOpenAI,
		"@ai-sdk/google":                  TypeGoogle,
		"@ai-sdk/google-vertex":           TypeVertexAI,
		"@ai-sdk/google-vertex/anthropic": TypeVertexAI,
		"@ai-sdk/azure":                   TypeAzure,
		"@ai-sdk/amazon-bedrock":          TypeBedrock,
		"@ai-sdk/vercel":                  TypeVercel,
		"@openrouter/ai-sdk-provider":     TypeOpenRouter,
		"@ai-sdk/deepinfra":               "",
		"":                                "",
	} {
		got, native := npmToType(npm)
		if native {
			require.Equal(t, want, got, "npm %q", npm)
		} else {
			require.Equal(t, Type(""), got, "npm %q should not map", npm)
		}
	}
}
