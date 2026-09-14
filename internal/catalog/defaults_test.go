package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func toolModel(id string, context, output int64, in, out float64, released string) modelsDevModel {
	return modelsDevModel{
		ID: id, Name: id, ToolCall: true, ReleaseDate: released,
		Limit:      modelsDevLimit{Context: context, Output: output},
		Cost:       modelsDevCost{Input: in, Output: out},
		Modalities: modelsDevModalities{Input: []string{"text"}, Output: []string{"text"}},
	}
}

func compatProvider(id string, models ...modelsDevModel) modelsDevProvider {
	byID := make(map[string]modelsDevModel, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	return modelsDevProvider{
		ID: id, Name: id, NPM: "@ai-sdk/openai-compatible",
		API: "https://example.test/v1", Env: []string{"EXAMPLE_API_KEY"},
		Models: byID,
	}
}

// TestUsableModelsOnly covers the filter that keeps the catalog to
// models the agent loop can actually drive.
func TestUsableModelsOnly(t *testing.T) {
	t.Parallel()

	embedding := modelsDevModel{
		ID: "embed-1", Name: "Embed", ToolCall: false,
		Limit:      modelsDevLimit{Context: 8192},
		Modalities: modelsDevModalities{Input: []string{"text"}, Output: []string{"embedding"}},
	}
	imageOut := modelsDevModel{
		ID: "paint-1", Name: "Paint", ToolCall: true,
		Limit:      modelsDevLimit{Context: 8192},
		Modalities: modelsDevModalities{Input: []string{"text"}, Output: []string{"image"}},
	}
	noTools := modelsDevModel{
		ID: "chat-no-tools", Name: "Chat", ToolCall: false,
		Limit:      modelsDevLimit{Context: 8192},
		Modalities: modelsDevModalities{Input: []string{"text"}, Output: []string{"text"}},
	}
	deprecated := toolModel("old", 8192, 1024, 1, 2, "2024-01-01")
	deprecated.Status = "deprecated"
	unspecified := modelsDevModel{ID: "bare", Name: "Bare", ToolCall: true, Limit: modelsDevLimit{Context: 4096}}

	src := compatProvider("example",
		toolModel("chat-1", 128000, 8192, 1, 2, "2025-01-01"),
		embedding, imageOut, noTools, deprecated, unspecified,
	)

	providers := translateModelsDev(modelsDev{"example": src})
	p, ok := findProvider(providers, "example")
	require.True(t, ok)

	ids := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		ids = append(ids, m.ID)
	}
	require.ElementsMatch(t, []string{"chat-1", "bare"}, ids,
		"embeddings, image output, tool-less and deprecated models are not chat models")
}

// TestDefaultModelSelection covers the pair a provider opens with, which
// models.dev does not publish.
func TestDefaultModelSelection(t *testing.T) {
	t.Parallel()

	priced := compatProvider("priced",
		toolModel("flagship", 1000000, 64000, 2, 10, "2026-01-01"),
		toolModel("older-flagship", 1000000, 64000, 2, 10, "2025-01-01"),
		toolModel("cheap-wide", 1000000, 64000, 0.1, 0.4, "2026-01-01"),
		toolModel("mini", 128000, 16000, 0.05, 0.2, "2026-01-01"),
	)
	free := compatProvider("free",
		toolModel("plan-large", 1000000, 64000, 0, 0, "2026-01-01"),
		toolModel("plan-small", 32000, 8000, 0, 0, "2026-01-01"),
	)

	providers := translateModelsDev(modelsDev{"priced": priced, "free": free})

	p, ok := findProvider(providers, "priced")
	require.True(t, ok)
	require.Equal(t, "flagship", p.DefaultLargeModelID,
		"widest context, then the priciest, then the most recent")
	require.Equal(t, "mini", p.DefaultSmallModelID, "the cheapest model runs the cheap calls")
	require.Equal(t, "flagship", p.Models[0].ID, "the picker shows the flagship first")

	f, ok := findProvider(providers, "free")
	require.True(t, ok)
	require.Equal(t, "plan-large", f.DefaultLargeModelID)
	require.Equal(t, "plan-small", f.DefaultSmallModelID,
		"with no prices to rank by, the narrowest context is the small model")
}

// TestSeedIsUsable guards the snapshot bundled in the binary: it is the
// only catalog a first run without network has.
func TestSeedIsUsable(t *testing.T) {
	t.Parallel()

	seed := Seed()
	require.NotEmpty(t, seed)

	byID := make(map[InferenceProvider]Provider, len(seed))
	for _, p := range seed {
		byID[p.ID] = p
	}

	for _, id := range []InferenceProvider{
		InferenceProviderAnthropic,
		InferenceProviderOpenAI,
		InferenceProviderGemini,
		InferenceProviderOpenRouter,
		InferenceProviderZAI,
		InferenceProviderZAICoding,
	} {
		p, ok := byID[id]
		require.True(t, ok, "provider %q is missing from the seed", id)
		require.NotEmpty(t, p.Models, "provider %q has no models", id)
		require.NotEmpty(t, p.Type, "provider %q has no protocol", id)
	}

	for _, p := range seed {
		require.NotEmpty(t, p.Models, "provider %q was seeded with no models", p.ID)
		require.NotEmpty(t, p.DefaultLargeModelID, "provider %q has no default large model", p.ID)
		require.NotEmpty(t, p.DefaultSmallModelID, "provider %q has no default small model", p.ID)
		if p.Type == TypeOpenAICompat {
			require.NotEmpty(t, p.APIEndpoint, "openai-compat provider %q has no endpoint", p.ID)
		}
	}
}
