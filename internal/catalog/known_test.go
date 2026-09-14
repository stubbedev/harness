package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestKnownProvidersFillWhatModelsDevOmits covers the entries models.dev
// publishes without a base URL: the catalog is useless for them unless
// the known-provider table supplies one.
func TestKnownProvidersFillWhatModelsDevOmits(t *testing.T) {
	t.Parallel()

	md := modelsDev{
		// A vendor SDK entry with no base URL of its own.
		"cerebras": {
			ID: "cerebras", Name: "Cerebras", NPM: "@ai-sdk/cerebras",
			Env: []string{"CEREBRAS_API_KEY"},
			Models: map[string]modelsDevModel{
				"c": {ID: "c", ToolCall: true, Limit: modelsDevLimit{Context: 128000, Output: 8192}},
			},
		},
		// A gateway that attributes traffic by referer.
		"openrouter": {
			ID: "openrouter", Name: "OpenRouter", NPM: "@openrouter/ai-sdk-provider",
			API: "https://openrouter.ai/api/v1", Env: []string{"OPENROUTER_API_KEY"},
			Models: map[string]modelsDevModel{
				"o": {ID: "o", ToolCall: true, Limit: modelsDevLimit{Context: 128000, Output: 8192}},
			},
		},
		// An unknown vendor SDK with no base URL stays unusable.
		"mystery": {
			ID: "mystery", Name: "Mystery", NPM: "@mystery/ai-sdk-provider",
			Models: map[string]modelsDevModel{
				"m": {ID: "m", ToolCall: true, Limit: modelsDevLimit{Context: 8192, Output: 1024}},
			},
		},
	}

	providers := translateModelsDev(md)

	cerebras, ok := findProvider(providers, InferenceProviderCerebras)
	require.True(t, ok, "a known provider is adopted even without a models.dev base URL")
	require.Equal(t, "https://api.cerebras.ai/v1", cerebras.APIEndpoint)
	require.Equal(t, TypeOpenAICompat, cerebras.Type)
	require.Equal(t, "harness", cerebras.DefaultHeaders["X-Cerebras-3rd-Party-Integration"])

	openrouter, ok := findProvider(providers, InferenceProviderOpenRouter)
	require.True(t, ok)
	require.Equal(t, "https://openrouter.ai/api/v1", openrouter.APIEndpoint, "models.dev wins over the table for the endpoint")
	require.Equal(t, harnessReferer, openrouter.DefaultHeaders["HTTP-Referer"])
	require.Equal(t, harnessTitle, openrouter.DefaultHeaders["X-Title"])

	_, ok = findProvider(providers, "mystery")
	require.False(t, ok, "an unknown SDK with no base URL cannot be talked to")
}

// TestKnownHeadersAreCopied guards the shared attribution map against a
// caller mutating every provider's headers at once.
func TestKnownHeadersAreCopied(t *testing.T) {
	t.Parallel()

	headers := knownHeaders(InferenceProviderOpenRouter)
	require.NotNil(t, headers)
	headers["X-Title"] = "mutated"

	require.Equal(t, harnessTitle, knownHeaders(InferenceProviderVercel)["X-Title"])
	require.Nil(t, knownHeaders(InferenceProviderDeepSeek), "a provider with no headers gets none")
}

func TestLegacyProviderID(t *testing.T) {
	t.Parallel()

	for old, want := range map[string]string{
		"gemini":            "google",
		"copilot":           "github-copilot",
		"bedrock":           "amazon-bedrock",
		"vertexai":          "google-vertex",
		"opencode-zen":      "opencode",
		"zhipu":             "zhipuai",
		"moonshot":          "moonshotai",
		"alibaba-singapore": "alibaba",
		"ionet":             "io-net",
	} {
		got, ok := LegacyProviderID(old)
		require.True(t, ok, "%q was renamed", old)
		require.Equal(t, want, got)
	}

	_, ok := LegacyProviderID("openai")
	require.False(t, ok, "an id that never changed is not a rename")

	// "zai" survived the move with a different meaning, so it must not
	// be rewritten: a pay-per-token key would be sent to the coding
	// plan's endpoint.
	_, ok = LegacyProviderID("zai")
	require.False(t, ok)
	note, ok := ProviderMeaningChanged("zai")
	require.True(t, ok)
	require.Contains(t, note, "zai-coding-plan")
}

func TestProviderFamilies(t *testing.T) {
	t.Parallel()

	require.True(t, IsZAI("zai"))
	require.True(t, IsZAI("zai-coding-plan"))
	require.False(t, IsZAI("zhipuai"))

	require.True(t, IsAlibabaDashScope("alibaba"))
	require.True(t, IsAlibabaDashScope("alibaba-cn"))
	require.True(t, IsAlibabaDashScope("alibaba-coding-plan"))
	require.False(t, IsAlibabaDashScope("alibabacloud"))
}

// TestKnownProviderIDsExistUpstream keeps the hand-maintained table from
// drifting: every id it names must be a real models.dev entry, or the
// row is dead weight that silently does nothing.
func TestKnownProviderIDsExistUpstream(t *testing.T) {
	t.Parallel()

	seed := Seed()
	require.NotEmpty(t, seed, "the bundled snapshot is what this is checked against")

	seeded := make(map[InferenceProvider]bool, len(seed))
	for _, p := range seed {
		seeded[p.ID] = true
	}

	// Providers whose every model was filtered out do not reach the
	// catalog even though the table entry is correct.
	allowMissing := map[InferenceProvider]bool{
		InferenceProviderPerplexity: true, // sonar models cannot call tools
	}

	for id := range knownProviders {
		if allowMissing[id] {
			continue
		}
		require.True(t, seeded[id], "known provider %q is not in the catalog", id)
	}
	for old, current := range legacyProviderIDs {
		require.True(t, seeded[current], "%q maps to %q, which is not in the catalog", old, current)
	}
}
