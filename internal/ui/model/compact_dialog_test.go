package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/dialog"
	"github.com/stubbedev/harness/internal/ui/util"
)

// compactWorkspace gives the counting workspace stub a config, which the
// commands dialog reads while building its item list.
type compactWorkspace struct {
	*countingWorkspace
}

func (w *compactWorkspace) Config() *config.Config { return &config.Config{} }

// newCompactUI builds a UI with an idle agent and an active session, its
// editor focused and empty.
func newCompactUI(t *testing.T) (*UI, *countingWorkspace) {
	t.Helper()

	ws := &countingWorkspace{}
	m := newBusyUI(ws)
	m.com = common.DefaultCommon(&compactWorkspace{countingWorkspace: ws})
	// A real cursor: the virtual one blinks, and every blink is a timer
	// the command runner would sit through. The production editor is
	// virtual; these tests only exercise command routing, not the caret.
	m.textarea.SetVirtualCursor(false)
	m.textarea.Focus()
	warmCaches(m, false)
	return m, ws
}

// openPalette opens the command palette.
func openPalette(t *testing.T, m *UI) {
	t.Helper()

	runCmds(m, m.openCommandsDialog())
	require.True(t, m.dialog.ContainsDialog(dialog.CommandsID), "command palette should be open")
}

// typeText feeds s through the full Update routing one key press at a
// time, the way a user types it.
func typeText(m *UI, s string) {
	for _, r := range s {
		_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		runCmds(m, cmd)
	}
}

// selectCommand filters the command palette down to one entry and confirms
// it, exactly the key presses a user makes to pick a command.
func selectCommand(t *testing.T, m *UI, name string) {
	t.Helper()

	openPalette(t, m)
	typeText(m, name)
	pressKey(t, m, tea.KeyEnter)
}

// pressKey sends a special key (enter, esc, ...) through the full Update
// routing and drains the resulting command tree.
func pressKey(t *testing.T, m *UI, code rune) {
	t.Helper()

	_, cmd := m.Update(tea.KeyPressMsg{Code: code})
	runCmds(m, cmd)
}

func TestPaletteCompactRunsImmediately(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	selectCommand(t, m, "compact")

	require.False(t, m.dialog.HasDialogs())
	require.Empty(t, m.textarea.Value(), "an optional focus does not send compact to the editor")
	require.Equal(t, []summarizeCall{{sessionID: "s1", instructions: ""}}, ws.summarizeCalls)
}

func TestIdleActionsRefusedWhileBusy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		action dialog.IdleAction
		want   string
	}{
		{dialog.ActionNewSession{}, "Agent is busy, please wait before starting a new session..."},
		{dialog.ActionCompact{SessionID: "s1"}, "Agent is busy, please wait before compacting the session..."},
		{dialog.ActionSaveSummary{SessionID: "s1"}, "Agent is busy, please wait before saving the summary..."},
		{dialog.ActionExternalEditor{}, "Agent is busy, please wait before opening the editor..."},
		{dialog.ActionInitializeProject{}, "Agent is busy, please wait before initializing the project..."},
	}
	for _, tc := range cases {
		m, ws := newCompactUI(t)
		warmCaches(m, true)

		info := infoOf(t, m.handleAction(tc.action))

		require.Equal(t, util.InfoTypeWarn, info.Type)
		require.Equal(t, tc.want, info.Msg, "%T", tc.action)
		require.Empty(t, ws.summarizeCalls)
	}
}

func TestInvokedCompactRefusedWhileBusy(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)
	warmCaches(m, true)

	typeText(m, "/compact keep the notes")
	pressKey(t, m, tea.KeyEnter)

	require.Empty(t, ws.summarizeCalls)
}

func TestPaletteOpensArgumentsFormWhereEditorCannotInvoke(t *testing.T) {
	t.Parallel()

	cases := map[string]func(m *UI){
		"agent view": func(m *UI) { m.agentView.shown = "m1$$a1" },
		"shell mode": func(m *UI) { m.bangMode = true },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m, _ := newCompactUI(t)
			setup(m)

			selectCommand(t, m, "Set Goal")

			require.Equal(t, dialog.ArgumentsID, m.dialog.DialogLast().ID())
			require.False(t, m.dialog.ContainsDialog(dialog.CommandsID))
			require.Empty(t, m.textarea.Value(), "the invocation must not land in the editor")
			require.Nil(t, m.session.Goal)
		})
	}
}

