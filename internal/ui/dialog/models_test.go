package dialog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/workspace"
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

// stubWorkspace feeds the models dialog a config without touching a real
// workspace.
type stubWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *stubWorkspace) Config() *config.Config { return w.cfg }

func newModelsDialogForTest(t *testing.T, cfg *config.Config, providers []catalog.Provider, isOnboarding bool) *Models {
	t.Helper()

	// Keep setProviderItems off the shared catalog cache: the
	// default-provider path would hit it (and the network) via
	// config.Providers.
	if cfg.Options == nil {
		cfg.Options = &config.Options{}
	}
	cfg.Options.DisableDefaultProviders = true

	s := styles.CharmtonePantera()
	m := &Models{
		com:          &common.Common{Workspace: &stubWorkspace{cfg: cfg}, Styles: &s},
		isOnboarding: isOnboarding,
		modelType:    ModelTypeLarge,
		providers:    providers,
	}
	m.list = NewModelsList(&s)
	require.NoError(t, m.setProviderItems())
	return m
}

func configuredTestConfig(t *testing.T, providerIDs ...string) *config.Config {
	t.Helper()
	providers := map[string]config.ProviderConfig{}
	for _, id := range providerIDs {
		providers[id] = config.ProviderConfig{
			ID:     id,
			APIKey: "test-key",
			Models: []catalog.Model{{ID: id + "-model", Name: id + " model"}},
		}
	}
	return &config.Config{
		Options:   &config.Options{},
		Providers: csync.NewMapFrom(providers),
	}
}

func testCatalogProviders() []catalog.Provider {
	return []catalog.Provider{
		{ID: catalog.InferenceProvider("anthropic"), Name: "Anthropic", Type: catalog.TypeAnthropic},
		{ID: catalog.InferenceProvider("openai"), Name: "OpenAI", Type: catalog.TypeOpenAI},
		{ID: catalog.InferenceProvider("azure"), Name: "Azure", Type: catalog.TypeAzure},
		{ID: catalog.InferenceProvider("google-vertex"), Name: "Google Vertex", Type: catalog.TypeVertexAI},
	}
}

// TestModelsDialogHidesUnconfiguredProviders pins that once a provider is
// configured, the dialog stops offering the rest of the catalog: an entry
// without credentials cannot serve a request.
func TestModelsDialogHidesUnconfiguredProviders(t *testing.T) {
	t.Parallel()

	m := newModelsDialogForTest(t, configuredTestConfig(t, "openai"), testCatalogProviders(), false)
	require.Len(t, m.list.groups, 1)
	assert.Equal(t, "openai", m.list.groups[0].Title, "the group is named from the config when no display name is set")
}

// TestModelsDialogShowsCatalogDuringOnboardingAndWhenUnconfigured pins the
// two cases where the full catalog must stay visible: onboarding (it is
// the only way to pick a first provider) and a config with nothing set.
func TestModelsDialogShowsCatalogDuringOnboardingAndWhenUnconfigured(t *testing.T) {
	t.Parallel()

	m := newModelsDialogForTest(t, configuredTestConfig(t, "openai"), testCatalogProviders(), true)
	assert.Len(t, m.list.groups, 2, "onboarding shows the whole catalog")

	m = newModelsDialogForTest(t, configuredTestConfig(t), testCatalogProviders(), false)
	require.Len(t, m.list.groups, 2, "no configured provider falls back to the catalog, minus the config-only providers")
	for _, g := range m.list.groups {
		assert.NotContains(t, []string{"Azure", "Google Vertex"}, g.Title,
			"providers the TUI cannot authenticate never show, even in the fallback")
	}
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

// TestModelsDialogDropsConfiguredBadgeWhenEveryGroupIsConfigured pins that
// the badge only shows while the catalog is on screen: once the list is
// nothing but configured providers, a badge on every row says nothing.
func TestModelsDialogDropsConfiguredBadgeWhenEveryGroupIsConfigured(t *testing.T) {
	t.Parallel()

	m := newModelsDialogForTest(t, configuredTestConfig(t, "openai"), testCatalogProviders(), false)
	require.Len(t, m.list.groups, 1)
	assert.False(t, m.list.groups[0].configured, "no badge when there is nothing to tell apart")

	m = newModelsDialogForTest(t, configuredTestConfig(t, "openai"), testCatalogProviders(), true)
	require.Len(t, m.list.groups, 2)
	assert.True(t, m.list.groups[0].configured, "onboarding mixes both, so the badge marks the ready one")
}
