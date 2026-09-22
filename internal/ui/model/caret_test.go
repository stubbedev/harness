package model

import (
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// drawCursor renders a frame and returns the cursor the UI reports to
// the terminal. The caret is visible exactly when this is non-nil.
func drawCursor(t *testing.T, m *UI) *tea.Cursor {
	t.Helper()
	scr := uv.NewScreenBuffer(m.width, m.height)
	return m.Draw(scr, image.Rect(0, 0, m.width, m.height))
}

// TestCaretAlwaysRendersWhileEditorFocused pins the caret-liveness
// contract: every path that focuses the editor, and every event that
// takes away what the editor was standing in for (an inline question
// form, the background tasks strip), leaves the pair (focus state,
// focused surface) consistent, so Draw keeps returning a cursor. The
// historic failure mode was a desynced pair: the caret silently
// vanished until the user unfocused and refocused the input.
func TestCaretAlwaysRendersWhileEditorFocused(t *testing.T) {
	newUI := func(t *testing.T) *UI {
		ui := newFrameTestUI(t)
		ui.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
		ui.state = uiChat
		ui.session = &session.Session{ID: "session", Title: "Caret"}
		ui.focus = uiFocusMain
		ui.textarea.Blur()
		require.Nil(t, drawCursor(t, ui), "chat focus draws no caret")
		return ui
	}

	t.Run("tab into editor", func(t *testing.T) {
		ui := newUI(t)
		ui.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		require.Equal(t, uiFocusEditor, ui.focus)
		require.True(t, ui.textarea.Focused())
		require.NotNil(t, drawCursor(t, ui))
	})

	t.Run("click into editor", func(t *testing.T) {
		ui := newUI(t)
		ui.Update(tea.MouseClickMsg{
			Button: tea.MouseLeft,
			X:      ui.layout.editor.Min.X,
			Y:      ui.layout.editor.Min.Y,
		})
		require.Equal(t, uiFocusEditor, ui.focus)
		require.True(t, ui.textarea.Focused())
		require.NotNil(t, drawCursor(t, ui))
	})

	t.Run("question form opens and dismisses", func(t *testing.T) {
		ui := newUI(t)
		ui.Update(pubsub.Event[question.Request]{Payload: question.Request{
			ID: "q1", Questions: []question.Question{{
				ID:   "q1.1",
				Type: question.TypeFreeText,
				Text: "Proceed?",
			}},
		}})
		_, ok := ui.activeInline.(*dialog.QuestionForm)
		require.True(t, ok, "question request should open the inline form")
		require.Equal(t, uiFocusEditor, ui.focus)

		ui.Update(pubsub.Event[question.Notification]{Payload: question.Notification{BatchID: "q1"}})
		require.Nil(t, ui.activeInline)
		require.Equal(t, uiFocusEditor, ui.focus)
		require.True(t, ui.textarea.Focused())
		require.NotNil(t, drawCursor(t, ui))
	})

	t.Run("focused task strip emptying falls back to editor", func(t *testing.T) {
		ui := newUI(t)
		_ = ui.upsertAgentTask(&message.Message{ID: "m1", Role: message.Assistant}, agentToolCall("a1"))
		require.NotEmpty(t, ui.agentTasks)

		ui.focusTasks()
		require.Equal(t, uiFocusTasks, ui.focus)

		ui.reapAgentTask("a1")
		require.Empty(t, ui.agentTasks)
		require.Equal(t, uiFocusEditor, ui.focus)
		require.True(t, ui.textarea.Focused())
		require.NotNil(t, drawCursor(t, ui))
	})
}
