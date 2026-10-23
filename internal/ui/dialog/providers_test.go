package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func newProvidersDialogForTest(t *testing.T, cfg *config.Config, providers []catalog.Provider, rejected ...catalog.InferenceProvider) *Providers {
	t.Helper()
	s := styles.CharmtonePantera()
	marks := make(map[catalog.InferenceProvider]bool)
	for _, id := range rejected {
		marks[id] = true
	}
	return newProviders(&common.Common{Workspace: &stubWorkspace{cfg: cfg}, Styles: &s}, providers, marks)
}

func providerTestCatalog() []catalog.Provider {
	providers := testCatalogProviders()
	for i, p := range providers {
		providers[i].Models = []catalog.Model{{ID: string(p.ID) + "-large", Name: "Large"}}
	}
	return providers
}

func providerRows(t *testing.T, p *Providers) [][2]string {
	t.Helper()
	var rows [][2]string
	for _, item := range p.list.FilteredItems() {
		row := item.(PickerItem)
		rows = append(rows, [2]string{row.Label(), row.RightLabel()})
	}
	return rows
}

func TestProvidersListsEveryProviderWithItsState(t *testing.T) {
	t.Parallel()

	cfg := configuredTestConfig(t, "openai", "work")
	p := newProvidersDialogForTest(t, cfg, providerTestCatalog())

	require.Equal(t, [][2]string{
		{"Add custom provider", "openai or anthropic compatible"},
		{"OpenAI", "connected"},
		{"work", "connected"},
		{"Anthropic", "api key · 1 model"},
		{"Azure", "endpoint & key · 1 model"},
		{"Google Vertex", "google cloud · 1 model"},
	}, providerRows(t, p))
}

func TestProvidersMarksRejectedCredentials(t *testing.T) {
	t.Parallel()

	p := newProvidersDialogForTest(t, configuredTestConfig(t, "openai"), providerTestCatalog(), "openai")
	require.Equal(t, [2]string{"OpenAI", "credentials rejected"}, providerRows(t, p)[1])
}

func TestProvidersSelectOpensConfiguration(t *testing.T) {
	t.Parallel()

	p := newProvidersDialogForTest(t, configuredTestConfig(t, "openai"), providerTestCatalog())
	require.Equal(t, ActionConfigureProvider{}, p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))

	p.list.SetSelected(1)
	require.Equal(t, ActionConfigureProvider{ProviderID: "openai"}, p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
}

func TestProvidersDisconnectAsksForConfirmation(t *testing.T) {
	t.Parallel()

	p := newProvidersDialogForTest(t, configuredTestConfig(t, "openai"), providerTestCatalog())
	p.list.SetSelected(1)

	require.Nil(t, p.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}))
	require.NotNil(t, p.removing)
	require.Nil(t, p.HandleMsg(tea.KeyPressMsg{Code: 'n', Text: "n"}))
	require.Nil(t, p.removing, "n cancels")

	p.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.Equal(t, ActionRemoveProvider{ProviderID: "openai"}, p.HandleMsg(tea.KeyPressMsg{Code: 'y', Text: "y"}))
}

func TestProvidersDisconnectIgnoresUnconfigured(t *testing.T) {
	t.Parallel()

	p := newProvidersDialogForTest(t, configuredTestConfig(t, "openai"), providerTestCatalog())
	p.list.SetSelected(2)

	p.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.Nil(t, p.removing)
	for _, b := range p.ShortHelp() {
		require.NotEqual(t, p.remove.Remove.Help(), b.Help(), "the disconnect hint is gated on a configured provider")
	}
}

func TestProvidersOfferSignInForOpenCodeGateways(t *testing.T) {
	t.Parallel()

	for _, id := range []catalog.InferenceProvider{catalog.InferenceProviderOpenCodeGo, catalog.InferenceProviderOpenCodeZen, catalog.InferenceProviderCopilot} {
		require.Equal(t, "sign in", providerCredentialKind(catalog.Provider{ID: id, Type: catalog.TypeOpenAICompat}), id)
	}
	require.Equal(t, "api key", providerCredentialKind(catalog.Provider{ID: "deepseek", Type: catalog.TypeOpenAICompat}))
}
