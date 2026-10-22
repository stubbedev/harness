package model

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// renameWorkspace gives the counting workspace stub one session and
// records the renames the sessions dialog issues.
type renameWorkspace struct {
	*countingWorkspace
	renamed map[string]string
}

func (w *renameWorkspace) ListSessions(context.Context) ([]session.Session, error) {
	return []session.Session{{ID: "s1", Title: "Current title"}}, nil
}

func (w *renameWorkspace) RenameSession(_ context.Context, id, title string) error {
	w.renamed[id] = title
	return nil
}

// fakeClipboard puts clipboard readers on PATH that print content, so the
// text input's clipboard paste reads it whichever tool the clipboard
// package settled on.
func fakeClipboard(t *testing.T, content string) {
	t.Helper()

	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' '" + content + "'\n"
	for _, name := range []string{"xclip", "xsel", "wl-paste", "termux-clipboard-get", "pbpaste", "powershell.exe"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755))
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// leafMsgs runs cmd and every command a batch fans out to, returning the
// messages they produce.
func leafMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var msgs []tea.Msg
	for _, c := range batch {
		msgs = append(msgs, leafMsgs(c)...)
	}
	return msgs
}

// TestSessionRenameCtrlVPastesFromClipboard pins issue #89: ctrl+v in the
// session rename field asks the text input to paste, and handleAction used
// to send that command back as a message instead of running it, so the
// clipboard never reached the field.
func TestSessionRenameCtrlVPastesFromClipboard(t *testing.T) {
	fakeClipboard(t, "Pasted title")

	ws := &renameWorkspace{countingWorkspace: &countingWorkspace{}, renamed: map[string]string{}}
	m := newBusyUI(ws.countingWorkspace)
	m.com = common.DefaultCommon(ws)

	runCmds(m, m.openSessionsDialog())
	require.True(t, m.dialog.ContainsDialog(dialog.SessionsID), "sessions dialog should be open")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	runCmds(m, cmd)

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	msgs := leafMsgs(cmd)
	require.NotEmpty(t, msgs, "ctrl+v runs the input's paste command")
	for _, msg := range msgs {
		_, isCmd := msg.(tea.Cmd)
		require.False(t, isCmd, "handleAction sent a command back as a message instead of running it")
		if err, ok := msg.(error); ok {
			t.Skipf("No clipboard utility for the clipboard package to run: %v", err)
		}
		_, next := m.Update(msg)
		runCmds(m, next)
	}

	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runCmds(m, cmd)
	require.Equal(t, map[string]string{"s1": "Pasted title"}, ws.renamed)
}
