package model

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// decodeInto decodes rendered into a cache buffer the way Chat.Draw does,
// so the blit tests exercise the same ScreenBuffer Chat.Draw blits from.
func decodeInto(tb testing.TB, rendered string) uv.ScreenBuffer {
	tb.Helper()
	c := &chatDrawCache{}
	c.decode(rendered, ansi.WcWidth)
	require.Equal(tb, rendered, c.rendered)
	return c.buf
}

// blitCases are the content shapes that make a row copy differ from a cell
// loop if it is written carelessly: wide cells and their placeholders,
// styled runs, a frame that shrinks, and one that leaves the area's tail
// blank.
func blitCases() []struct{ name, first, second string } {
	return []struct{ name, first, second string }{
		{
			name:   "same content",
			first:  "alpha\nbeta\ngamma",
			second: "alpha\nbeta\ngamma",
		},
		{
			name:   "shrinks",
			first:  "one\ntwo\nthree\nfour",
			second: "one",
		},
		{
			name:   "styled",
			first:  "\x1b[31mred\x1b[0m plain \x1b[1;32mbold green\x1b[0m",
			second: "\x1b[34mblue\x1b[0m replaces \x1b[33myellow\x1b[0m",
		},
		{
			name:   "wide cells",
			first:  "汉字 text 漢字",
			second: "漢字 mixed 汉字 wider",
		},
		{
			name:   "heavy wide cells",
			first:  strings.Repeat("漢字かな ", 6),
			second: strings.Repeat("かな漢字 ", 6),
		},
		{
			name:   "emoji cluster",
			first:  "👩‍💻 commit faces 👍",
			second: "👍 faces 家族 back",
		},
	}
}

// TestBlitBufferMatchesStyledDraw pins the blit to the same cells an
// uncached frame produces: whatever the previous frame left in the buffer,
// blitting the new frame must leave exactly what uv.StyledString.Draw would
// paint into a fresh one. The area is offset and smaller than the screen,
// the way the chat sits between the header and the editor.
func TestBlitBufferMatchesStyledDraw(t *testing.T) {
	t.Parallel()

	const w, h = 60, 8
	screen := uv.Rect(0, 0, w+8, h+4)
	area := uv.Rect(6, 3, 6+w-10, 3+h-2)
	for _, tc := range blitCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			scr := uv.NewScreenBuffer(screen.Dx(), screen.Dy())
			scr.Method = ansi.WcWidth
			drawCachedBuffer(scr, area, decodeInto(t, tc.first))
			drawCachedBuffer(scr, area, decodeInto(t, tc.second))

			want := uv.NewScreenBuffer(screen.Dx(), screen.Dy())
			want.Method = ansi.WcWidth
			uv.NewStyledString(tc.second).Draw(want, area)
			require.Equal(t, want.Render(), scr.Render())
		})
	}
}

// TestBlitBufferOnlyTouchesChangedRows pins the frame-diffing half: a blit
// that changes nothing leaves every line untouched, so an idle frame keeps
// producing no terminal output.
func TestBlitBufferOnlyTouchesChangedRows(t *testing.T) {
	t.Parallel()

	const w, h = 40, 6
	area := uv.Rect(0, 0, w, h)
	content := "first line\nsecond line\n" + strings.Repeat("漢字 ", 5)

	scr := uv.NewScreenBuffer(w, h)
	scr.Method = ansi.WcWidth
	drawCachedBuffer(scr, area, decodeInto(t, content))
	require.NotEmpty(t, scr.Touched, "the first frame must mark its lines")
	for i := range scr.Touched {
		scr.Touched[i] = nil
	}

	drawCachedBuffer(scr, area, decodeInto(t, content))
	for y, line := range scr.Touched {
		require.Nil(t, line, "unchanged row %d must not be touched", y)
	}

	drawCachedBuffer(scr, area, decodeInto(t, "first line\nchanged line\n"+strings.Repeat("漢字 ", 5)))
	require.NotNil(t, scr.Touched[1], "a changed row is touched")
	require.Nil(t, scr.Touched[0], "an unchanged row beside it stays untouched")
}

// TestBlitBufferFallsBackForOtherScreens pins the guard: a screen that is
// not ultraviolet's own keeps the generic clear-and-draw path, so a
// different implementation cannot be written through as if it were a
// buffer.
func TestBlitBufferFallsBackForOtherScreens(t *testing.T) {
	t.Parallel()

	scr := &redirectScreen{buf: uv.NewScreenBuffer(20, 4)}
	require.False(t, blitBuffer(scr, uv.Rect(0, 0, 20, 4), &uv.ScreenBuffer{}))
}

// redirectScreen is a Screen that is not a uv.ScreenBuffer, standing in for
// any other screen implementation.
type redirectScreen struct{ buf uv.ScreenBuffer }

func (s *redirectScreen) Bounds() uv.Rectangle         { return s.buf.Bounds() }
func (s *redirectScreen) CellAt(x, y int) *uv.Cell     { return s.buf.CellAt(x, y) }
func (s *redirectScreen) SetCell(x, y int, c *uv.Cell) { s.buf.SetCell(x, y, c) }
func (s *redirectScreen) WidthMethod() uv.WidthMethod  { return s.buf.WidthMethod() }

// BenchmarkDrawCachedBuffer measures the per-frame copy of an already
// decoded buffer, the fast path against the clear-then-draw it replaced.
func BenchmarkDrawCachedBuffer(b *testing.B) {
	const w, h = 140, 45
	area := uv.Rect(0, 0, w, h)
	var sb strings.Builder
	for i := range h {
		fmt.Fprintf(&sb, "\x1b[38;5;%dmline %d\x1b[0m with some \x1b[1mstyled\x1b[0m text\n", 20+i, i)
	}
	src := decodeInto(b, sb.String())

	blit := uv.NewScreenBuffer(w, h)
	b.Run("blit", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			drawCachedBuffer(blit, area, src)
		}
	})

	generic := uv.NewScreenBuffer(w, h)
	b.Run("clear-and-draw", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for y := area.Min.Y; y < area.Max.Y; y++ {
				for x := area.Min.X; x < area.Max.X; x++ {
					generic.SetCell(x, y, nil)
				}
			}
			src.Draw(generic, area)
		}
	})
}
