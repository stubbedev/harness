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

// TestPalettePutsArgumentTakingCommandInEditor pins the palette's pick of
// a command that takes arguments: it is put in the editor, followed by a
// space, for its arguments to be typed after it - nothing runs yet.
func TestPalettePutsArgumentTakingCommandInEditor(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	selectCommand(t, m, "compact")

	require.False(t, m.dialog.HasDialogs(), "the palette hands over to the editor")
	require.Equal(t, "/compact ", m.textarea.Value())
	require.Empty(t, ws.summarizeCalls, "picking must not compact")
}

// TestCompactInvocationSubmitsFocus pins the whole /compact chain: the text
// after the name reaches AgentSummarize as the focus.
func TestCompactInvocationSubmitsFocus(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	selectCommand(t, m, "compact")
	typeText(m, "Focus on the auth refactor")
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

// TestPickerCompletesPartialName pins the picker: enter on a partial name
// completes the selected command that takes arguments.
func TestPickerCompletesPartialName(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	typeText(m, "/compa")
	pressKey(t, m, tea.KeyEnter)

	require.Equal(t, "/compact ", m.textarea.Value())
	require.Empty(t, ws.summarizeCalls)
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

	m, ws := newCompactUI(t)

	typeText(m, "/summarize now please")
	pressKey(t, m, tea.KeyEnter)

	require.Empty(t, ws.summarizeCalls)
}

// TestSummarizeActionPassesEmptyFocus pins the non-/compact entry point:
// the summarize command takes nothing, runs on pick and always asks for a
// general summary.
func TestSummarizeActionPassesEmptyFocus(t *testing.T) {
	t.Parallel()

	m, ws := newCompactUI(t)

	selectCommand(t, m, "summarize")

	require.Equal(t, []summarizeCall{{sessionID: "s1", instructions: ""}}, ws.summarizeCalls)
	require.False(t, m.dialog.HasDialogs(), "summarize must not open the arguments dialog")
}
