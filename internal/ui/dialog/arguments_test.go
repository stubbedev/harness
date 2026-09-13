package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/workspace"
)

// argumentsWorkspace is the minimal workspace the arguments dialog needs.
type argumentsWorkspace struct {
	workspace.Workspace
}

// newTestArguments builds the dialog exactly the way the /compact flow does:
// the result action is the ActionCompact message the commands dialog emitted,
// carrying the /compact argument schema.
func newTestArguments() *Arguments {
	st := styles.CharmtonePantera()
	com := &common.Common{Styles: &st, Workspace: &argumentsWorkspace{}}
	return NewArguments(
		com,
		"Compact Session",
		"Optionally steer what the compacted summary keeps.",
		compactArguments,
		ActionCompact{SessionID: "s1", Arguments: compactArguments},
	)
}

// typeIntoArguments feeds a string to the dialog one key press at a time,
// the way a user filling the focus field would.
func typeIntoArguments(d *Arguments, s string) {
	for _, r := range s {
		d.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// TestCompactArgumentsSchema pins the /compact focus contract: one field,
// keyed "instructions", optional so an empty submit still compacts.
func TestCompactArgumentsSchema(t *testing.T) {
	t.Parallel()

	require.Len(t, compactArguments, 1)
	arg := compactArguments[0]
	require.Equal(t, "instructions", arg.ID)
	require.False(t, arg.Required)
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

	action := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	compact, ok := action.(ActionCompact)
	require.True(t, ok, "enter must submit the dialog, got %T", action)
	require.Equal(t, "s1", compact.SessionID)
	require.Equal(t, map[string]string{"instructions": "Focus on the auth refactor"}, compact.Args)
}

// TestArgumentsSubmitsEmptyFocus verifies that submitting without typing
// still returns the action (with an empty focus, not a nil Args map) so the
// session compacts with a general summary.
func TestArgumentsSubmitsEmptyFocus(t *testing.T) {
	t.Parallel()

	d := newTestArguments()

	action := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	compact, ok := action.(ActionCompact)
	require.True(t, ok, "enter must submit the dialog, got %T", action)
	require.NotNil(t, compact.Args)
	require.Equal(t, "", compact.Args["instructions"])
}

// TestArgumentsCancelCloses verifies esc reports a close instead of
// submitting, leaving the session untouched.
func TestArgumentsCancelCloses(t *testing.T) {
	t.Parallel()

	d := newTestArguments()
	typeIntoArguments(d, "a focus that must be dropped")

	require.IsType(t, ActionClose{}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}
