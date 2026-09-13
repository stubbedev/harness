package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// compactWorkspace gives the counting workspace stub a config, which the
// commands dialog reads while building its item list.
type compactWorkspace struct {
	*countingWorkspace
}

func (w *compactWorkspace) Config() *config.Config { return &config.Config{} }

// newCompactUI builds a UI with an idle agent and an active session, opens
// the command palette and selects the named command by filtering the list
// the way a user would.
func newCompactUI(t *testing.T) (*UI, *countingWorkspace) {
	t.Helper()

	ws := &countingWorkspace{}
	m := newBusyUI(ws)
	m.com = common.DefaultCommon(&compactWorkspace{countingWorkspace: ws})
	warmCaches(m, false)

	cmd := m.openCommandsDialog()
	runCmds(m, cmd)
	require.True(t, m.dialog.ContainsDialog(dialog.CommandsID), "command palette should be open")

	return m, ws
}

// selectCommand filters the command palette down to one entry and confirms
// it, exactly the key presses a user makes to run a slash command.
func selectCommand(t *testing.T, m *UI, name string) {
	t.Helper()

	for _, r := range name {
		_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		runCmds(m, cmd)
	}
	pressKey(t, m, tea.KeyEnter)
}

// pressKey sends a special key (enter, esc, ...) through the full Update
// routing and drains the resulting command tree.
func pressKey(t *testing.T, m *UI, code rune) {
	t.Helper()

	_, cmd := m.Update(tea.KeyPressMsg{Code: code})
	runCmds(m, cmd)
}

// TestCompactWithoutArgumentsOpensDialog verifies the first checklist item
// of the /compact flow: selecting /compact (no focus given) opens the
// arguments dialog instead of compacting immediately.
func TestCompactWithoutArgumentsOpensDialog(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	selectCommand(t, m, "compact")

	require.Equal(t, dialog.ArgumentsID, m.dialog.DialogLast().ID())
	require.Empty(t, ws.summarizeCalls, "opening the dialog must not compact")
}

// TestCompactDialogSubmitsFocus verifies that a typed focus survives the
// whole first link: the arguments dialog's submit, the UI's ActionCompact
// handler, and the AgentSummarize request the workspace receives.
func TestCompactDialogSubmitsFocus(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	selectCommand(t, m, "compact")
	for _, r := range "Focus on the auth refactor" {
		_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		runCmds(m, cmd)
	}
	pressKey(t, m, tea.KeyEnter)

	require.Equal(t, []summarizeCall{{sessionID: "s1", instructions: "Focus on the auth refactor"}}, ws.summarizeCalls)
	require.False(t, m.dialog.HasDialogs(), "submitting must close the dialog")
}

// TestCompactDialogSubmitsEmptyFocus verifies that submitting the dialog
// without typing still compacts, with a general (empty) summary.
func TestCompactDialogSubmitsEmptyFocus(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	selectCommand(t, m, "compact")
	pressKey(t, m, tea.KeyEnter)

	require.Equal(t, []summarizeCall{{sessionID: "s1", instructions: ""}}, ws.summarizeCalls)
	require.False(t, m.dialog.HasDialogs())
}

// TestCompactDialogCancelLeavesSessionAlone verifies cancelling the dialog:
// no compaction request, no dialog left open, no lingering busy state.
func TestCompactDialogCancelLeavesSessionAlone(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	selectCommand(t, m, "compact")
	for _, r := range "a focus that must be dropped" {
		_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		runCmds(m, cmd)
	}
	pressKey(t, m, tea.KeyEscape)

	require.Empty(t, ws.summarizeCalls, "cancelling must not compact")
	require.False(t, m.dialog.HasDialogs())
	require.False(t, m.isAgentBusy(), "cancelling must not leave pending state")
}

// TestSummarizeActionPassesEmptyFocus pins the non-/compact entry point:
// the summarize command never opens the arguments dialog and always asks
// for a general summary.
func TestSummarizeActionPassesEmptyFocus(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	selectCommand(t, m, "summarize")

	require.Equal(t, []summarizeCall{{sessionID: "s1", instructions: ""}}, ws.summarizeCalls)
	require.False(t, m.dialog.HasDialogs(), "summarize must not open the arguments dialog")
}
