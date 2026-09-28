package model

import (
	"image"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// drawCursor renders a frame and returns the cursor the UI reports to
// the terminal: the protocol cursor a dialog or an inline editor
// places. The editor's own caret is not one of these — it is drawn as
// part of the frame (see caretCell).
func drawCursor(t *testing.T, m *UI) *tea.Cursor {
	t.Helper()
	scr := uv.NewScreenBuffer(m.width, m.height)
	return m.Draw(scr, image.Rect(0, 0, m.width, m.height))
}

// caretCell renders a frame and returns the position of the editor's
// drawn caret cell. The textarea draws its caret as a reverse-video
// cell inside the frame, so the caret is visible exactly when one such
// cell exists in the editor area.
func caretCell(t *testing.T, m *UI) (image.Point, bool) {
	t.Helper()
	scr := uv.NewScreenBuffer(m.width, m.height)
	m.Draw(scr, image.Rect(0, 0, m.width, m.height))
	for y := m.layout.editor.Min.Y; y < m.layout.editor.Max.Y; y++ {
		for x := m.layout.editor.Min.X; x < m.layout.editor.Max.X; x++ {
			if cell := scr.CellAt(x, y); cell != nil && cell.Style.Attrs&uv.AttrReverse != 0 {
				return image.Pt(x, y), true
			}
		}
	}
	return image.Point{}, false
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
// the editor area's top and bottom rows, with the textarea strictly
// between them - all derived from the same helpers the draw path uses.
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
	require.Contains(t, lines[ui.editorContentOrigin().Y], "┃", "the prompt renders on the textarea's first row, inside the frame")
}

// TestCaretSitsOnTheTextareaRow pins the caret to the field itself: the
// drawn caret cell is on the editor area's first content row, directly
// below the top rule, on the row the prompt glyph occupies.
func TestCaretSitsOnTheTextareaRow(t *testing.T) {
	ui := newFrameTestUI(t)
	ui.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
	ui.state = uiChat
	ui.session = &session.Session{ID: "s1"}
	ui.focus = uiFocusEditor

	at, ok := caretCell(t, ui)
	require.True(t, ok, "a focused editor draws its caret")
	require.Equal(t, ui.editorContentOrigin().Y, at.Y, "the caret sits on the field's first row")
	require.GreaterOrEqual(t, at.X, ui.editorContentOrigin().X, "the caret sits inside the field")
}

// TestCaretAlwaysRendersWhileEditorFocused pins the caret-liveness
// contract: every path that focuses the editor, and every event that
// takes away what the editor was standing in for (an inline question
// form, the background tasks strip), leaves the pair (focus state,
// focused surface) consistent, so the drawn caret stays in the frame.
// The historic failure mode was a desynced pair: the caret silently
// vanished until the user unfocused and refocused the input.
func TestCaretAlwaysRendersWhileEditorFocused(t *testing.T) {
	newUI := func(t *testing.T) *UI {
		ui := newFrameTestUI(t)
		ui.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
		ui.state = uiChat
		ui.session = &session.Session{ID: "session", Title: "Caret"}
		ui.focus = uiFocusMain
		ui.textarea.Blur()
		_, ok := caretCell(t, ui)
		require.False(t, ok, "chat focus draws no caret")
		return ui
	}

	t.Run("tab into editor", func(t *testing.T) {
		ui := newUI(t)
		ui.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		require.Equal(t, uiFocusEditor, ui.focus)
		require.True(t, ui.textarea.Focused())
		_, ok := caretCell(t, ui)
		require.True(t, ok, "the focused editor draws its caret")
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
		_, ok := caretCell(t, ui)
		require.True(t, ok, "the focused editor draws its caret")
	})

	t.Run("subagent toolbox key leaves the toolbox", func(t *testing.T) {
		ui := newUI(t)
		_ = ui.upsertAgentTask(&message.Message{ID: "m1", Role: message.Assistant}, agentToolCall("a1"))
		require.NotEmpty(t, ui.agentTasks)

		ui.focusTasks()
		ui.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		require.Equal(t, uiFocusEditor, ui.focus)
		require.True(t, ui.textarea.Focused(), "leaving a non-empty strip through esc must refocus the textarea")
		_, ok := caretCell(t, ui)
		require.True(t, ok, "the focused editor draws its caret")
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
		require.NotNil(t, drawCursor(t, ui), "the inline form places its own caret")

		ui.Update(pubsub.Event[question.Notification]{Payload: question.Notification{BatchID: "q1"}})
		require.Nil(t, ui.activeInline)
		require.Equal(t, uiFocusEditor, ui.focus)
		require.True(t, ui.textarea.Focused())
		_, ok = caretCell(t, ui)
		require.True(t, ok, "the focused editor draws its caret")
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
		_, ok := caretCell(t, ui)
		require.True(t, ok, "the focused editor draws its caret")
	})
}

// TestCaretBlinkTicksReachTheTextarea pins the blink plumbing: the
// textarea arms a blink tick on caret activity, and a cursor.BlinkMsg
// fed through Update reaches the textarea, whose cursor toggles the
// drawn caret's phase. The centralized model is the only place that can
// hand the tick back, so dropping the routing freezes the caret in
// whatever phase it was in.
func TestCaretBlinkTicksReachTheTextarea(t *testing.T) {
	ui := newFrameTestUI(t)
	ui.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
	ui.state = uiChat
	ui.session = &session.Session{ID: "s1"}
	ui.focus = uiFocusEditor

	// Shorten the blink so the tick command completes instantly; the
	// production speed stays untouched.
	taStyles := ui.com.Styles.Editor.Textarea
	taStyles.Cursor.BlinkSpeed = time.Millisecond
	ui.textarea.SetStyles(taStyles)

	_, ok := caretCell(t, ui)
	require.True(t, ok, "a focused editor draws its caret")

	// Arm the tick the way a keystroke does, through the UI's own
	// textarea update so the armed state lands on the model. The tick
	// rides the returned batch, as it does on the real key path.
	msg := textareaTick(t, ui.updateTextarea(tea.KeyPressMsg{Code: 'x', Text: "x"}))
	require.IsType(t, cursor.BlinkMsg{}, msg)

	ui.Update(msg)

	_, ok = caretCell(t, ui)
	require.False(t, ok, "the routed blink tick must toggle the drawn caret off")
}

// textareaTick executes a command the way the runtime would and returns
// the first non-nil message it yields (the blink tick). Batch compacts
// to the single command when nothing else rides along, so both shapes
// are accepted.
func textareaTick(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	require.NotNil(t, cmd, "caret movement must arm a blink tick")
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if msg := c(); msg != nil {
				return msg
			}
		}
		t.Fatal("the batch carried no blink tick")
	}
	require.NotNil(t, msg)
	return msg
}
