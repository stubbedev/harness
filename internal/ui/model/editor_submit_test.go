package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// typeIntoEditor feeds text through the full Update routing the way a
// user types it, with the editor focused and a session active.
func typeIntoEditor(t *testing.T, m *UI, text string) {
	t.Helper()
	// Production always has the prompt focused when the user types; the
	// textarea drops keys while blurred.
	m.textarea.Focus()
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// TestEnterKeepsDraftWhenAgentNotReady pins the submit gate: a prompt
// typed while the agent is not accepting yet (still starting, or the
// server unreachable) is reported as an error but stays in the editor,
// so mashing enter during startup cannot throw the draft away.
func TestEnterKeepsDraftWhenAgentNotReady(t *testing.T) {
	t.Parallel()

	ws := &countingWorkspace{ready: false}
	m := newBusyUI(ws)
	typeIntoEditor(t, m, "hello")

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.NotNil(t, cmd, "the rejection is reported")
	require.Equal(t, "hello", m.textarea.Value(), "a rejected send keeps the draft")
}

// TestEnterClearsAndSendsWhenReady pins the happy path the gate wraps:
// with the agent accepting, enter clears the editor and dispatches the
// send.
func TestEnterClearsAndSendsWhenReady(t *testing.T) {
	t.Parallel()

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	typeIntoEditor(t, m, "hello")

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.NotNil(t, cmd, "enter dispatches the send")
	require.Empty(t, m.textarea.Value(), "an accepted send clears the editor")
}

// TestEnterWithEmptyEditorDoesNotProbe pins that an empty enter clears
// the editor without consulting the agent, so idling on the key never
// produces an error toast in any agent state.
func TestEnterWithEmptyEditorDoesNotProbe(t *testing.T) {
	t.Parallel()

	for _, ready := range []bool{true, false} {
		ws := &countingWorkspace{ready: ready}
		m := newBusyUI(ws)
		m.textarea.SetValue("   ")

		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		require.Empty(t, m.textarea.Value(), "an empty enter clears the editor")
		require.Zero(t, ws.readyCalls, "an empty enter never consults the agent")
	}
}
