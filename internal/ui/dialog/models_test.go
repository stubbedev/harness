package dialog

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func newTestModelGroup(t *testing.T, providerID, providerName string, modelNames ...string) ModelGroup {
	t.Helper()
	s := styles.CharmtonePantera()
	provider := catalog.Provider{
		ID:   catalog.InferenceProvider(providerID),
		Name: providerName,
	}
	items := make([]*ModelItem, 0, len(modelNames))
	for _, name := range modelNames {
		model := catalog.Model{ID: providerID + ":" + name, Name: name}
		items = append(items, NewModelItem(&s, provider, model, ModelTypeLarge, false))
	}
	return NewModelGroup(&s, providerName, true, items...)
}

// TestModelsListIncrementalFilterMatchesOneShot pins the incremental
// filter cache: typing a query one character at a time must produce the
// same visible set as entering it whole, and backspacing out of the
// cached prefix must fall back to a full pass.
func TestModelsListIncrementalFilterMatchesOneShot(t *testing.T) {
	t.Parallel()

	s := styles.CharmtonePantera()
	groups := []ModelGroup{
		newTestModelGroup(t, "anthropic", "Anthropic", "Claude Opus", "Claude Sonnet", "Claude Haiku"),
		newTestModelGroup(t, "openai", "OpenAI", "GPT-5", "o4-mini"),
		newTestModelGroup(t, "google", "Google", "Gemini Flash", "Gemini Pro"),
	}

	typed := NewModelsList(&s, groups...)
	typed.SetGroups(groups...)
	for _, q := range []string{"c", "cl", "cla", "clau", "claude", "claudes"} {
		typed.SetFilter(q)
	}

	oneShot := NewModelsList(&s, groups...)
	oneShot.SetGroups(groups...)
	oneShot.SetFilter("claudes")

	require.Equal(t, oneShot.VisibleItems(), typed.VisibleItems(),
		"typing the query incrementally must match entering it whole")

	// Backspace past the cached prefix, then extend again.
	typed.SetFilter("gpt")
	typed.SetFilter("gpt5")
	oneShot2 := NewModelsList(&s, groups...)
	oneShot2.SetGroups(groups...)
	oneShot2.SetFilter("gpt5")
	require.Equal(t, oneShot2.VisibleItems(), typed.VisibleItems(),
		"retyping after a backtrack must still match a one-shot filter")
}

// TestShowProviderForAmbiguousModelsAcrossProviders verifies that a model name
// served by several providers shows the provider on every one of its entries.
func TestShowProviderForAmbiguousModelsAcrossProviders(t *testing.T) {
	t.Parallel()

	groups := []ModelGroup{
		newTestModelGroup(t, "openrouter", "OpenRouter", "Ox Alpha"),
		newTestModelGroup(t, "aihubmix", "AIHubMix", "Ox Alpha"),
		newTestModelGroup(t, "venice", "Venice", "Ox Alpha"),
	}

	showProviderForAmbiguousModels(groups)

	for _, group := range groups {
		require.True(t, group.Items[0].showProvider, "expected %q to show its provider", group.Title)
	}
}

// TestShowProviderForAmbiguousModelsKeepsUniqueNamesUnchanged verifies that a
// model offered by a single provider is left alone.
func TestShowProviderForAmbiguousModelsKeepsUniqueNamesUnchanged(t *testing.T) {
	t.Parallel()

	openrouter := newTestModelGroup(t, "openrouter", "OpenRouter", "Ox Alpha", "Sonnet 5")
	groups := []ModelGroup{
		openrouter,
		newTestModelGroup(t, "aihubmix", "AIHubMix", "Ox Alpha"),
	}

	showProviderForAmbiguousModels(groups)

	require.True(t, openrouter.Items[0].showProvider, "Ox Alpha is served by both providers")
	require.False(t, openrouter.Items[1].showProvider, "Sonnet 5 is unique to OpenRouter")
}

// TestShowProviderForAmbiguousModelsIgnoresRecentDuplicates verifies that the
// recently used group, which repeats entries from their own provider group,
// does not make those models look ambiguous.
func TestShowProviderForAmbiguousModelsIgnoresRecentDuplicates(t *testing.T) {
	t.Parallel()

	recent := newTestModelGroup(t, "openrouter", "OpenRouter", "Ox Alpha")
	provider := newTestModelGroup(t, "openrouter", "OpenRouter", "Ox Alpha")

	showProviderForAmbiguousModels([]ModelGroup{recent, provider})

	require.False(t, recent.Items[0].showProvider)
	require.False(t, provider.Items[0].showProvider)
}

// TestShowProviderForAmbiguousModelsFallsBackToProviderName verifies that
// configured providers without an ID are told apart by their name.
func TestShowProviderForAmbiguousModelsFallsBackToProviderName(t *testing.T) {
	t.Parallel()

	first := newTestModelGroup(t, "", "Local A", "Ox Alpha")
	second := newTestModelGroup(t, "", "Local B", "Ox Alpha")

	showProviderForAmbiguousModels([]ModelGroup{first, second})

	require.True(t, first.Items[0].showProvider)
	require.True(t, second.Items[0].showProvider)
}

// TestShowProviderForAmbiguousModelsIgnoresEmptyNames verifies that unnamed
// models are not treated as sharing a name with one another.
func TestShowProviderForAmbiguousModelsIgnoresEmptyNames(t *testing.T) {
	t.Parallel()

	first := newTestModelGroup(t, "openrouter", "OpenRouter", "")
	second := newTestModelGroup(t, "aihubmix", "AIHubMix", "")

	showProviderForAmbiguousModels([]ModelGroup{first, second})

	require.False(t, first.Items[0].showProvider)
	require.False(t, second.Items[0].showProvider)
}
