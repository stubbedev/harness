package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/workspace"
)

// argumentsWorkspace is the minimal workspace the arguments dialog needs.
type argumentsWorkspace struct {
	workspace.Workspace
}

// newTestArguments builds the form the /compact flow opens: the one laid
// out from ActionCompact's ArgSpec.
func newTestArguments() *Arguments {
	st := styles.CharmtonePantera()
	com := &common.Common{Styles: &st, Workspace: &argumentsWorkspace{}}
	return NewArguments(com, ActionCompact{SessionID: "s1"}, commands.Args{})
}

// typeIntoArguments feeds a string to the dialog one key press at a time,
// the way a user filling the focus field would.
func typeIntoArguments(d *Arguments, s string) {
	for _, r := range s {
		d.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// TestCompactArgumentsSchema pins the /compact focus contract: one field,
// keyed "focus", optional so an empty submit still compacts.
func TestCompactArgumentsSchema(t *testing.T) {
	t.Parallel()

	fields := ActionCompact{}.ArgSpec().Fields
	require.Len(t, fields, 1)
	require.Equal(t, "focus", fields[0].ID)
	require.False(t, fields[0].Required)
}

// TestArgumentsSubmitsFocusAsActionCompact verifies the first link of the
// /compact chain: pressing enter on the dialog must return the ActionCompact
// message it was opened with, now carrying the typed focus. Before this was
// wired up, the submit switch only knew custom commands and MCP prompts, so
// enter silently re-focused the input and the focus never left the dialog.
func TestArgumentsSubmitsFocusAsActionCompact(t *testing.T) {
	t.Parallel()

	d := newTestArguments()
	typeIntoArguments(d, "Focus on the auth refactor")

	compact := submittedCompact(t, d)
	require.Equal(t, "s1", compact.SessionID)
	require.Equal(t, "Focus on the auth refactor", compact.Args.Value("focus"))
}

// TestArgumentsSubmitsEmptyFocus verifies that submitting without typing
// still returns the action, with an empty focus, so the
// session compacts with a general summary.
func TestArgumentsSubmitsEmptyFocus(t *testing.T) {
	t.Parallel()

	d := newTestArguments()

	compact := submittedCompact(t, d)
	require.Equal(t, "", compact.Args.Value("focus"))
}

// TestArgumentsCancelCloses verifies esc reports a close instead of
// submitting, leaving the session untouched.
func TestArgumentsCancelCloses(t *testing.T) {
	t.Parallel()

	d := newTestArguments()
	typeIntoArguments(d, "a focus that must be dropped")

	require.IsType(t, ActionClose{}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}

// submittedCompact presses enter on d and returns the ActionCompact the
// submit runs.
func submittedCompact(t *testing.T, d *Arguments) ActionCompact {
	t.Helper()

	action := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	run, ok := action.(ActionRun)
	require.True(t, ok, "enter must submit the dialog, got %T", action)
	compact, ok := run.Action.(ActionCompact)
	require.True(t, ok, "the submit runs the action the form was opened for, got %T", run.Action)
	return compact
}
