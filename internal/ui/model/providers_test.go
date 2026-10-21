package model

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/dialog"
	"github.com/stubbedev/harness/internal/ui/util"
)

func (w *blockingWorkspace) UpdateProvider(_ context.Context, _ config.Scope, providerID catalog.InferenceProvider, _ config.ProviderUpdate) error {
	w.write("update-provider:" + string(providerID))
	return nil
}

func (w *blockingWorkspace) RemoveProvider(_ context.Context, _ config.Scope, providerID catalog.InferenceProvider) error {
	w.write("remove-provider:" + string(providerID))
	return nil
}

func (w *blockingWorkspace) ProviderSettings(providerID catalog.InferenceProvider) (config.ProviderConfig, bool, error) {
	w.write("settings:" + string(providerID))
	return config.ProviderConfig{ID: providerID, APIKey: "$P_KEY"}, true, nil
}

func TestSaveProviderDoesNotBlockUpdateAndLeavesModels(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	update := config.ProviderUpdate{Set: map[config.ProviderField]any{config.ProviderFieldAPIKey: "sk-new"}}

	cmd := returnsPromptly(t, func() tea.Cmd {
		return m.handleAction(dialog.ActionSaveProvider{ProviderID: "p", Update: update})
	})
	require.Empty(t, ws.recorded())

	close(ws.release)
	saved := findMsg[providerSavedMsg](t, drainMsgs(cmd))
	require.NoError(t, saved.err)
	require.Equal(t, []string{"update-provider:p", "update-agent"}, ws.recorded(), "saving a provider never writes models.large or models.small")
}

func TestProviderSavedClosesDialogsAndKeepsModel(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	close(ws.release)
	m.rejectedProviders = map[catalog.InferenceProvider]bool{"p": true}
	m.dialog.OpenDialog(stubDialog{id: dialog.ProvidersID})
	m.dialog.OpenDialog(stubDialog{id: dialog.ProviderFormID})

	_, cmd := m.Update(providerSavedMsg{providerID: "p"})

	require.False(t, m.dialog.ContainsDialog(dialog.ProviderFormID))
	require.False(t, m.dialog.ContainsDialog(dialog.ProvidersID))
	require.False(t, m.rejectedProviders["p"], "a saved provider is no longer marked rejected")
	info := findMsg[util.InfoMsg](t, drainMsgs(cmd))
	require.Equal(t, "p configured", info.Msg)
	require.Empty(t, ws.recorded())
}

func TestProviderSavedFirstTimeSelectsDefaultModel(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, false)
	close(ws.release)
	ws.cfg.Models = map[config.SelectedModelType]config.SelectedModel{}

	_, cmd := m.Update(providerSavedMsg{providerID: "p"})

	selected := findMsg[modelSelectedMsg](t, drainMsgs(cmd))
	require.Equal(t, config.SelectedModel{Provider: "p", Model: "m1"}, selected.action.Model)
}

func TestProviderSaveFailureKeepsFormOpen(t *testing.T) {
	t.Parallel()
	m, _ := newModelSelectUI(t, true)
	m.dialog.OpenDialog(stubDialog{id: dialog.ProviderFormID})

	_, cmd := m.Update(providerSavedMsg{providerID: "p", err: errUnreachable})

	require.True(t, m.dialog.ContainsDialog(dialog.ProviderFormID))
	info := findMsg[util.InfoMsg](t, drainMsgs(cmd))
	require.Equal(t, util.InfoTypeError, info.Type)
}

func TestSelectingModelOfUnconfiguredProviderReportsError(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	close(ws.release)

	cmd := m.handleAction(dialog.ActionSelectModel{
		Model:     config.SelectedModel{Provider: "missing", Model: "x"},
		ModelType: config.SelectedModelTypeLarge,
	})

	info := findMsg[util.InfoMsg](t, drainMsgs(cmd))
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Empty(t, ws.recorded())
	require.False(t, m.dialog.ContainsDialog(dialog.OAuthID))
}

func TestRemoveProviderRefusesWhileServingAModel(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	close(ws.release)

	info := findMsg[util.InfoMsg](t, drainMsgs(m.handleAction(dialog.ActionRemoveProvider{ProviderID: "p"})))
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Contains(t, info.Msg, "serves the large model")
	require.Empty(t, ws.recorded())
}

func TestRemoveProviderDisconnects(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	ws.cfg.Providers.Set("q", config.ProviderConfig{ID: "q", Models: []catalog.Model{{ID: "q1"}}})

	cmd := returnsPromptly(t, func() tea.Cmd {
		return m.handleAction(dialog.ActionRemoveProvider{ProviderID: "q"})
	})
	require.Empty(t, ws.recorded())

	close(ws.release)
	removed := findMsg[providerRemovedMsg](t, drainMsgs(cmd))
	require.NoError(t, removed.err)
	require.Equal(t, []string{"remove-provider:q", "update-agent"}, ws.recorded())
}

func TestReAuthenticateMarksProviderAndOpensForm(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	close(ws.release)

	loaded := findMsg[providerTargetLoadedMsg](t, drainMsgs(m.handleReAuthenticate("p")))
	require.True(t, m.rejectedProviders["p"])
	require.NoError(t, loaded.err)
	require.Equal(t, "$P_KEY", loaded.target.Settings.APIKey)

	m.Update(loaded)
	require.True(t, m.dialog.ContainsDialog(dialog.ProviderFormID))
}
