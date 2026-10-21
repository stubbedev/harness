package model

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/dialog"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/ui/util"
)

type blockingWorkspace struct {
	*countingWorkspace
	cfg       *config.Config
	release   chan struct{}
	updateErr error

	mu     sync.Mutex
	writes []string
}

func (w *blockingWorkspace) write(name string) {
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, name)
}

func (w *blockingWorkspace) recorded() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.writes...)
}

func (w *blockingWorkspace) Config() *config.Config { return w.cfg }

func (w *blockingWorkspace) UpdatePreferredModel(_ config.Scope, modelType config.SelectedModelType, _ config.SelectedModel) error {
	w.write("preferred:" + string(modelType))
	return w.updateErr
}

func (w *blockingWorkspace) SetConfigField(_ config.Scope, key string, _ any) error {
	w.write("field:" + key)
	return nil
}

func (w *blockingWorkspace) GetDefaultSmallModel(providerID string) (config.SelectedModel, error) {
	return config.SelectedModel{Provider: providerID, Model: "small"}, nil
}

func (w *blockingWorkspace) UpdateAgentModel(context.Context) error {
	w.write("update-agent")
	return nil
}

func (w *blockingWorkspace) InitCoderAgent(context.Context) error {
	w.write("init-agent")
	return nil
}

func (w *blockingWorkspace) ImportCopilot() (*oauth.Token, bool) {
	w.write("import-copilot")
	return nil, false
}

func (w *blockingWorkspace) CreateSession(context.Context, string) (session.Session, error) {
	w.write("create-session")
	return session.Session{ID: "created"}, nil
}

func (w *blockingWorkspace) AgentSetGoal(_ context.Context, sessionID, _ string) error {
	w.write("goal:" + sessionID)
	return nil
}

func newModelSelectUI(t *testing.T, ready bool) (*UI, *blockingWorkspace) {
	t.Helper()
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("p", config.ProviderConfig{
		ID:     "p",
		Models: []catalog.Model{{ID: "m1", Name: "Model One"}, {ID: "m2", Name: "Model Two"}},
	})
	ws := &blockingWorkspace{
		countingWorkspace: &countingWorkspace{ready: ready},
		release:           make(chan struct{}),
		cfg: &config.Config{
			Providers: providers,
			Models: map[config.SelectedModelType]config.SelectedModel{
				config.SelectedModelTypeLarge: {Provider: "p", Model: "m1"},
			},
			Agents:  map[string]config.Agent{config.AgentCoder: {Model: config.SelectedModelTypeLarge}},
			Options: &config.Options{},
		},
	}
	m := newBusyUI(ws.countingWorkspace)
	m.com = common.DefaultCommon(ws)
	m.themeKey = styles.ThemeKeyForProvider("p")
	warmCaches(m, false)
	return m, ws
}

func returnsPromptly(t *testing.T, f func() tea.Cmd) tea.Cmd {
	t.Helper()
	done := make(chan tea.Cmd, 1)
	go func() { done <- f() }()
	select {
	case cmd := <-done:
		return cmd
	case <-time.After(5 * time.Second):
		t.Fatal("Update blocked on a workspace write")
		return nil
	}
}

func drainMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeFor[tea.Cmd]() {
		var out []tea.Msg
		for i := range v.Len() {
			out = append(out, drainMsgs(v.Index(i).Interface().(tea.Cmd))...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func findMsg[T tea.Msg](t *testing.T, msgs []tea.Msg) T {
	t.Helper()
	for _, msg := range msgs {
		if found, ok := msg.(T); ok {
			return found
		}
	}
	var zero T
	require.Failf(t, "message not produced", "%T not in %#v", zero, msgs)
	return zero
}

func infoOf(t *testing.T, cmd tea.Cmd) util.InfoMsg {
	t.Helper()
	require.NotNil(t, cmd)
	info, ok := cmd().(util.InfoMsg)
	require.True(t, ok)
	return info
}

func TestModelSelectionDoesNotBlockUpdate(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, false)

	cmd := returnsPromptly(t, func() tea.Cmd {
		return m.handleAction(dialog.ActionSelectModel{
			Model:     config.SelectedModel{Provider: "p", Model: "m2"},
			ModelType: config.SelectedModelTypeLarge,
		})
	})
	require.Empty(t, ws.recorded())

	close(ws.release)
	msgs := drainMsgs(cmd)
	selected := findMsg[modelSelectedMsg](t, msgs)
	findMsg[agentModelChangedMsg](t, msgs)
	require.True(t, selected.saved)
	require.NoError(t, selected.err)
	require.Equal(t, []string{"preferred:large", "preferred:small", "init-agent"}, ws.recorded())
}

func TestCopilotImportDoesNotBlockUpdate(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	copilot := string(catalog.InferenceProviderCopilot)

	cmd := returnsPromptly(t, func() tea.Cmd {
		return m.handleAction(dialog.ActionSelectModel{
			Model:     config.SelectedModel{Provider: copilot, Model: "gpt"},
			ModelType: config.SelectedModelTypeLarge,
		})
	})
	require.Empty(t, ws.recorded())

	close(ws.release)
	imported := findMsg[copilotImportedMsg](t, drainMsgs(cmd))
	require.Equal(t, copilot, imported.action.Model.Provider)
	require.Equal(t, []string{"import-copilot"}, ws.recorded())
}

func TestModelSelectedMsgAppliesConsequences(t *testing.T) {
	t.Parallel()
	m, _ := newModelSelectUI(t, true)
	m.dialog.OpenDialog(stubDialog{id: dialog.ModelsID})

	_, cmd := m.Update(modelSelectedMsg{
		action: dialog.ActionSelectModel{Model: config.SelectedModel{Provider: "p", Model: "m2"}, ModelType: config.SelectedModelTypeLarge},
		saved:  true,
	})

	require.False(t, m.dialog.ContainsDialog(dialog.ModelsID))
	info := findMsg[util.InfoMsg](t, drainMsgs(cmd))
	require.Equal(t, util.InfoTypeInfo, info.Type)
	require.Equal(t, "Large model changed to Model Two", info.Msg)
}

func TestModelSelectedMsgReportsFailedWrite(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	ws.updateErr = errUnreachable
	close(ws.release)
	m.dialog.OpenDialog(stubDialog{id: dialog.ModelsID})

	selected := findMsg[modelSelectedMsg](t, drainMsgs(m.handleSelectModel(dialog.ActionSelectModel{
		Model:     config.SelectedModel{Provider: "p", Model: "m2"},
		ModelType: config.SelectedModelTypeLarge,
	})))
	require.False(t, selected.saved)
	require.Equal(t, []string{"preferred:large"}, ws.recorded())

	_, cmd := m.Update(selected)
	info := findMsg[util.InfoMsg](t, drainMsgs(cmd))
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Contains(t, info.Msg, errUnreachable.Error())
	require.True(t, m.dialog.ContainsDialog(dialog.ModelsID), "a failed selection keeps the picker open")
}

func TestReasoningEffortDoesNotBlockUpdate(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	m.dialog.OpenDialog(stubDialog{id: dialog.ReasoningID})

	cmd := returnsPromptly(t, func() tea.Cmd {
		return m.handleAction(dialog.ActionSelectReasoningEffort{Effort: "low"})
	})
	require.Empty(t, ws.recorded())
	require.True(t, m.dialog.ContainsDialog(dialog.ReasoningID), "the dialog closes when the write lands")

	close(ws.release)
	saved := findMsg[reasoningEffortSavedMsg](t, drainMsgs(cmd))
	require.True(t, saved.saved)
	require.NoError(t, saved.err)
	require.Equal(t, []string{"preferred:large", "field:reasoning_efforts", "update-agent"}, ws.recorded())

	_, next := m.Update(saved)
	require.False(t, m.dialog.ContainsDialog(dialog.ReasoningID))
	info := findMsg[util.InfoMsg](t, drainMsgs(next))
	require.Equal(t, "Reasoning effort set to low", info.Msg)
}

func TestReasoningEffortSavedMsgReportsError(t *testing.T) {
	t.Parallel()
	m, _ := newModelSelectUI(t, true)
	m.dialog.OpenDialog(stubDialog{id: dialog.ReasoningID})

	_, cmd := m.Update(reasoningEffortSavedMsg{effort: "low", err: errUnreachable})

	require.True(t, m.dialog.ContainsDialog(dialog.ReasoningID))
	info := findMsg[util.InfoMsg](t, drainMsgs(cmd))
	require.Equal(t, util.InfoTypeError, info.Type)
}

func TestSessionModelRestoreDoesNotBlockUpdate(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	msgs := []message.Message{{Role: message.Assistant, Provider: "p", Model: "m2"}}

	cmd := returnsPromptly(t, func() tea.Cmd { return m.restoreModelFromSession(msgs) })
	require.Empty(t, ws.recorded())

	close(ws.release)
	restored := findMsg[sessionModelRestoredMsg](t, drainMsgs(cmd))
	require.True(t, restored.saved)
	require.NoError(t, restored.err)
	require.Equal(t, config.SelectedModel{Provider: "p", Model: "m2"}, restored.model)
	require.Equal(t, []string{"preferred:large", "preferred:small", "update-agent"}, ws.recorded())
}

func TestSessionModelRestoredMsgReportsError(t *testing.T) {
	t.Parallel()
	m, _ := newModelSelectUI(t, true)

	_, cmd := m.Update(sessionModelRestoredMsg{
		model: config.SelectedModel{Provider: "p", Model: "m2"},
		saved: true,
		err:   errors.New("small model unavailable"),
	})

	info := findMsg[util.InfoMsg](t, drainMsgs(cmd))
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Contains(t, info.Msg, "small model unavailable")
}

func TestSetGoalDoesNotBlockUpdate(t *testing.T) {
	t.Parallel()
	m, ws := newModelSelectUI(t, true)
	m.session = nil

	cmd := returnsPromptly(t, func() tea.Cmd {
		return m.handleAction(dialog.ActionSetGoal{Args: commands.Args{Values: map[string]string{"condition": "tests pass"}}})
	})
	require.Empty(t, ws.recorded())
	require.Nil(t, m.session)

	close(ws.release)
	set := findMsg[goalSetMsg](t, drainMsgs(cmd))
	require.Equal(t, "created", set.sessionID)
	require.Equal(t, "tests pass", set.condition)
	require.Equal(t, []string{"create-session", "goal:created"}, ws.recorded())

	m.applyGoalSet(set)
	require.NotNil(t, m.session)
	require.Equal(t, "created", m.session.ID)
	require.Equal(t, uiChat, m.state)
}

func TestGoalSetMsgForDepartedSessionWarns(t *testing.T) {
	t.Parallel()
	m, _ := newModelSelectUI(t, true)

	info := infoOf(t, m.applyGoalSet(goalSetMsg{sessionID: "other", condition: "tests pass"}))

	require.Equal(t, util.InfoTypeWarn, info.Type)
	require.Equal(t, "s1", m.session.ID)
}
