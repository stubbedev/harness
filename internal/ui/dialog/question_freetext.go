package dialog

import (
	"image"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// FreeText is an open-ended text input component for questions
// that need a narrative answer rather than a selection.
type FreeText struct {
	Styles  *styles.Styles
	Request question.Question
	focused bool

	editor       textarea.Model
	scrollOffset int  // lines scrolled past the top of the textarea viewport
	wheelActive  bool // wheel-scroll mode: skip cursor-follow until next key press
	keyEnter     key.Binding
	keyNewline   key.Binding
	keyClose     key.Binding

	// secret holds the raw answer for Secret questions; it is never
	// rendered, only masked length is shown.
	secret string

	lastResponse question.Answer
	lastWidth    int
}

// freeTextMinEditorHeight and freeTextMaxEditorHeight bound the
// answer textarea. It starts at the minimum and, when the form has
// taller sibling tabs, grows at draw time to fill the shared form
// height, capped at the maximum.
const (
	freeTextMinEditorHeight = 3
	freeTextMaxEditorHeight = 6
)

// NewFreeText creates a new free-text question component.
func NewFreeText(sty *styles.Styles, req question.Question) *FreeText {
	ta := newQuestionTextarea(sty, "Type your answer...", 1000)
	ta.DynamicHeight = false
	ta.MinHeight = freeTextMinEditorHeight
	ta.MaxHeight = freeTextMaxEditorHeight
	ta.SetHeight(freeTextMinEditorHeight)

	return &FreeText{
		Styles:     sty,
		Request:    req,
		editor:     ta,
		keyEnter:   keys.WithDesc(dialogKeys().Question.Confirm, "submit"),
		keyNewline: dialogKeys().Question.Newline,
		keyClose:   CloseKey(),
	}
}

// HandleKey processes a key press. Returns true when the user has
// submitted or dismissed the question.
func (d *FreeText) HandleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	d.wheelActive = false
	switch {
	case key.Matches(msg, d.keyClose):
		d.answer(question.Answer{QuestionID: d.Request.ID})
		return true, nil
	case key.Matches(msg, d.keyEnter):
		val := strings.TrimSpace(d.editor.Value())
		if d.Request.Secret {
			val = d.secret
		}
		if val != "" {
			d.answer(question.Answer{
				QuestionID: d.Request.ID,
				FillInText: val,
			})
			return true, nil
		}
		return false, nil
	case key.Matches(msg, d.keyNewline):
		if !d.Request.Secret {
			d.editor.InsertRune('\n')
		}
		return false, nil
	default:
		if d.Request.Secret {
			return false, d.handleSecretKey(msg)
		}
		var cmd tea.Cmd
		d.editor, cmd = d.editor.Update(msg)
		return false, cmd
	}
}

// handleSecretKey accumulates a masked single-line answer: printable
// runes append, backspace deletes. Everything else is ignored.
func (d *FreeText) handleSecretKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Code {
	case tea.KeyBackspace:
		if len(d.secret) > 0 {
			_, size := utf8.DecodeLastRuneInString(d.secret)
			d.secret = d.secret[:len(d.secret)-size]
		}
		return nil
	case tea.KeyDelete, tea.KeyLeft, tea.KeyRight, tea.KeyUp, tea.KeyDown,
		tea.KeyHome, tea.KeyEnd, tea.KeyPgUp, tea.KeyPgDown:
		return nil
	}
	if msg.Text != "" {
		d.secret += msg.Text
	}
	return nil
}

func (d *FreeText) answer(resp question.Answer) {
	d.lastResponse = resp
}

// Response returns the current answer, including any unsaved
// editor content so that tabbing away preserves typed text.
func (d *FreeText) Response() question.Answer {
	if d.Request.Secret {
		if d.secret != "" {
			return question.Answer{QuestionID: d.Request.ID, FillInText: d.secret}
		}
		return d.lastResponse
	}
	if val := strings.TrimSpace(d.editor.Value()); val != "" {
		return question.Answer{QuestionID: d.Request.ID, FillInText: val}
	}
	return d.lastResponse
}

// GetRequest returns the underlying question request.
func (d *FreeText) GetRequest() question.Question { return d.Request }

// ShortHelp returns key bindings for the status bar.
func (d *FreeText) ShortHelp() []key.Binding {
	return []key.Binding{d.keyEnter, d.keyNewline, d.keyClose}
}

// Height returns the visual height at the default max width.
func (d *FreeText) Height(width int) int {
	w := width
	if w <= 0 {
		w = d.lastWidth
	}
	if w <= 0 {
		w = choiceListMaxWidth
	}
	h := len(questionHeaderLines(d.Styles, d.focused, d.Request.Text, d.Request.Description, w))
	h += freeTextMinEditorHeight // textarea (minimum; grows to fill at draw time)
	if d.Request.Secret {
		h = h - freeTextMinEditorHeight + 1 // single masked line
	}
	h++ // trailing blank for bottom padding
	return h
}

