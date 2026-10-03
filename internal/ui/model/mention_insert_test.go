package model

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/completions"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// mentionWorkspace is countingWorkspace with a usable config, which the
// mention picker needs for its completion limits.
type mentionWorkspace struct {
	*countingWorkspace
}

func (w *mentionWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{}}
}

// newMentionUI builds a focused chat model whose editor behaves the way
// production builds it, over a workspace with a non-nil config.
func newMentionUI() (*UI, *mentionWorkspace) {
	ws := &mentionWorkspace{countingWorkspace: &countingWorkspace{ready: true}}
	com := common.DefaultCommon(ws)
	m := &UI{
		com:      com,
		status:   NewStatus(com, nil),
		chat:     NewChat(com, config.ScrollbarDefault),
		textarea: textarea.New(),
		state:    uiChat,
		focus:    uiFocusEditor,
		width:    140,
		height:   45,
		session:  &session.Session{ID: "s1"},
		keyMap:   DefaultKeyMap(),
		dialog:   dialog.NewOverlay(),
	}
	m.textarea.Focus()
	return m, ws
}

// cursorOffset returns the cursor's absolute rune offset in the editor.
func cursorOffset(m *UI) int {
	return absoluteOffset(m.textarea.Value(), m.textarea.Line(), m.textarea.Column())
}

// typeAtCursor sends the given runes through the real key path.
func typeAtCursor(m *UI, text string) {
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestMentionPickerAnchorsAtCursor(t *testing.T) {
	t.Parallel()

	m, _ := newMentionUI()
	m.textarea.SetValue("hello world")
	m.moveCursorToOffset(6)

	typeAtCursor(m, "@")

	require.True(t, m.dialog.ContainsDialog(dialog.MentionPickerID), "typing @ mid-text opens the picker")
	require.Equal(t, 6, m.completionsStartIndex, "the picker anchors at the cursor, not the end of the text")
	require.Equal(t, "hello @world", m.textarea.Value(), "the typed @ lands at the cursor")
}

func TestMentionInsertLandsAtCursor(t *testing.T) {
	t.Parallel()

	m, _ := newMentionUI()
	m.textarea.SetValue("hello world")
	m.moveCursorToOffset(6)
	typeAtCursor(m, "@")

	m.handleAction(dialog.ActionMentionSelected{Value: completions.FileCompletionValue{Path: "main.go"}})

	require.Equal(t, "hello main.go world", m.textarea.Value(), "the picked file replaces the @ where it was typed")
	require.Equal(t, 14, cursorOffset(m), "the cursor resumes right after the inserted mention")
	require.False(t, m.dialog.ContainsDialog(dialog.MentionPickerID), "the picker closes on selection")
}

func TestMentionInsertAtPromptStart(t *testing.T) {
	t.Parallel()

	m, _ := newMentionUI()
	typeAtCursor(m, "@")

	require.True(t, m.dialog.ContainsDialog(dialog.MentionPickerID), "typing @ on an empty prompt opens the picker")

	m.handleAction(dialog.ActionMentionSelected{Value: completions.FileCompletionValue{Path: "main.go"}})

	require.Equal(t, "main.go ", m.textarea.Value(), "the picked file lands at the start of an empty prompt")
	require.Equal(t, 8, cursorOffset(m), "the cursor resumes at the end of the prompt")
}

func TestMentionCancelKeepsAt(t *testing.T) {
	t.Parallel()

	m, _ := newMentionUI()
	m.textarea.SetValue("hi ")
	typeAtCursor(m, "@")

	require.True(t, m.dialog.ContainsDialog(dialog.MentionPickerID), "typing @ after whitespace opens the picker")

	m.handleAction(dialog.ActionMentionCancelled{})

	require.Equal(t, "hi @", m.textarea.Value(), "escape keeps the typed @ as ordinary text")
	require.False(t, m.dialog.ContainsDialog(dialog.MentionPickerID), "escape closes the picker")
	require.Equal(t, 0, m.completionsStartIndex, "the mention anchor resets on cancel")
}

func TestMentionCancelKeepsAtMidText(t *testing.T) {
	t.Parallel()

	m, _ := newMentionUI()
	m.textarea.SetValue("hello world")
	m.moveCursorToOffset(6)
	typeAtCursor(m, "@")
	m.handleAction(dialog.ActionMentionCancelled{})

	require.Equal(t, "hello @world", m.textarea.Value(), "a mid-text @ survives cancel where it was typed")
	require.Equal(t, 7, cursorOffset(m), "the cursor stays after the @")
}
