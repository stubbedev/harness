package list

import (
	"strings"
	"testing"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func TestHighlightContentWrappedLines(t *testing.T) {
	t.Parallel()

	// A long line that will wrap at width 20.
	content := "This is a long line that should wrap around"
	width := 20

	// When selecting the entire content, wrapped portions should be joined
	// with spaces, not newlines.
	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)

	// The result should contain only one trailing newline, no internal ones
	// from wrapping.
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	require.Len(t, lines, 1, "wrapped content should be a single logical line")
}

func TestHighlightContentRealNewlinesPreserved(t *testing.T) {
	t.Parallel()

	// Short lines that don't wrap should preserve real newlines.
	content := "first\nsecond"
	width := 40

	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)

	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	require.Len(t, lines, 2, "real newlines should be preserved")
	require.Contains(t, lines[0], "first")
	require.Contains(t, lines[1], "second")
}

func TestHighlightContentParagraphBreak(t *testing.T) {
	t.Parallel()

	content := "first paragraph\n\nsecond paragraph"
	width := 40

	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)

	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 3, "paragraph break should produce empty line")
}

func TestHighlightContentHardWrap(t *testing.T) {
	t.Parallel()

	// A word longer than the width is cut mid-word by the screen buffer;
	// the pieces must be joined without inserting a space.
	content := strings.Repeat("a", 79) + "b"
	width := 80

	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)

	require.Equal(t, strings.Repeat("a", 79)+"b\n", result)
}

func TestHighlightContentMarkdownList(t *testing.T) {
	t.Parallel()

	// Render a markdown list through glamour so the output matches what
	// the chat view actually produces: a long item word-wrapped onto a
	// continuation row, followed by another item.
	md := "- If the current row's content extends past sixty percent of the buffer width emit a space (space, wrap continuation)\n- Otherwise emit a newline (real newline, short lines like headings, list items, code)"
	sty := styles.CharmtonePantera()
	width := 100
	r, err := glamour.NewTermRenderer(glamour.WithStyles(sty.Markdown), glamour.WithWordWrap(width))
	require.NoError(t, err)
	content, err := r.Render(md)
	require.NoError(t, err)

	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)

	// The wrapped item must join with its continuation, and the next item
	// must start on its own line.
	require.Contains(t, result, "(space, wrap continuation)\n", "wrapped continuation must join with a space, got:\n%s", result)
	require.Contains(t, result, "continuation)\n• Otherwise", "next list item must start on its own line, got:\n%s", result)
}

// TestHighlightContentRestoresCodespanBackticks guards the copy side of the
// inline-code copy fix: markdown codespans render blank sentinel padding
// instead of their backticks ([styles.CodespanPadding]), and a selection copy
// must turn that padding back into the original backticks so the clipboard
// matches the source text.
func TestHighlightContentRestoresCodespanBackticks(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	r, err := glamour.NewTermRenderer(glamour.WithStyles(sty.Markdown), glamour.WithWordWrap(80))
	require.NoError(t, err)
	rendered, err := r.Render(`message "this is ` + "`code`" + `" ok`)
	require.NoError(t, err)

	require.NotContains(t, ansi.Strip(rendered), "`", "display must not show backticks")

	result := HighlightContent(rendered, uv.Rect(0, 0, 80, lipgloss.Height(rendered)), 0, 0, -1, -1)
	require.Contains(t, result, `"this is `+"`code`"+`" ok`, "copy must restore the codespan backticks, got:\n%s", result)
}

// TestCodespanPaddingCopiesAsBacktick pins that only the codespan
// padding copies as a backtick: it is a concealed no-break space
// ([styles.CodespanPaddingMarkup]), so a no-break space in the message
// text, which is not concealed, copies as itself. That holds on a
// render the selection has already painted, which is the render a chat
// copy reads.
func TestCodespanPaddingCopiesAsBacktick(t *testing.T) {
	t.Parallel()

	area := uv.Rect(0, 0, 40, 1)
	padded := "foo" + styles.CodespanPaddingMarkup + "bar" + styles.CodespanPaddingMarkup
	require.Equal(t, "foo`bar`\n", HighlightContent(padded, area, 0, 0, -1, -1))

	painted := Highlight(padded, area, 0, 0, -1, -1, nil)
	require.Equal(t, "foo`bar`\n", HighlightContent(painted, area, 0, 0, -1, -1),
		"painting keeps the padding concealed")

	// A no-break space in the text itself is not padding.
	require.Equal(t, "foo\u00a0bar\n", HighlightContent("foo\u00a0bar", area, 0, 0, -1, -1))

	// Regular spaces and plain text are untouched.
	require.Equal(t, "foo bar\n", HighlightContent("foo bar", area, 0, 0, -1, -1))
}

// TestHighlightContentRestoresWrappedCodespanBackticks is the narrow-width
// variant: when the codespan pill wraps onto its own row, the copy must still
// reassemble the pill into a single backticked span.
func TestHighlightContentRestoresWrappedCodespanBackticks(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	width := 16
	r, err := glamour.NewTermRenderer(glamour.WithStyles(sty.Markdown), glamour.WithWordWrap(width))
	require.NoError(t, err)
	rendered, err := r.Render("some text `codespan` and more words to force wrapping across rows")
	require.NoError(t, err)

	result := HighlightContent(rendered, uv.Rect(0, 0, width, lipgloss.Height(rendered)), 0, 0, -1, -1)
	require.Contains(t, result, "`codespan`", "wrapped pill must reassemble in copy, got:\n%s", result)
}

// TestSelectionSkipsConcealedCells pins that a selection treats
// concealed cells as not there: copying leaves them out, and painting
// does not mark them selected, so what shows as selected is exactly
// what copies. Renderers conceal layout that is not content, such as a
// code block's margin.
func TestSelectionSkipsConcealedCells(t *testing.T) {
	t.Parallel()

	margin := ansi.Style{}.Conceal(true).Styled("  ")
	content := margin + "ls -la\n" + margin + "  make"
	area := uv.Rect(0, 0, 20, 2)

	require.Equal(t, "ls -la\n  make\n", HighlightContent(content, area, 0, 0, -1, -1))

	buf := HighlightBuffer(content, area, 0, 0, -1, -1, nil)
	for y := range 2 {
		line := buf.Line(y)
		for x := range 2 {
			require.Zero(t, line.At(x).Style.Attrs&uv.AttrReverse, "row %d col %d: the margin is not painted", y, x)
		}
		require.NotZero(t, line.At(2).Style.Attrs&uv.AttrReverse, "row %d: the text is painted", y)
	}
}
