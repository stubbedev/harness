package dialog

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// sessionEditWorkspace records the deletes and renames the sessions
// dialog issues.
type sessionEditWorkspace struct {
	sessionMouseWorkspace
	deleted []string
	renamed map[string]string
}

func (w *sessionEditWorkspace) DeleteSession(_ context.Context, id string) error {
	w.deleted = append(w.deleted, id)
	return nil
}

func (w *sessionEditWorkspace) RenameSession(_ context.Context, id, title string) error {
	w.renamed[id] = title
	return nil
}

// newFilteredSessionsDialog opens the sessions dialog over three
// sessions, newest first, and types a filter only the oldest matches.
func newFilteredSessionsDialog(t *testing.T) (*Session, *sessionEditWorkspace) {
	t.Helper()

	ws := &sessionEditWorkspace{
		sessions: []session.Session{
			{ID: "s3", Title: "Newest work"},
			{ID: "s2", Title: "Middle work"},
			{ID: "s1", Title: "Oldest work"},
		},
		renamed: map[string]string{},
	}
	sty := styles.CharmtonePantera()
	s, load := NewSessions(&common.Common{Workspace: ws, Styles: &sty}, "s3")
	loadDialog(t, s, load)

	typeKeys(s, "oldest")
	require.Len(t, s.list.FilteredItems(), 1, "the filter narrows the list")
	require.Equal(t, "s1", s.selectedSessionItem().ID())
	return s, ws
}

func typeKeys(d Dialog, text string) {
	for _, r := range text {
		d.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// runAction executes the command an ActionCmd carries.
func runAction(t *testing.T, action Action) {
	t.Helper()
	cmd, ok := action.(ActionCmd)
	require.True(t, ok, "the dialog returns a command")
	require.NotNil(t, cmd.Cmd)
	cmd.Cmd()
}

// TestSessionDeleteWhileFilteredDeletesMatch pins issue #86: entering
// delete mode while filtered used to swap the list back to every
// session but keep the selected index, so confirming deleted the
// newest session instead of the one the user filtered to.
func TestSessionDeleteWhileFilteredDeletesMatch(t *testing.T) {
	t.Parallel()

	s, ws := newFilteredSessionsDialog(t)

	pressDelete(t, s)
	require.Equal(t, sessionsModeDeleting, s.sessionsMode)
	require.Len(t, s.list.FilteredItems(), 1, "delete mode keeps the filter")
	require.Equal(t, "s1", s.selectedSessionItem().ID())

	runAction(t, s.HandleMsg(tea.KeyPressMsg{Code: 'y', Text: "y"}))
	require.Equal(t, []string{"s1"}, ws.deleted, "only the matched session is deleted")
	require.Equal(t, []session.Session{
		{ID: "s3", Title: "Newest work"},
		{ID: "s2", Title: "Middle work"},
	}, s.sessions)
	require.Equal(t, "oldest", s.input.Value())
	require.Empty(t, s.list.FilteredItems(), "the list keeps applying the filter shown in the input")
}

// TestSessionRenameWhileFilteredRenamesMatch pins the same bug class
// for rename mode: the session renamed is the one the user filtered to.
func TestSessionRenameWhileFilteredRenamesMatch(t *testing.T) {
	t.Parallel()

	s, ws := newFilteredSessionsDialog(t)

	s.HandleMsg(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	require.Equal(t, sessionsModeUpdating, s.sessionsMode)
	require.Equal(t, "s1", s.selectedSessionItem().ID())

	typeKeys(s, "Renamed")
	runAction(t, s.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
	require.Equal(t, map[string]string{"s1": "Renamed"}, ws.renamed)
}
