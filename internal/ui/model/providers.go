package model

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/dialog"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

type providerTargetLoadedMsg struct {
	target dialog.ProviderTarget
	err    error
}

type providerSavedMsg struct {
	providerID catalog.InferenceProvider
	err        error
}

type providerRemovedMsg struct {
	providerID catalog.InferenceProvider
	err        error
}

type copilotImportedMsg struct {
	imported bool
}

func (m *UI) openProvidersDialog() tea.Cmd {
	if m.dialog.FocusIfOpen(dialog.ProvidersID) {
		return nil
	}
	providers, err := dialog.NewProviders(m.com, m.rejectedProviders)
	if err != nil {
		return util.ReportError(err)
	}
	m.dialog.CloseDialog(dialog.ModelsID)
	m.dialog.OpenDialog(providers)
	return nil
}

func (m *UI) configureProvider(id catalog.InferenceProvider) tea.Cmd {
	if id != catalog.InferenceProviderCopilot {
		return loadProviderTargetCmd(m.com.Workspace, id)
	}
	if _, configured := m.com.Config().Providers.Get(id); !configured {
		return importCopilotCmd(m.com.Workspace)
	}
	return m.openCopilotSignIn()
}

func (m *UI) openCopilotSignIn() tea.Cmd {
	provider := catalog.Provider{ID: catalog.InferenceProviderCopilot, Name: "GitHub Copilot"}
	providers, _ := config.Providers(m.com.Config())
	if idx := slices.IndexFunc(providers, func(p catalog.Provider) bool { return p.ID == provider.ID }); idx >= 0 {
		provider = providers[idx]
	}
	if m.dialog.FocusIfOpen(dialog.OAuthID) {
		return nil
	}
	dlg, cmd := dialog.NewOAuthCopilot(m.com, provider)
	m.dialog.OpenDialogWithGrace(dlg)
	return cmd
}

func loadProviderTargetCmd(ws workspace.Workspace, id catalog.InferenceProvider) tea.Cmd {
	return func() tea.Msg {
		cfg := ws.Config()
		providers, err := config.Providers(cfg)
		if err != nil {
			if len(providers) == 0 {
				return providerTargetLoadedMsg{err: fmt.Errorf("failed to get providers: %w", err)}
			}
			slog.Warn("Listing the previously known providers", "error", err)
		}
		var settings config.ProviderConfig
		if id != "" {
			if settings, _, err = ws.ProviderSettings(id); err != nil {
				return providerTargetLoadedMsg{err: fmt.Errorf("failed to read the settings of provider %s: %w", id, err)}
			}
		}
		return providerTargetLoadedMsg{target: dialog.NewProviderTarget(cfg, providers, id, settings)}
	}
}

func importCopilotCmd(ws workspace.Workspace) tea.Cmd {
	return func() tea.Msg {
		_, imported := ws.ImportCopilot()
		return copilotImportedMsg{imported: imported}
	}
}

func (m *UI) applyProviderTargetLoaded(msg providerTargetLoadedMsg) tea.Cmd {
	if msg.err != nil {
		return util.ReportError(msg.err)
	}
	m.dialog.CloseDialog(dialog.ProviderFormID)
	m.dialog.OpenDialog(dialog.NewProviderForm(m.com, msg.target))
	return nil
}

func (m *UI) applyCopilotImported(msg copilotImportedMsg) tea.Cmd {
	if msg.imported {
		return m.providerConfigured(catalog.InferenceProviderCopilot)
	}
	return m.openCopilotSignIn()
}

func (m *UI) saveProvider(msg dialog.ActionSaveProvider) tea.Cmd {
	ws := m.com.Workspace
	return m.updateAgentModelCmd(func() tea.Msg {
		if err := ws.UpdateProvider(context.Background(), config.ScopeGlobal, msg.ProviderID, msg.Update); err != nil {
			return providerSavedMsg{providerID: msg.ProviderID, err: err}
		}
		return providerSavedMsg{providerID: msg.ProviderID, err: refreshAgentProviders(ws)}
	})
}

func refreshAgentProviders(ws workspace.Workspace) error {
	ready, err := agentReady(ws)
	if err != nil || !ready {
		return err
	}
	return ws.UpdateAgentModel(context.Background())
}

