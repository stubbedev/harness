package vt10x

import (
	"fmt"
	"strings"
	"testing"
)

// refScrollUp is the upstream scrollUp this fork's rotation replaced,
// kept as the reference the rotation is checked against.
func refScrollUp(t *State, orig, n int) {
	n = clamp(n, 0, t.bottom-orig+1)
	t.clear(0, orig, t.cols-1, orig+n-1)
	for i := orig; i <= t.bottom-n; i++ {
		t.lines[i], t.lines[i+n] = t.lines[i+n], t.lines[i]
	}
}

// filledState returns a rows x cols state whose every line is labelled,
// with the scroll region set to top..bottom.
func filledState(rows, cols, top, bottom int) *State {
	st := newState(nil)
	st.resize(cols, rows)
	for y := range rows {
		label := fmt.Sprintf("row%02d", y)
		for x, c := range label {
			st.lines[y][x].Char = c
		}
	}
	st.top, st.bottom = top, bottom
	return st
}

// The rotation leaves the screen exactly as the pairwise swaps did, for
// every region and every scroll distance, the ones that empty the whole
// region included.
func TestScrollUpMatchesReference(t *testing.T) {
	const rows, cols = 8, 12
	for top := range rows {
		for bottom := top; bottom < rows; bottom++ {
			for orig := top; orig <= bottom; orig++ {
				for n := 0; n <= bottom-orig+2; n++ {
					got := filledState(rows, cols, top, bottom)
					want := filledState(rows, cols, top, bottom)
					got.scrollUp(orig, n)
					refScrollUp(want, orig, n)
					if g, w := got.String(), want.String(); g != w {
						t.Fatalf("top=%d bottom=%d orig=%d n=%d\ngot:\n%s\nwant:\n%s", top, bottom, orig, n, g, w)
					}
				}
			}
		}
	}
}

// Output longer than the screen leaves its last lines on it.
func TestScrollingOutputKeepsTail(t *testing.T) {
	term := New(WithSize(10, 4))
	var in strings.Builder
	for i := range 50 {
		fmt.Fprintf(&in, "L%02d\r\n", i)
	}
	if _, err := term.Write([]byte(in.String())); err != nil {
		t.Fatal(err)
	}
	for row, want := range []string{"L47", "L48", "L49", "   "} {
		if got := extractStr(term, 0, 2, row); got != want {
			t.Fatalf("row %d = %q, want %q", row, got, want)
		}
	}
}

func TestFillGlyphs(t *testing.T) {
	for n := range 70 {
		row := make([]Glyph, n)
		fillGlyphs(row, Glyph{Char: 'x', FG: 3})
		for i, g := range row {
			if g.Char != 'x' || g.FG != 3 {
				t.Fatalf("len %d: cell %d = %+v", n, i, g)
			}
		}
	}
}
