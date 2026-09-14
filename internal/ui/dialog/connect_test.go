package dialog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func newConnectDialogForTest(t *testing.T, cfg *config.Config, providers []catalog.Provider) *Connect {
	t.Helper()

	s := styles.CharmtonePantera()
	c := &Connect{
		com:  &common.Common{Workspace: &stubWorkspace{cfg: cfg}, Styles: &s},
		list: list.NewFilterableList(),
	}
	c.setItems(providers)
	return c
}

func connectTestProviders() []catalog.Provider {
	withModels := func(p catalog.Provider) catalog.Provider {
		p.Models = []catalog.Model{{ID: string(p.ID) + "-large", Name: "Large"}}
		p.DefaultLargeModelID = string(p.ID) + "-large"
		return p
	}
	providers := testCatalogProviders()
	for i, p := range providers {
		providers[i] = withModels(p)
	}
	return providers
}

// TestConnectListsOnlyUnconfiguredProviders pins the split between the two
// dialogs: a provider with credentials belongs in the models dialog, and
// only what is left shows up here.
func TestConnectListsOnlyUnconfiguredProviders(t *testing.T) {
	t.Parallel()

	c := newConnectDialogForTest(t, configuredTestConfig(t, "openai"), connectTestProviders())

	var names []string
	for _, item := range c.list.FilteredItems() {
		names = append(names, item.(*ConnectItem).name())
	}
	assert.Equal(t, []string{"Anthropic"}, names,
		"OpenAI is configured, and Azure and Google Vertex cannot be authenticated from the TUI")
}

// TestConnectSelectsDefaultLargeModel pins that connecting a provider
// carries its default large model into the authentication flow, which is
// what gets selected once the key verifies.
func TestConnectSelectsDefaultLargeModel(t *testing.T) {
	t.Parallel()

	provider := catalog.Provider{
		ID:                  catalog.InferenceProvider("anthropic"),
		Name:                "Anthropic",
		Type:                catalog.TypeAnthropic,
		DefaultLargeModelID: "big",
		Models: []catalog.Model{
			{ID: "small", Name: "Small"},
			{ID: "big", Name: "Big", DefaultMaxTokens: 4096},
		},
	}

	selected := defaultSelectedModel(provider)
	require.Equal(t, "big", selected.Model)
	assert.Equal(t, "anthropic", selected.Provider)
	assert.Equal(t, int64(4096), selected.MaxTokens)
}

// TestConnectFallsBackToFirstModel pins that a provider without a declared
// default still connects, rather than dead-ending on an empty model.
func TestConnectFallsBackToFirstModel(t *testing.T) {
	t.Parallel()

	provider := catalog.Provider{
		ID:     catalog.InferenceProvider("anthropic"),
		Models: []catalog.Model{{ID: "only", Name: "Only"}},
	}
	assert.Equal(t, "only", defaultSelectedModel(provider).Model)
}