func TestPaletteSetGoalInsertsInvocationInNormalState(t *testing.T) {
	t.Parallel()

	m, _ := newCompactUI(t)

	selectCommand(t, m, "Set Goal")

	require.False(t, m.dialog.HasDialogs())
	require.Equal(t, "/goal ", m.textarea.Value())
}

// TestCompactInvocationSubmitsFocus pins the whole /compact chain: the text
// after the name reaches AgentSummarize as the focus.
func TestCompactInvocationSubmitsFocus(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	typeText(m, "/compact Focus on the auth refactor")
	pressKey(t, m, tea.KeyEnter)

	require.Equal(t, []summarizeCall{{sessionID: "s1", instructions: "Focus on the auth refactor"}}, ws.summarizeCalls)
	require.Empty(t, m.textarea.Value(), "the invocation is consumed, not sent as a message")
}

// TestCompactTypedInFullRuns pins that a command name typed out in full
// runs on enter, even while the picker lists it: /compact takes an
// optional focus, so it compacts with a general summary.
func TestCompactTypedInFullRuns(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	typeText(m, "/compact")
	require.True(t, m.commandPicker.visible(), "a partial invocation opens the picker")
	pressKey(t, m, tea.KeyEnter)

	require.Equal(t, []summarizeCall{{sessionID: "s1", instructions: ""}}, ws.summarizeCalls)
	require.False(t, m.commandPicker.visible())
}

func TestPickerRunsPartialNameWithOptionalArgs(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	typeText(m, "/compa")
	pressKey(t, m, tea.KeyEnter)

	require.Empty(t, m.textarea.Value())
	require.Equal(t, []summarizeCall{{sessionID: "s1", instructions: ""}}, ws.summarizeCalls)
}

// TestPickerCompletesPartialName pins the picker: enter on a partial name
// completes the selected command that asks for arguments.
func TestPickerCompletesPartialName(t *testing.T) {
	t.Parallel()

	m, _ := newCompactUI(t)

	typeText(m, "/goa")
	pressKey(t, m, tea.KeyEnter)

	require.Equal(t, "/goal ", m.textarea.Value())
	require.Nil(t, m.session.Goal)
}

// TestMissingRequiredArgumentOpensForm pins the fallback: an invocation
// without a required argument opens the arguments form, and cancelling
// it does nothing.
func TestMissingRequiredArgumentOpensForm(t *testing.T) {
	t.Parallel()

	m, _ := newCompactUI(t)

	typeText(m, "/goal")
	pressKey(t, m, tea.KeyEnter)

	require.Equal(t, dialog.ArgumentsID, m.dialog.DialogLast().ID())
	pressKey(t, m, tea.KeyEscape)
	require.False(t, m.dialog.HasDialogs())
	require.Nil(t, m.session.Goal)
}

// TestUnknownInvocationIsAMessage pins that a line naming no command is
// an ordinary message, so a path like "/etc/hosts" is never swallowed.
func TestUnknownInvocationIsAMessage(t *testing.T) {
	t.Parallel()

	m, _ := newCompactUI(t)

	_, ok := m.editorInvocation("/etc/hosts is broken")
	require.False(t, ok)
	_, ok = m.editorInvocation("/no-such-command now")
	require.False(t, ok)
	inv, ok := m.editorInvocation(":compact keep the notes")
	require.True(t, ok, "every editor.commands key opens an invocation")
	require.Equal(t, "keep the notes", inv.Raw)
}

// TestArgumentsToCommandWithoutArgumentsWarn pins that text after a
// command that takes nothing is refused rather than dropped.
func TestArgumentsToCommandWithoutArgumentsWarn(t *testing.T) {
	t.Parallel()

	m, _ := newCompactUI(t)

	typeText(m, "/new-session now please")
	pressKey(t, m, tea.KeyEnter)

	require.NotNil(t, m.session)
	require.Equal(t, "s1", m.session.ID)
}

// TestSummarizeActionPassesEmptyFocus pins the /summarize alias: it is the
// compact entry, runs on pick and asks for a general summary.
func TestSummarizeActionPassesEmptyFocus(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	selectCommand(t, m, "summarize")

	require.Equal(t, []summarizeCall{{sessionID: "s1", instructions: ""}}, ws.summarizeCalls)
	require.False(t, m.dialog.HasDialogs(), "summarize must not open the arguments dialog")
}
