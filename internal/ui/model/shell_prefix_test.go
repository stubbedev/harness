package model

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/util"
)

func newReboundShellUI(t *testing.T, shellKeys ...string) *UI {
	t.Helper()
	m, _ := newBangUI()
	m.keyMap.ApplyKeybinds(map[string][]string{"editor.shell_mode": shellKeys})
	return m
}

func TestReboundShellModeKeyEntersShellMode(t *testing.T) {
	t.Parallel()

	m := newReboundShellUI(t, "$")
	typeText(m, "$ls")
	require.True(t, m.bangMode)
	require.Equal(t, "ls", m.textarea.Value())

	m = newReboundShellUI(t, "$")
	typeText(m, "!ls")
	require.False(t, m.bangMode, "the old key no longer enters shell mode")
	require.Equal(t, "!ls", m.textarea.Value())
}

func TestPasteRespectsReboundShellPrefix(t *testing.T) {
	t.Parallel()

	m := newReboundShellUI(t, "$")
	m.handlePasteMsg(tea.PasteMsg{Content: "  $git status"})
	require.True(t, m.bangMode)
	require.Equal(t, "git status", m.textarea.Value())

	m = newReboundShellUI(t, "$")
	m.handlePasteMsg(tea.PasteMsg{Content: "!git status"})
	require.False(t, m.bangMode)
	require.Equal(t, "!git status", m.textarea.Value())
}

func TestExternalEditorResultRespectsReboundShellPrefix(t *testing.T) {
	t.Parallel()

	m := newReboundShellUI(t, "$")
	m.Update(openEditorMsg{Text: "$make test"})
	require.True(t, m.bangMode)
	require.Equal(t, "make test", m.textarea.Value())

	m.Update(openEditorMsg{Text: "!not a command"})
	require.False(t, m.bangMode)
	require.Equal(t, "!not a command", m.textarea.Value())

	m = newReboundShellUI(t, "ctrl+x")
	m.Update(openEditorMsg{Text: "make test", Shell: true})
	require.True(t, m.bangMode, "shell mode survives the editor when no prefix can carry it")
	require.Equal(t, "make test", m.textarea.Value())
}

func TestHistoryRespectsReboundShellPrefix(t *testing.T) {
	t.Parallel()

	m := newReboundShellUI(t, "$")
	m.promptHistory.messages = []message.PromptHistoryEntry{
		{Text: "!literal prompt"},
		{Text: "ls", Shell: true},
	}

	typeText(m, "$ec")
	require.True(t, m.bangMode)

	require.True(t, m.historyPrev())
	require.False(t, m.bangMode)
	require.Equal(t, "!literal prompt", m.textarea.Value())

	require.True(t, m.historyPrev())
	require.True(t, m.bangMode)
	require.Equal(t, "ls", m.textarea.Value())

	require.True(t, m.historyNext())
	require.True(t, m.historyNext())
	require.True(t, m.bangMode, "the shell-mode draft comes back in shell mode")
	require.Equal(t, "ec", m.textarea.Value())
}

func TestShellModeHintFollowsReboundKey(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	m.keyMap.ApplyKeybinds(map[string][]string{"editor.shell_mode": {"$"}})
	require.Equal(t, "$", helpKey(t, m.ShortHelp(), "shell mode"))

	m.keyMap.ApplyKeybinds(map[string][]string{"editor.shell_mode": {"ctrl+x"}})
	require.False(t, hasDesc(m.ShortHelp(), "shell mode"), "a key that cannot start the prompt is inert there")
}

func TestQuitWarningNamesQuitHelpKey(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.keyMap.Quit = key.NewBinding(key.WithKeys("ctrl+q"), key.WithHelp("^q", "quit"))

	batch, ok := m.quit()().(tea.BatchMsg)
	require.True(t, ok)
	info, ok := batch[0]().(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, "Press ^q again to quit", info.Msg)
}
