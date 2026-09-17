package tools

import (
	"strings"
	"testing"
)

// fakeSizer reports a fixed terminal size for needsPasteMarkers tests.
type fakeSizer struct{ rows, cols int }

func (f fakeSizer) Size() (int, int) { return f.rows, f.cols }

func TestNeedsPasteMarkers(t *testing.T) {
	t.Parallel()

	wide := fakeSizer{rows: 40, cols: 200}
	narrow := fakeSizer{rows: 40, cols: 60}
	unknown := fakeSizer{rows: 0, cols: 0}

	cases := []struct {
		name string
		s    ptySizer
		body string
		want bool
	}{
		{"short single line", wide, "ls -la", false},
		{"multiline always needs markers", narrow, "echo a\necho b", true},
		{"long single line on a narrow terminal wraps", narrow, strings.Repeat("x", 60), true},
		{"same long line fits a wide terminal", wide, strings.Repeat("x", 60), false},
		{"line just past the wrap margin needs markers", narrow, strings.Repeat("x", 41), true},
		{"line comfortably inside the margin does not", narrow, strings.Repeat("x", 30), false},
		{"unknown size never trips the length check", unknown, strings.Repeat("x", 500), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := needsPasteMarkers(tc.s, tc.body); got != tc.want {
				t.Errorf("needsPasteMarkers(%dx%d cols, len %d) = %v, want %v",
					tc.s.(fakeSizer).rows, tc.s.(fakeSizer).cols, len(tc.body), got, tc.want)
			}
		})
	}
}
