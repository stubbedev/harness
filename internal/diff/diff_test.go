package diff

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCountChangesMatchesGenerateDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		before string
		after  string
	}{
		{name: "identical", before: "a\nb\nc", after: "a\nb\nc"},
		{name: "single line change", before: "a\nb\nc", after: "a\nB\nc"},
		{name: "insert in middle", before: "a\nb\nc", after: "a\nx\nb\nc"},
		{name: "delete in middle", before: "a\nb\nc", after: "a\nc"},
		{name: "append before trailing partial line", before: "a\nb", after: "a\nx\nb"},
		{name: "append after trailing partial line", before: "a\nb", after: "a\nb\nx"},
		{name: "modify trailing partial line", before: "a\nb", after: "a\nB"},
		{name: "insert at eof without newline", before: "a", after: "a\nx"},
		{name: "create file", before: "", after: "a\nb\nc"},
		{name: "empty file", before: "", after: ""},
		{name: "delete file content", before: "a\nb\nc", after: ""},
		{name: "no trailing newline to trailing newline", before: "a\nb", after: "a\nb\n"},
		{name: "crlf content", before: "a\r\nb\r\n", after: "a\r\nB\r\n"},
		{name: "multiline block replaced", before: "a\nb\nc\nd\ne", after: "a\nx\ny\ne"},
		{name: "two separate changes", before: "a\nb\nc\nd\ne", after: "A\nb\nc\nD\ne"},
		{name: "repeated identical lines", before: "x\nx\nx\nx", after: "x\ny\nx\nx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, additions, removals := GenerateDiff(tt.before, tt.after, "test.txt")
			gotAdditions, gotRemovals := CountChanges(tt.before, tt.after)
			require.Equal(t, additions, gotAdditions, "additions")
			require.Equal(t, removals, gotRemovals, "removals")
		})
	}
}

// TestCountChangesRandomPairsCrossChecks counts against GenerateDiff on
// random line sequences, which covers edit shapes the table above cannot
// enumerate.
func TestCountChangesRandomPairs(t *testing.T) {
	t.Parallel()

	var state uint64 = 42
	next := func(n int) int {
		state = state*6364136223846793005 + 1442695040888963407
		return int((state >> 33) % uint64(n))
	}

	line := func() string {
		words := []string{"a", "b", "c", "dd", "ee"}
		return words[next(len(words))] + fmt.Sprint(next(4))
	}

	for i := range 200 {
		before := make([]string, next(12))
		for j := range before {
			before[j] = line()
		}
		after := slices.Clone(before)

		switch next(4) {
		case 0:
			if len(after) > 0 {
				at := next(len(after))
				after = append(slices.Clone(after[:at]), after[at+1:]...)
			}
		case 1:
			at := next(len(after) + 1)
			after = slices.Insert(slices.Clone(after), at, line())
		case 2:
			if len(after) > 0 {
				at := next(len(after))
				after[at] = line()
			}
		case 3:
			if len(after) > 1 {
				at := next(len(after) - 1)
				after = slices.Insert(slices.Clone(after), at, line(), line())
				after = slices.Delete(after, at+2, at+3)
			}
		}

		beforeText := strings.Join(before, "\n")
		if next(2) == 0 {
			beforeText += "\n"
		}
		afterText := strings.Join(after, "\n")
		if next(2) == 0 {
			afterText += "\n"
		}

		_, additions, removals := GenerateDiff(beforeText, afterText, "test.txt")
		gotAdditions, gotRemovals := CountChanges(beforeText, afterText)
		require.Equal(t, additions, gotAdditions, "additions, case %d", i)
		require.Equal(t, removals, gotRemovals, "removals, case %d", i)
	}
}
