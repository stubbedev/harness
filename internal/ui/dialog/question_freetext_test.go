package dialog

import (
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// drawRows renders a component into a screen buffer and returns the
// cursor it reported plus one string per rendered row.
func drawRows(t *testing.T, d ftDrawer, w, h int) (*tea.Cursor, []string) {
	t.Helper()
	scr := uv.NewScreenBuffer(w, h)
	cur := d.Draw(scr, image.Rect(0, 0, w, h))
	rows := make([]string, h)
	for y := range h {
		var row []byte
		for x := range w {
			c := scr.CellAt(x, y)
			if c == nil || c.IsZero() {
				row = append(row, ' ')
			} else {
				row = append(row, c.String()...)
			}
		}
		rows[y] = string(row)
	}
	return cur, rows
}

type ftDrawer interface {
	Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor
}

// The question textareas all draw bar + prompt + editor content, so
// the hardware cursor must land exactly on the cell after the typed
// text. The fill-in prompt used to place it one cell short.
func TestQuestionCursorPlacement(t *testing.T) {
	t.Parallel()
	s := styles.CharmtonePantera()

	t.Run("free text end of line", func(t *testing.T) {
		t.Parallel()
		d := NewFreeText(&s, question.Question{ID: "q1", Type: question.TypeFreeText, Text: "Describe the issue."})
		d.SetFocused(true)
		for _, r := range "hello" {
			d.HandleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		cur, rows := drawRows(t, d, 80, 12)
		require.NotNil(t, cur)
		require.Equal(t, questionBarCells+len("hello"), cur.X)
		require.Contains(t, rows[cur.Y], "hello")
	})

	t.Run("free text mid line", func(t *testing.T) {
		t.Parallel()
		d := NewFreeText(&s, question.Question{ID: "q1", Type: question.TypeFreeText, Text: "Describe the issue."})
		d.SetFocused(true)
		for _, r := range "hello" {
			d.HandleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		for range 2 {
			d.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
		}
		cur, _ := drawRows(t, d, 80, 12)
		require.NotNil(t, cur)
		require.Equal(t, questionBarCells+len("hel"), cur.X)
	})

	t.Run("free text through form", func(t *testing.T) {
		t.Parallel()
		f := NewQuestionForm(&s, question.Request{
			ID: "batch",
			Questions: []question.Question{
				{ID: "q1", Type: question.TypeFreeText, Text: "Describe the issue."},
			},
		})
		f.SetFocused(true)
		ft := f.questions[0].(*FreeText)
		for _, r := range "hello" {
			ft.editor.InsertRune(r)
		}
		cur, rows := drawRows(t, f, 80, 14)
		require.NotNil(t, cur)
		require.Equal(t, questionBarCells+len("hello"), cur.X)
		require.Contains(t, rows[cur.Y], "hello")
	})

	t.Run("single choice fill-in", func(t *testing.T) {
		t.Parallel()
		d := NewSingleChoice(&s, question.Question{
			ID:      "q1",
			Type:    question.TypeSingleChoice,
			Text:    "Pick one.",
			Choices: []question.Choice{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta"}},
		})
		d.SetFocused(true)
		d.cursorIdx = len(d.Request.Choices)
		d.fillIn.Focus()
		for _, r := range "custom" {
			d.fillIn.InsertRune(r)
		}
		cur, rows := drawRows(t, d, 80, 14)
		require.NotNil(t, cur)
		// The fill-in row draws bar + "❯ " prompt + content.
		require.Equal(t, questionBarCells+lipgloss.Width("❯ ")+len("custom"), cur.X)
		require.Contains(t, rows[cur.Y], "custom")
	})

	t.Run("secret masked end of mask", func(t *testing.T) {
		t.Parallel()
		d := NewFreeText(&s, question.Question{ID: "q1", Type: question.TypeFreeText, Text: "Enter it.", Secret: true})
		d.SetFocused(true)
		for _, r := range "hunter2" {
			d.HandleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		cur, rows := drawRows(t, d, 80, 12)
		require.NotNil(t, cur)
		require.Equal(t, tea.CursorBar, cur.Shape)
		require.Equal(t, questionBarCells+len("hunter2"), cur.X)
		require.Contains(t, rows[cur.Y], "*******")
	})
}
