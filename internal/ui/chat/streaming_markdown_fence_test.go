package chat

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFenceLineStartsAgreesWithCount pins the index findBoundaryAfter
// skips candidates with to the count the full predicate uses.
func TestFenceLineStartsAgreesWithCount(t *testing.T) {
	t.Parallel()

	content := "intro\n\n```go\nfunc a() {}\n\n  ```\n\ntext\n~~~\n\ncode\n~~~\ntail"
	fences := fenceLineStarts(content, 0)
	for p := 0; p <= len(content); p++ {
		if p != 0 && content[p-1] != '\n' {
			continue
		}
		require.Equal(t, countFenceLines(content[:p]), sort.SearchInts(fences, p), "line start %d", p)
	}
}

// BenchmarkFindBoundaryAfterInOpenFence is the case the fence index
// exists for: a long code block still streaming, full of blank lines.
func BenchmarkFindBoundaryAfterInOpenFence(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("intro\n\n```go\n")
	for range 2000 {
		sb.WriteString("x := 1\n\n")
	}
	content := sb.String()
	s := &streamingMarkdown{stablePrefix: "intro\n\n"}
	for b.Loop() {
		s.findBoundaryAfter(content)
	}
}
