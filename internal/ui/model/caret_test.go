package model

import (
	"image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/attachments"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// drawCursor renders a frame and returns the cursor the UI reports to
// the terminal. The caret is visible exactly when this is non-nil.
func drawCursor(t *testing.T, m *UI) *tea.Cursor {
	t.Helper()
	scr := uv.NewScreenBuffer(m.width, m.height)
	return m.Draw(scr, image.Rect(0, 0, m.width, m.height))
}

// useAttachmentRenderer swaps in the real attachments renderer; the
// frame-test helper builds the component without one, so any test
// that draws with pills present needs it.
func useAttachmentRenderer(ui *UI) {
	sty := ui.com.Styles.Attachments
	ui.attachments = attachments.New(
		attachments.NewRenderer(sty.Normal, sty.Deleting, sty.Image, sty.Text, sty.Skill, sty.Remove),
		attachments.Keymap{},
	)
}

// drawScreen renders a frame and returns the flattened, ANSI-stripped
// screen text.
func drawScreen(t *testing.T, m *UI) string {
	t.Helper()
	scr := uv.NewScreenBuffer(m.width, m.height)
	m.Draw(scr, image.Rect(0, 0, m.width, m.height))
	return scr.String()
}

// TestEditorFrameWrapsContent pins the editor frame: a rule line on
// the editor area's top and bottom rows, with the content (textarea,
// or attachments strip then textarea) strictly between them - all
// derived from the same helpers the draw path uses.
func TestEditorFrameWrapsContent(t *testing.T) {
	t.Parallel()

	ui := newFrameTestUI(t)
	ui.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
	ui.state = uiChat
	ui.session = &session.Session{ID: "s1"}
	ui.focus = uiFocusEditor

	screen := ansi.Strip(drawScreen(t, ui))
	lines := strings.Split(screen, "\n")
	top := ui.layout.editor.Min.Y
	bottom := ui.layout.editor.Max.Y - 1

	require.Equal(t, strings.Repeat("─", ui.layout.editor.Dx()), strings.TrimRight(lines[top], " "),
		"the top frame row must be a full-width rule")
	require.Equal(t, strings.Repeat("─", ui.layout.editor.Dx()), strings.TrimRight(lines[bottom], " "),
		"the bottom frame row must be a full-width rule")
	require.Contains(t, lines[ui.textareaOrigin().Y], "┃", "the prompt renders on the textarea's first row, inside the frame")

	useAttachmentRenderer(ui)
	ui.attachments.Update(message.Attachment{FileName: "a.txt"})
	ui.updateLayoutAndSize()
	screen = ansi.Strip(drawScreen(t, ui))
	lines = strings.Split(screen, "\n")
	require.Equal(t, strings.Repeat("─", ui.layout.editor.Dx()), strings.TrimRight(lines[ui.layout.editor.Min.Y], " "))
	require.Contains(t, lines[ui.editorContentOrigin().Y], "a.txt", "the attachments strip renders below the top rule")
	require.Contains(t, lines[ui.textareaOrigin().Y], "┃", "the textarea renders below the strip, above the bottom rule")
}

// TestCaretSitsOnTheTextareaRow pins the caret to the field itself:
// on the editor area's top row with no attachments, and one row down
// when the attachments strip occupies that row. The cursor offset
// derives from the same predicate that reserves the strip's row in
// the layout, so it cannot drift onto the legend again.
func TestCaretSitsOnTheTextareaRow(t *testing.T) {
	ui := newFrameTestUI(t)
	ui.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
	ui.state = uiChat
	ui.session = &session.Session{ID: "s1"}
	ui.focus = uiFocusEditor
	useAttachmentRenderer(ui)

	cur := drawCursor(t, ui)
	require.NotNil(t, cur)
	require.Equal(t, ui.textareaOrigin().Y, cur.Y, "with no attachments the caret sits on the field's first row")

	ui.attachments.Update(message.Attachment{FileName: "a.txt"})
	ui.updateLayoutAndSize()
	cur = drawCursor(t, ui)
	require.NotNil(t, cur)
	require.Equal(t, ui.textareaOrigin().Y, cur.Y, "the attachments strip pushes the caret down with the field")
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
