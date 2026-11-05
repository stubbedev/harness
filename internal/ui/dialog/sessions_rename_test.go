package dialog

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// newRenamingSessionsDialog opens the sessions dialog over one session
// and enters rename mode on it.
func newRenamingSessionsDialog(t *testing.T) (*Session, *sessionEditWorkspace) {
	t.Helper()

	ws := &sessionEditWorkspace{
		sessions: []session.Session{{ID: "s1", Title: "Current title"}},
		renamed:  map[string]string{},
	}
	sty := styles.CharmtonePantera()
	s, load := NewSessions(&common.Common{Workspace: ws, Styles: &sty}, "s1")
	loadDialog(t, s, load)

	s.HandleMsg(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	require.Equal(t, sessionsModeUpdating, s.sessionsMode)
	return s, ws
}

// helpDescs returns the help descriptions of bindings, the text a hint
// row shows next to each key.
func helpDescs(bindings []key.Binding) []string {
	descs := make([]string, 0, len(bindings))
	for _, b := range bindings {
		descs = append(descs, b.Help().Desc)
	}
	return descs
}

// fullHelpDescs flattens FullHelp's columns into help descriptions.
func fullHelpDescs(s *Session) []string {
	var descs []string
	for _, column := range s.FullHelp() {
		descs = append(descs, helpDescs(column)...)
	}
	return descs
}

// TestSessionRenamePasteFillsField pins issue #89: a bracketed paste
// into the rename field used to be dropped because only key presses
// reached the rename input.
func TestSessionRenamePasteFillsField(t *testing.T) {
	t.Parallel()

	s, ws := newRenamingSessionsDialog(t)

	s.HandleMsg(tea.PasteMsg{Content: "Pasted title"})
	runAction(t, s.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
	require.Equal(t, map[string]string{"s1": "Pasted title"}, ws.renamed)
}

// TestSessionRenameInputCommandIsAnActionCmd pins issue #89: the rename
// input's command used to be returned as the bare Action, which the UI
// sent back as a message instead of running, so ctrl+v never pasted.
func TestSessionRenameInputCommandIsAnActionCmd(t *testing.T) {
	t.Parallel()

	s, _ := newRenamingSessionsDialog(t)

	action := s.HandleMsg(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	cmd, ok := action.(ActionCmd)
	require.True(t, ok, "the rename input's command is wrapped in an ActionCmd, got %T", action)
	require.NotNil(t, cmd.Cmd, "ctrl+v asks the input to paste from the clipboard")
}

// TestSessionRenameKeepTitleHintOnlyBeforeFirstKeystroke pins issue #89:
// the keep-title hint showed for the whole rename mode although the
// binding only applies before the first keystroke.
func TestSessionRenameKeepTitleHintOnlyBeforeFirstKeystroke(t *testing.T) {
	t.Parallel()

	s, _ := newRenamingSessionsDialog(t)
	keepDesc := s.keyMap.KeepTitle.Help().Desc

	require.Contains(t, helpDescs(s.ShortHelp()), keepDesc)
	require.Contains(t, fullHelpDescs(s), keepDesc)

	typeKeys(s, "x")
	require.NotContains(t, helpDescs(s.ShortHelp()), keepDesc)
	require.NotContains(t, fullHelpDescs(s), keepDesc)

	s.HandleMsg(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	require.Equal(t, "x ", s.selectedSessionItem().InputValue(), "after the first keystroke the keep-title key types")
}

// TestSessionRenameKeepTitleFollowsBinding pins issue #89: keep-title
// matched a literal space rather than its binding, so rebinding
// dialog.sessions.keep_title had no effect.
func TestSessionRenameKeepTitleFollowsBinding(t *testing.T) {
	// Not parallel: this installs the process keymap.
	t.Cleanup(func() { keys.Install(nil) })
	keys.Install(map[string][]string{"dialog.sessions.keep_title": {"ctrl+k"}})

	s, _ := newRenamingSessionsDialog(t)
	require.Contains(t, helpDescs(s.ShortHelp()), s.keyMap.KeepTitle.Help().Desc)
	require.Equal(t, "ctrl+k", s.keyMap.KeepTitle.Help().Key)

	s.HandleMsg(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	require.Equal(t, "Current title", s.selectedSessionItem().InputValue())
	require.NotContains(t, helpDescs(s.ShortHelp()), s.keyMap.KeepTitle.Help().Desc)

	s, _ = newRenamingSessionsDialog(t)
	s.HandleMsg(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	require.Equal(t, " ", s.selectedSessionItem().InputValue(), "space no longer keeps the title once rebound")
}
