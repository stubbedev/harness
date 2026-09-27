package list

import (
	"image"
	"strings"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/stringext"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// DefaultHighlighter is the default highlighter function that applies inverse style.
var DefaultHighlighter Highlighter = func(x, y int, c *uv.Cell) *uv.Cell {
	if c == nil {
		return c
	}
	c.Style.Attrs |= uv.AttrReverse
	return c
}

// concealed reports whether a cell is concealed: rendered, but not shown
// as text.
func concealed(c *uv.Cell) bool {
	return c.Style.Attrs&uv.AttrConceal != 0
}

// copiedText is what a selection copies for a cell, and whether it copies
// anything. It is the one rule for what a selection holds: extractRow
// copies by it and HighlightBuffer paints by it, so what shows as selected
// is exactly what copies.
//
// Concealed cells are not shown as text, so they are not part of what was
// selected. Renderers conceal layout that is not content, such as a code
// block's margin, so copied code carries no indent its source did not
// have. The one concealed cell that does copy is the codespan padding
// ([styles.CodespanPaddingMarkup]): markdown inline code renders it in
// place of its backticks, and a copy must reproduce the source text, so
// it copies as the backtick. A no-break space that is not concealed is
// message text and copies as itself.
func copiedText(c *uv.Cell) (string, bool) {
	switch {
	case c == nil || c.Content == "":
		return "", false
	case !concealed(c):
		return c.Content, true
	case c.Content == styles.CodespanPadding:
		return "`", true
	default:
		return "", false
	}
}

// Highlighter represents a function that defines how to highlight text.
type Highlighter func(x, y int, c *uv.Cell) *uv.Cell

// HighlightContent returns the content with highlighted regions based on the specified parameters.
func HighlightContent(content string, area image.Rectangle, startLine, startCol, endLine, endCol int) string {
	content = stringext.NormalizeSpace(content)

	if startLine < 0 || startCol < 0 {
		return ""
	}

	width, height := area.Dx(), area.Dy()
	buf := renderBuffer(content, area, width, height)

	// Treat -1 as "end of content".
	if endLine < 0 {
		endLine = height - 1
	}
	if endCol < 0 {
		endCol = width
	}

	rows := extractRows(buf, startLine, startCol, endLine, endCol, height)
	return joinRows(rows, width) + "\n"
}

// renderBuffer draws content into a screen buffer of the given dimensions.
func renderBuffer(content string, area image.Rectangle, width, height int) uv.ScreenBuffer {
	buf := uv.NewScreenBuffer(width, height)
	styled := uv.NewStyledString(content)
	styled.Draw(&buf, area)
	return buf
}

// extractRows extracts the text of the selected region from the buffer,
// one string per row, trimmed to the last cell holding content.
func extractRows(buf uv.ScreenBuffer, startLine, startCol, endLine, endCol, height int) []string {
	rows := make([]string, 0, endLine-startLine+1)
	for y := startLine; y <= endLine && y < height; y++ {
		if y >= buf.Height() {
			break
		}

		line := buf.Line(y)
		colStart := 0
		if y == startLine {
			colStart = min(startCol, len(line))
		}
		colEnd := len(line)
		if y == endLine {
			colEnd = min(endCol, len(line))
		}

		rows = append(rows, extractRow(line, colStart, colEnd))
	}
	return rows
}

// extractRow returns the text of a single buffer line between colStart and
// colEnd, as [copiedText] copies each cell, trimmed to the last cell that
// copies anything (explicit spaces included: renderers like glamour pad
// rows with real space cells, so content usually reaches the full width).
func extractRow(line uv.Line, colStart, colEnd int) string {
	lastCellX := -1
	for x := colStart; x < colEnd; x++ {
		if _, ok := copiedText(line.At(x)); ok {
			lastCellX = x
		}
	}

	var row strings.Builder
	for x := colStart; x <= lastCellX; x++ {
		if text, ok := copiedText(line.At(x)); ok {
			row.WriteString(text)
		}
	}
	return row.String()
}

// joinRows stitches screen rows back into text, deciding per row boundary
// whether it was a real newline or a word wrap. Blank rows are paragraph
// breaks; otherwise isWordWrap decides.
func joinRows(rows []string, width int) string {
	var sb strings.Builder
	for i, row := range rows {
		text := strings.TrimRight(row, " ")
		sb.WriteString(text)
		if i == len(rows)-1 {
			break
		}

		next := rows[i+1]
		switch {
		case strings.TrimSpace(next) == "":
			// Blank row: paragraph break.
			sb.WriteString("\n")
		case isWordWrap(text, next, width):
			sb.WriteString(" ")
		default:
			sb.WriteString("\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// isWordWrap reports whether the boundary between the current row and the
// next is a renderer word wrap rather than a real newline.
//
// Renderers like glamour word-wrap paragraphs, filling each wrapped row
// close to the full width; a row whose text reaches past the wrap
// threshold therefore continues on the next row, while a shorter row ends
// a block (a heading, a list item, the last line of a paragraph). The
// threshold is generous because glamour fills wrapped rows to roughly 80%
// or more of the width.
//
// Two overrides apply: a blank next row is a paragraph break (handled by
// the caller), and a next row that starts a new markdown block — a bullet
// or a heading — always begins on its own line, even if the current row is
// full-width (a list item can itself wrap right up to the width before the
// following item).
func isWordWrap(text, next string, width int) bool {
	if startsBlock(next) {
		return false
	}
	return width > 0 && ansi.StringWidth(text) >= width*3/5
}

// startsBlock reports whether a row begins a new markdown block such as a
// list item or heading, rather than continuing a wrapped paragraph.
// Indented rows are continuations of nested content (e.g. the second line
// of a list item), not new blocks.
func startsBlock(row string) bool {
	if row != strings.TrimLeft(row, " ") {
		return false
	}
	switch {
	case strings.HasPrefix(row, "- "), strings.HasPrefix(row, "* "),
		strings.HasPrefix(row, "+ "), strings.HasPrefix(row, "• "),
		strings.HasPrefix(row, "#"):
		return true
	}
	if i := strings.IndexAny(row, ".)"); i > 0 && i < 4 {
		for j := range i {
			if row[j] < '0' || row[j] > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// Highlight highlights a region of text within the given content and region.
func Highlight(content string, area image.Rectangle, startLine, startCol, endLine, endCol int, highlighter Highlighter) string {
	buf := HighlightBuffer(content, area, startLine, startCol, endLine, endCol, highlighter)
	if buf == nil {
		return content
	}
	return buf.Render()
}

// HighlightBuffer highlights a region of text within the given content and
// region, returning a [uv.ScreenBuffer].
func HighlightBuffer(content string, area image.Rectangle, startLine, startCol, endLine, endCol int, highlighter Highlighter) *uv.ScreenBuffer {
	content = stringext.NormalizeSpace(content)

	if startLine < 0 || startCol < 0 {
		return nil
	}

	if highlighter == nil {
		highlighter = DefaultHighlighter
	}

	width, height := area.Dx(), area.Dy()
	buf := uv.NewScreenBuffer(width, height)
	styled := uv.NewStyledString(content)
	styled.Draw(&buf, area)

	// Treat -1 as "end of content"
	if endLine < 0 {
		endLine = height - 1
	}
	if endCol < 0 {
		endCol = width
	}

	for y := startLine; y <= endLine && y < height; y++ {
		if y >= buf.Height() {
			break
		}

		line := buf.Line(y)

		// Determine column range for this line
		colStart := 0
		if y == startLine {
			colStart = min(startCol, len(line))
		}

		colEnd := len(line)
		if y == endLine {
			colEnd = min(endCol, len(line))
		}

		// Track last non-empty position as we go
		lastContentX := -1

		// Single pass: check content and track last non-empty position
		for x := colStart; x < colEnd; x++ {
			cell := line.At(x)
			if cell == nil {
				continue
			}

			// Update last content position if non-empty
			if cell.Content != "" && cell.Content != " " {
				lastContentX = x
			}
		}

		// Only apply highlight up to last content position
		highlightEnd := colEnd
		if lastContentX >= 0 {
			highlightEnd = lastContentX + 1
		} else if lastContentX == -1 {
			highlightEnd = colStart // No content on this line
		}

		// Apply highlight style only to cells with content, and only to
		// cells a copy holds ([copiedText]), so what shows as selected is
		// exactly what copies. A painted cell stays concealed: the copy
		// reads the painted render, and the conceal is what tells it the
		// codespan padding from message text.
		for x := colStart; x < highlightEnd; x++ {
			if !image.Pt(x, y).In(area) {
				continue
			}
			cell := line.At(x)
			if _, ok := copiedText(cell); !ok && (cell == nil || concealed(cell)) {
				continue
			}
			wasConcealed := concealed(cell)
			highlighter(x, y, cell)
			if wasConcealed {
				cell.Style.Attrs |= uv.AttrConceal
			}
		}
	}

	return &buf
}

// ToHighlighter converts a [lipgloss.Style] to a [Highlighter].
func ToHighlighter(lgStyle lipgloss.Style) Highlighter {
	return func(_ int, _ int, c *uv.Cell) *uv.Cell {
		if c != nil {
			c.Style = ToStyle(lgStyle)
		}
		return c
	}
}

// ToStyle converts an inline [lipgloss.Style] to a [uv.Style].
func ToStyle(lgStyle lipgloss.Style) uv.Style {
	var uvStyle uv.Style

	// Colors are already color.Color
	uvStyle.Fg = lgStyle.GetForeground()
	uvStyle.Bg = lgStyle.GetBackground()

	// Build attributes using bitwise OR
	var attrs uint8

	if lgStyle.GetBold() {
		attrs |= uv.AttrBold
	}

	if lgStyle.GetItalic() {
		attrs |= uv.AttrItalic
	}

	if lgStyle.GetUnderline() {
		uvStyle.Underline = uv.UnderlineSingle
	}

	if lgStyle.GetStrikethrough() {
		attrs |= uv.AttrStrikethrough
	}

	if lgStyle.GetFaint() {
		attrs |= uv.AttrFaint
	}

	if lgStyle.GetBlink() {
		attrs |= uv.AttrBlink
	}

	if lgStyle.GetReverse() {
		attrs |= uv.AttrReverse
	}

	uvStyle.Attrs = attrs

	return uvStyle
}
