package common

import (
	"fmt"
	"image"
	"strconv"
	"strings"
	"testing"

	"charm.land/glamour/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func renderMarkdown(t *testing.T, r *glamour.TermRenderer, src string) string {
	t.Helper()
	mu := LockMarkdownRenderer(r)
	mu.Lock()
	defer mu.Unlock()
	out, err := r.Render(src)
	require.NoError(t, err)
	return out
}

// lineContaining draws rendered into a screen buffer and returns the row
// whose text contains want.
func lineContaining(t *testing.T, rendered, want string) uv.Line {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	buf := uv.NewScreenBuffer(120, len(lines))
	uv.NewStyledString(rendered).Draw(&buf, image.Rect(0, 0, 120, len(lines)))
	for y := range len(lines) {
		if strings.Contains(ansi.Strip(buf.Line(y).Render()), want) {
			return buf.Line(y)
		}
	}
	t.Fatalf("no rendered line contains %q:\n%s", want, ansi.Strip(rendered))
	return nil
}

// TestCodeBlockMarginIsConcealed pins how a code block's margin renders
// in every markdown style: as blank cells that look exactly like the
// indent they replace, but concealed, which is what lets a selection
// copy leave them out while terminal-native copies still get spaces.
// The code's own indentation is plain text and stays.
func TestCodeBlockMarginIsConcealed(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	for name, r := range map[string]*glamour.TermRenderer{
		"markdown": MarkdownRenderer(&sty, 80),
		"user":     UserMarkdownRenderer(&sty, 80),
		"quiet":    QuietMarkdownRenderer(&sty, 80),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rendered := renderMarkdown(t, r, "Run:\n\n```bash\nls -la\n  make\n```\n")
			require.Contains(t, ansi.Strip(rendered), "  ls -la", "the margin still shows")

			for _, tc := range []struct {
				text   string
				indent int
			}{{"ls -la", 0}, {"make", 2}} {
				line := lineContaining(t, rendered, tc.text)
				start := strings.Index(ansi.Strip(line.Render()), tc.text)
				for x := range start {
					cell := line.At(x)
					require.Equal(t, " ", cell.Content)
					margin := x < start-tc.indent
					require.Equal(t, margin, cell.Style.Attrs&uv.AttrConceal != 0,
						"cell %d of %q: only the margin is concealed", x, tc.text)
				}
				first := line.At(start).Style
				require.Zero(t, first.Attrs&uv.AttrConceal, "code is not concealed")
				require.NotNil(t, first.Fg, "the margin does not reset the styling the line opens with")
			}
		})
	}
}

// TestCodeBlockColorsFollowTheme pins that code blocks are highlighted
// with the active theme's colors after a theme switch, not with those of
// the first theme the process rendered with. Not parallel: it switches
// the process-wide renderer rules.
func TestCodeBlockColorsFollowTheme(t *testing.T) {
	for _, theme := range []string{"charmtone", "gruvbox-dark", "charmtone"} {
		sty := styles.ThemeFromConfig(theme)
		InvalidateMarkdownRendererCache()
		rendered := renderMarkdown(t, MarkdownRenderer(&sty, 80), "```bash\nif true; then echo; fi\n```\n")
		require.Contains(t, rendered, sgrForeground(t, *sty.Markdown.CodeBlock.Chroma.Keyword.Color),
			"%s: keywords take the theme's keyword color", theme)
	}
}

// sgrForeground returns the truecolor foreground SGR parameters for a
// "#rrggbb" color, as lipgloss writes them.
func sgrForeground(t *testing.T, hex string) string {
	t.Helper()
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	require.NoError(t, err)
	return fmt.Sprintf("38;2;%d;%d;%dm", v>>16&0xff, v>>8&0xff, v&0xff)
}

// TestLinePrefixWriter pins where the margin goes: in front of the first
// printable byte of each line and in front of the styling that opens it,
// so the margin's own reset cannot cancel that styling, and nowhere on
// a line holding nothing but styling.
func TestLinePrefixWriter(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	w := &linePrefixWriter{w: &out, prefix: ">", atLineStart: true}
	for _, chunk := range []string{"\x1b[1mab", "c\x1b[m\n", "\x1b[2mde\n", "\x1b[m"} {
		n, err := w.Write([]byte(chunk))
		require.NoError(t, err)
		require.Equal(t, len(chunk), n)
	}
	require.NoError(t, w.Close())
	require.Equal(t, ">\x1b[1mabc\x1b[m\n>\x1b[2mde\n\x1b[m", out.String())
}
