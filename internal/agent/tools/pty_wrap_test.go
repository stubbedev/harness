package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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

// The echoed sentinel command is dropped wherever it lands: whole,
// behind a prompt, or wrapped at the window edge into fragments that
// only together rebuild it. The macOS CI shape: a 100-column terminal
// splits the ~115-character command after "/dev/ ", leaving the tail
// on the next screen line, with the command's own output ahead of it.
func TestCleanTruncatesAtWrappedSentinelEcho(t *testing.T) {
	t.Parallel()
	r := &ptyRunner{}
	mark := newSentinel(posixDialect)
	require.Greater(t, len(mark.cmd), 60, "the wrapped-echo shape needs a command longer than one line")

	head, tail := mark.cmd[:60], mark.cmd[60:]
	raw := "30 100\r\n\r\n" + head + " \r\n" + tail + "\r\n"
	require.Equal(t, "30 100", r.cleanWith(mark, raw, nil))
}

// Real output after the sentinel's echo survives it; the echo is
// dropped, not truncated at.
func TestCleanKeepsOutputAfterSentinelEcho(t *testing.T) {
	t.Parallel()
	r := &ptyRunner{}
	mark := newSentinel(posixDialect)

	raw := mark.cmd + "\r\nlater output\r\n"
	require.Equal(t, "later output", r.cleanWith(mark, raw, nil))
}

// Output that merely starts like the sentinel command but is too short
// to open a fragment run is real output and survives.
func TestCleanKeepsShortLookalikeOutput(t *testing.T) {
	t.Parallel()
	r := &ptyRunner{}
	mark := newSentinel(posixDialect)

	raw := "printf '__\r\nnull\r\n"
	require.Equal(t, "printf '__\nnull", r.cleanWith(mark, raw, nil))
}
