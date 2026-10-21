package model

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/stringext"
	"github.com/stubbedev/harness/internal/ui/dialog"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

type modelWrite struct {
	saved bool
	err   error
}

type modelSelectedMsg struct {
	action dialog.ActionSelectModel
	modelWrite
}

type reasoningEffortSavedMsg struct {
	effort string
	modelWrite
}

type sessionModelRestoredMsg struct {
	model config.SelectedModel
	modelWrite
}

type copilotImportedMsg struct {
	action dialog.ActionSelectModel
}

func saveModel(ws workspace.Workspace, modelType config.SelectedModelType, model config.SelectedModel, steps ...func() error) modelWrite {
	if err := ws.UpdatePreferredModel(config.ScopeGlobal, modelType, model); err != nil {
		return modelWrite{err: err}
	}
	var errs []error
	for _, step := range steps {
		if step != nil {
			errs = append(errs, step())
		}
	}
	errs = append(errs, syncAgentModel(ws))
	return modelWrite{saved: true, err: errors.Join(errs...)}
}

func syncAgentModel(ws workspace.Workspace) error {
	ready, err := agentReady(ws)
	if err != nil {
		return err
	}
	if ready {
		return ws.UpdateAgentModel(context.Background())
	}
	return ws.InitCoderAgent(context.Background())
}

func defaultSmallModelStep(ws workspace.Workspace, cfg *config.Config, providerID catalog.InferenceProvider) func() error {
	if _, ok := cfg.Models[config.SelectedModelTypeSmall]; ok {
		return nil
	}
	return func() error {
		small, err := ws.GetDefaultSmallModel(providerID)
		if err != nil {
			return fmt.Errorf("resolve default small model: %w", err)
		}
		return ws.UpdatePreferredModel(config.ScopeGlobal, config.SelectedModelTypeSmall, small)
	}
}

// restoreModelFromSession checks the last assistant message in the
// loaded session and, if it used a different provider/model than the
// current config, restores that model/provider provided it is still
// available. Returns a tea.Cmd that rebuilds the agent models if a
// switch was made, or nil if no switch was needed.
func (m *UI) restoreModelFromSession(msgs []message.Message) tea.Cmd {
	var lastAssistant *message.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == message.Assistant && !msgs[i].IsSummaryMessage {
			lastAssistant = &msgs[i]
			break
		}
	}
	if lastAssistant == nil || lastAssistant.Provider == "" || lastAssistant.Model == "" {
		return nil
	}

	cfg := m.com.Config()
	if cfg == nil {
		return nil
	}

	currentLarge := cfg.Models[config.SelectedModelTypeLarge]
	if currentLarge.Provider == lastAssistant.Provider && currentLarge.Model == lastAssistant.Model {
		return nil
	}

	if !cfg.IsModelAvailable(lastAssistant.Provider, lastAssistant.Model) {
		slog.Debug("Skipping model restoration: provider/model not available",
			"provider", lastAssistant.Provider,
			"model", lastAssistant.Model)
		return nil
	}

	selected := config.SelectedModel{
		Provider: lastAssistant.Provider,
		Model:    lastAssistant.Model,
	}
	ws := m.com.Workspace
	smallStep := defaultSmallModelStep(ws, cfg, selected.Provider)
	return m.updateAgentModelCmd(func() tea.Msg {
		return sessionModelRestoredMsg{
			model:      selected,
			modelWrite: saveModel(ws, config.SelectedModelTypeLarge, selected, smallStep),
		}
	})
}

func (m *UI) applySessionModelRestored(msg sessionModelRestoredMsg) tea.Cmd {
	if msg.saved {
		m.applyThemeForProvider(msg.model.Provider)
		slog.Info("Restored model from session", "provider", msg.model.Provider, "model", msg.model.Model)
	}
	if msg.err != nil {
		return util.ReportError(fmt.Errorf("restore model from session: %w", msg.err))
	}
	return nil
}