func (m *UI) applyProviderSaved(msg providerSavedMsg) tea.Cmd {
	if msg.err != nil {
		if form, ok := m.dialog.Dialog(dialog.ProviderFormID).(*dialog.ProviderForm); ok {
			form.SaveFailed(msg.err)
		}
		return util.ReportError(fmt.Errorf("failed to save provider %s: %w", msg.providerID, msg.err))
	}
	return m.providerConfigured(msg.providerID)
}

func (m *UI) providerConfigured(id catalog.InferenceProvider) tea.Cmd {
	delete(m.rejectedProviders, id)
	m.dialog.CloseDialog(dialog.ProviderFormID)
	m.dialog.CloseDialog(dialog.OAuthID)
	m.dialog.CloseDialog(dialog.ProvidersID)

	cfg := m.com.Config()
	providerCfg, ok := cfg.Providers.Get(id)
	if !ok {
		return util.ReportWarn(fmt.Sprintf("Saved %s, but it is not usable yet; check its settings", id))
	}
	name := cmp.Or(providerCfg.Name, string(id))
	if cfg.Models[config.SelectedModelTypeLarge].Model != "" {
		return util.ReportInfo(name + " configured")
	}
	selected, ok := defaultProviderModel(cfg, providerCfg)
	if !ok {
		return util.ReportWarn(name + " configured, but it offers no models")
	}
	return m.handleSelectModel(dialog.ActionSelectModel{
		Provider:  providerCfg.ToProvider(),
		Model:     selected,
		ModelType: config.SelectedModelTypeLarge,
	})
}

func defaultProviderModel(cfg *config.Config, providerCfg config.ProviderConfig) (config.SelectedModel, bool) {
	if len(providerCfg.Models) == 0 {
		return config.SelectedModel{}, false
	}
	model := providerCfg.Models[0]
	if providers, _ := config.Providers(cfg); len(providers) > 0 {
		if idx := slices.IndexFunc(providers, func(p catalog.Provider) bool { return p.ID == providerCfg.ID }); idx >= 0 {
			if mi := slices.IndexFunc(providerCfg.Models, func(m catalog.Model) bool { return m.ID == providers[idx].DefaultLargeModelID }); mi >= 0 {
				model = providerCfg.Models[mi]
			}
		}
	}
	return config.SelectedModel{
		Model:           model.ID,
		Provider:        providerCfg.ID,
		ReasoningEffort: catalog.DefaultReasoningLevel(model.ReasoningLevels),
		MaxTokens:       model.DefaultMaxTokens,
	}, true
}

func (m *UI) removeProvider(id catalog.InferenceProvider) tea.Cmd {
	cfg := m.com.Config()
	for _, modelType := range []config.SelectedModelType{config.SelectedModelTypeLarge, config.SelectedModelTypeSmall} {
		if cfg.Models[modelType].Provider == id {
			return util.ReportError(fmt.Errorf("%s serves the %s model; choose another %s model before disconnecting it", id, modelType, modelType))
		}
	}
	ws := m.com.Workspace
	return m.updateAgentModelCmd(func() tea.Msg {
		if err := ws.RemoveProvider(context.Background(), config.ScopeGlobal, id); err != nil {
			return providerRemovedMsg{providerID: id, err: err}
		}
		return providerRemovedMsg{providerID: id, err: refreshAgentProviders(ws)}
	})
}

func (m *UI) applyProviderRemoved(msg providerRemovedMsg) tea.Cmd {
	if msg.err != nil {
		return util.ReportError(fmt.Errorf("failed to disconnect %s: %w", msg.providerID, msg.err))
	}
	delete(m.rejectedProviders, msg.providerID)
	m.dialog.CloseDialog(dialog.ProvidersID)
	return tea.Batch(m.openProvidersDialog(), util.ReportInfo(fmt.Sprintf("Disconnected %s", msg.providerID)))
}

func (m *UI) handleReAuthenticate(providerID catalog.InferenceProvider) tea.Cmd {
	if _, ok := m.com.Config().Providers.Get(providerID); !ok {
		return nil
	}
	if m.rejectedProviders == nil {
		m.rejectedProviders = make(map[catalog.InferenceProvider]bool)
	}
	m.rejectedProviders[providerID] = true
	return m.configureProvider(providerID)
}

var errProviderNotConfigured = errors.New("provider is not configured")
