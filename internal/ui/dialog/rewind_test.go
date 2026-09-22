package dialog

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/workspace"
)

// rewindWorkspace is the minimal workspace the rewind dialog needs: a
// fixed set of user turns and checkpoint rows.
type rewindWorkspace struct {
	workspace.Workspace
	messages    []message.Message
	checkpoints []checkpoints.Checkpoint
}

func (w *rewindWorkspace) ListUserMessages(context.Context, string) ([]message.Message, error) {
	return w.messages, nil
}

func (w *rewindWorkspace) ListCheckpoints(context.Context, string) ([]checkpoints.Checkpoint, error) {
	return w.checkpoints, nil
}

// newRewindForTest builds a rewind dialog over three user turns, newest
// first, of which only the newest carries a working-tree snapshot.
func newRewindForTest(t *testing.T) *Rewind {
	t.Helper()
	st := styles.CharmtonePantera()
	com := &common.Common{Styles: &st, Workspace: &rewindWorkspace{
		messages: []message.Message{
			{ID: "m3", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "fix the lexer"}}, CreatedAt: time.Now().Add(-time.Minute).Unix()},
			{ID: "m2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "add tests"}}, CreatedAt: time.Now().Add(-time.Hour).Unix()},
			{ID: "m1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello world"}}, CreatedAt: time.Now().Add(-24 * time.Hour).Unix()},
		},
		checkpoints: []checkpoints.Checkpoint{{MessageID: "m3"}},
	}}
	r, err := NewRewind(com, "s1")
	require.NoError(t, err)
	return r
}

// TestRewindTurnsArePickerRows pins the shared row surface: every turn
// is a PickerItem whose value is the turn payload, whose label is the
// prompt, and whose right label carries the age and snapshot state.
func TestRewindTurnsArePickerRows(t *testing.T) {
	t.Parallel()

	r := newRewindForTest(t)
	rows := r.list.FilteredItems()
	require.Len(t, rows, 3)

	first := rows[0].(PickerItem)
	require.Equal(t, "fix the lexer", first.Label())
	turn, ok := first.Value().(RewindTurn)
	require.True(t, ok)
	require.Equal(t, "m3", turn.MessageID)
	require.True(t, turn.HasFiles)
	require.NotContains(t, first.RightLabel(), "no files", "the snapshotted turn hides the no-files note")

	third := rows[2].(PickerItem)
	require.Equal(t, "hello world", third.Label())
	require.Contains(t, third.RightLabel(), "no files", "an unsnapshotted turn flags it on the right")
}

// TestRewindFilterSelectsThroughValue pins the bug class the conversion
// removes: after filtering, confirming resolves the turn through the
// selected row's value, not a positional index into the turns.
func TestRewindFilterSelectsThroughValue(t *testing.T) {
	t.Parallel()

	r := newRewindForTest(t)

	// Type a filter only the second-newest turn matches.
	for _, c := range "add" {
		r.HandleMsg(tea.KeyPressMsg{Code: c, Text: string(c)})
	}
	require.Equal(t, "add", r.input.Value())
	require.Len(t, r.list.FilteredItems(), 1, "the filter narrows the list")

	r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, rewindPhaseMode, r.phase)
	require.Equal(t, "m2", r.selected.MessageID, "selection resolves through the row's value, not the pre-filter index")
}

// TestRewindModeConfirmEmitsAction pins the second phase: the mode rows
// are picker rows over the choice payloads, and confirming one emits
// the rewind action for the selected turn's mode.
func TestRewindModeConfirmEmitsAction(t *testing.T) {
	t.Parallel()

	r := newRewindForTest(t)

	// The dialog opens with no selection; next selects the top row.
	r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})

	// The newest turn has a snapshot, so all three modes are offered.
	r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, rewindPhaseMode, r.phase)

	rows := r.list.FilteredItems()
	require.Len(t, rows, 3)
	require.Equal(t, "Conversation and files", rows[0].(PickerItem).Label())
	require.Equal(t, "delete turns and restore files", rows[0].(PickerItem).RightLabel())

	action, ok := r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}).(ActionRewindConfirmed)
	require.True(t, ok)
	require.Equal(t, checkpoints.ModeBoth, action.Mode)
	require.Equal(t, "m3", action.MessageID)
	require.Equal(t, "fix the lexer", action.Prompt)
}

// TestRewindModePhaseWithoutSnapshot pins the degraded mode list: a
// turn without a snapshot offers conversation-only, and confirming it
// restores nothing on disk.
func TestRewindModePhaseWithoutSnapshot(t *testing.T) {
	t.Parallel()

	r := newRewindForTest(t)

	// Walk to the unsnapshotted oldest turn, newest first.
	r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, rewindPhaseMode, r.phase)

	rows := r.list.FilteredItems()
	require.Len(t, rows, 1)
	require.Equal(t, "Conversation only", rows[0].(PickerItem).Label())

	action, ok := r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}).(ActionRewindConfirmed)
	require.True(t, ok)
	require.Equal(t, checkpoints.ModeConversation, action.Mode)
	require.Equal(t, "m1", action.MessageID)
}

// TestRewindModePhaseBackRestoresTurns pins the phase switching: back
// from the mode picker rebuilds the turn rows as picker rows again.
func TestRewindModePhaseBackRestoresTurns(t *testing.T) {
	t.Parallel()

	r := newRewindForTest(t)
	r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, rewindPhaseMode, r.phase)

	r.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Equal(t, rewindPhaseTurns, r.phase)
	require.Len(t, r.list.FilteredItems(), 3)
	require.Equal(t, "fix the lexer", r.list.FilteredItems()[0].(PickerItem).Label())
}