// handleSelectReasoningEffort applies a reasoning effort selection.
// While the agent is mid-turn the choice is queued and applied when
// the turn finishes, so the in-flight request is untouched.
func (m *UI) handleSelectReasoningEffort(msg dialog.ActionSelectReasoningEffort) tea.Cmd {
	if m.isAgentBusy() {
		m.pendingModelAction = msg
		m.dialog.CloseDialog(dialog.ReasoningID)
		return util.ReportInfo("Reasoning effort applies after the current turn")
	}

	cfg := m.com.Config()
	if cfg == nil {
		return util.ReportError(errors.New("configuration not found"))
	}

	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return util.ReportError(errors.New("agent configuration not found"))
	}

	ws := m.com.Workspace
	modelType := agentCfg.Model
	effort := msg.Effort
	currentModel := cfg.Models[modelType]
	currentModel.ReasoningEffort = effort

	// Remember the choice per model so switching models and back
	// restores it instead of leaking the current model's effort. The
	// whole map is written in one field: model IDs contain dots, which
	// the dot-separated field paths cannot express.
	var rememberEffort func() error
	if currentModel.Provider != "" && currentModel.Model != "" {
		efforts := maps.Clone(cfg.ReasoningEfforts)
		if efforts == nil {
			efforts = make(map[string]string)
		}
		efforts[config.ModelReasoningKey(currentModel.Provider, currentModel.Model)] = effort
		rememberEffort = func() error {
			return ws.SetConfigField(config.ScopeGlobal, "reasoning_efforts", efforts)
		}
	}

	return m.updateAgentModelCmd(func() tea.Msg {
		return reasoningEffortSavedMsg{
			effort:     effort,
			modelWrite: saveModel(ws, modelType, currentModel, rememberEffort),
		}
	})
}

func (m *UI) applyReasoningEffortSaved(msg reasoningEffortSavedMsg) tea.Cmd {
	if !msg.saved {
		return util.ReportError(msg.err)
	}
	m.dialog.CloseDialog(dialog.ReasoningID)
	if msg.err != nil {
		return util.ReportError(msg.err)
	}
	return util.ReportInfo("Reasoning effort set to " + msg.effort)
}

// handleSelectModel performs the model selection after any provider
// pre-checks have completed.
func (m *UI) handleSelectModel(msg dialog.ActionSelectModel) tea.Cmd {
	return m.selectModel(msg, true)
}

func (m *UI) selectModel(msg dialog.ActionSelectModel, importCopilot bool) tea.Cmd {
	// we ignore dialogs with the oauth id as they need to be able to be dismissed
	if m.isAgentBusy() && !m.dialog.ContainsDialog(dialog.OAuthID) {
		// The in-flight turn keeps the model it started with; the
		// choice is queued and applied when the turn finishes.
		m.pendingModelAction = msg
		m.dialog.CloseDialog(dialog.ModelsID)
		return util.ReportInfo("Model change applies after the current turn")
	}

	cfg := m.com.Config()
	if cfg == nil {
		return util.ReportError(errors.New("configuration not found"))
	}

	providerID := msg.Model.Provider
	_, configured := cfg.Providers.Get(providerID)

	if importCopilot && !configured && !msg.ReAuthenticate && providerID == catalog.InferenceProviderCopilot {
		return m.importCopilotCmd(msg)
	}

	if !configured || msg.ReAuthenticate {
		m.dialog.CloseDialog(dialog.ModelsID)
		m.dialog.CloseDialog(dialog.ConnectID)
		return m.openAuthenticationDialog(msg.Provider, msg.Model, msg.ModelType)
	}

	// Restore a remembered per-model reasoning effort so manual choices
	// survive model switches; without one the selection defaults to the
	// model's highest supported level.
	if effort, ok := cfg.ReasoningEfforts[config.ModelReasoningKey(msg.Model.Provider, msg.Model.Model)]; ok {
		msg.Model.ReasoningEffort = effort
	}

	ws := m.com.Workspace
	smallStep := defaultSmallModelStep(ws, cfg, providerID)
	return m.updateAgentModelCmd(func() tea.Msg {
		return modelSelectedMsg{
			action:     msg,
			modelWrite: saveModel(ws, msg.ModelType, msg.Model, smallStep),
		}
	})
}

func (m *UI) importCopilotCmd(msg dialog.ActionSelectModel) tea.Cmd {
	ws := m.com.Workspace
	return func() tea.Msg {
		ws.ImportCopilot()
		return copilotImportedMsg{action: msg}
	}
}

func (m *UI) applyModelSelected(msg modelSelectedMsg) tea.Cmd {
	if !msg.saved {
		return util.ReportError(msg.err)
	}
	if msg.action.ModelType == config.SelectedModelTypeLarge {
		m.applyThemeForProvider(msg.action.Model.Provider)
	}
	m.dialog.CloseDialog(dialog.APIKeyInputID)
	m.dialog.CloseDialog(dialog.OAuthID)
	m.dialog.CloseDialog(dialog.ModelsID)
	m.dialog.CloseDialog(dialog.ConnectID)
	if msg.err != nil {
		return util.ReportError(msg.err)
	}

	modelName := msg.action.Model.Model
	if cfg := m.com.Config(); cfg != nil {
		if catwalkModel := cfg.GetModel(msg.action.Model.Provider, msg.action.Model.Model); catwalkModel != nil && catwalkModel.Name != "" {
			modelName = catwalkModel.Name
		}
	}
	return util.ReportInfo(fmt.Sprintf("%s model changed to %s", stringext.Capitalize(string(msg.action.ModelType)), modelName))
}