// Draw renders the free-text question directly to screen.
// Returns the cursor position, or nil.
//
// The question header, description, and textarea share a single
// scroll buffer so the title scrolls away as the answer grows,
// matching the choice and confirm components. This keeps the
// textarea from being permanently squeezed by fixed header rows.
func (d *FreeText) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	d.lastWidth = area.Dx()
	viewport := area.Dy()

	barActive := d.Styles.Editor.QuestionCursorBar.Render("┃ ")
	const barInactive = "  "
	bar := barInactive
	if d.focused {
		bar = barActive
	}
	prefixWidth := lipgloss.Width(bar)

	// ftLine is a single buffer row. cursorX >= 0 marks the row
	// carrying the textarea cursor and its column (incl. prefix).
	type ftLine struct {
		text    string
		cursorX int
	}

	// build renders the full content (header, description, textarea)
	// into a flat line buffer at the given content width. Returns
	// the buffer and the row index of the cursor (-1 if none).
	build := func(contentWidth int) ([]ftLine, int) {
		var lines []ftLine
		cursorRow := -1

		for _, l := range questionHeaderLines(d.Styles, d.focused, d.Request.Text, d.Request.Description, contentWidth) {
			lines = append(lines, ftLine{text: l, cursorX: -1})
		}

		// Grow the textarea to fill the form height, bounded by the
		// min and max editor heights.
		headerLines := len(lines)
		fill := viewport - headerLines - 1 // -1 for trailing padding
		available := min(freeTextMaxEditorHeight, max(freeTextMinEditorHeight, fill))
		d.editor.SetHeight(available)
		d.editor.SetWidth(contentWidth - 2 - prefixWidth)
		tc := d.editor.Cursor()
		editorLines := strings.Split(d.editor.View(), "\n")
		if d.Request.Secret {
			// Masked single line; the cursor sits at the end.
			editorLines = []string{strings.Repeat("*", len([]rune(d.secret)))}
			tc = &tea.Cursor{X: len(editorLines[0]), Y: 0, Shape: tea.CursorBar, Blink: true}
		}
		for j, ln := range editorLines {
			text := bar + ln
			cursorX := -1
			if tc != nil && tc.Y == j {
				cursorRow = len(lines)
				cursorX = tc.X + prefixWidth
			}
			lines = append(lines, ftLine{text: text, cursorX: cursorX})
		}
		lines = append(lines, ftLine{cursorX: -1}) // trailing bottom padding, matches Height()
		return lines, cursorRow
	}

	// Build at full width; reserve a scrollbar column and rebuild
	// only if the content overflows the viewport.
	contentWidth := area.Dx()
	lines, cursorRow := build(contentWidth)
	overflow := viewport > 0 && len(lines) > viewport
	if overflow {
		contentWidth--
		lines, cursorRow = build(contentWidth)
	}

	// Clamp scroll, then keep the cursor row visible unless the
	// user is wheel-scrolling.
	d.scrollOffset = ClampScroll(d.scrollOffset, len(lines), viewport)
	if !d.wheelActive && cursorRow >= 0 {
		if cursorRow < d.scrollOffset {
			d.scrollOffset = cursorRow
		} else if cursorRow >= d.scrollOffset+viewport {
			d.scrollOffset = cursorRow - viewport + 1
		}
		d.scrollOffset = ClampScroll(d.scrollOffset, len(lines), viewport)
	}

	// Blit the visible window and place the cursor. The cursor is
	// returned relative to the area's top-left, matching the
	// InlineEditor contract.
	var cur *tea.Cursor
	baseCursor := d.editor.Cursor()
	for screenRow := range viewport {
		idx := d.scrollOffset + screenRow
		if idx >= len(lines) {
			break
		}
		ln := lines[idx]
		y := area.Min.Y + screenRow
		drawStyledText(scr, image.Rect(area.Min.X, y, area.Min.X+contentWidth, y+1), ln.text)
		if ln.cursorX >= 0 && ln.cursorX < contentWidth && baseCursor != nil {
			c := *baseCursor
			c.X = ln.cursorX
			c.Y = screenRow
			cur = &c
		}
	}

	// Scrollbar.
	if overflow {
		sb := common.Scrollbar(d.Styles, viewport, len(lines), viewport, d.scrollOffset)
		if sb != "" {
			x := area.Max.X - 1
			uv.NewStyledString(sb).Draw(scr, image.Rect(x, area.Min.Y, x+1, area.Min.Y+viewport))
		}
	}

	return cur
}

// HeightChanged reports whether the textarea height changed.
func (d *FreeText) HeightChanged() bool { return false }

// SetFocused updates focus state.
func (d *FreeText) SetFocused(focused bool) {
	d.focused = focused
	if focused {
		d.editor.Focus()
	} else {
		d.editor.Blur()
	}
}

// SetHover is a no-op for free text questions.
func (d *FreeText) SetHover(x, y int) {}

// HandleMouseClick is a no-op for free text questions.
func (d *FreeText) HandleMouseClick(x, y int) (bool, bool) { return false, false }

// HandleWheel scrolls the textarea viewport.
func (d *FreeText) HandleWheel(deltaX, deltaY float64) {
	if deltaY != 0 {
		d.scrollOffset += int(deltaY)
		d.wheelActive = true
	}
}

// HandlePaste forwards paste events to the editor textarea.
func (d *FreeText) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	var cmd tea.Cmd
	d.editor, cmd = d.editor.Update(msg)
	return cmd
}
