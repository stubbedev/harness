package diff

import (
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// GenerateDiff creates a unified diff from two file contents
func GenerateDiff(beforeContent, afterContent, fileName string) (string, int, int) {
	fileName = strings.TrimPrefix(fileName, "/")

	var (
		unified   = udiff.Unified("a/"+fileName, "b/"+fileName, beforeContent, afterContent)
		additions = 0
		removals  = 0
	)

	lines := strings.SplitSeq(unified, "\n")
	for line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			additions++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removals++
		}
	}

	return unified, additions, removals
}

// CountChanges reports how many lines a unified diff of the two contents
// would add and remove, without rendering the hunks. Rendering a
// whole-file diff costs several milliseconds on large files, so callers
// that only want the counts should measure them here instead.
func CountChanges(beforeContent, afterContent string) (additions, removals int) {
	for _, edit := range udiff.Lines(beforeContent, afterContent) {
		removals += lineCount(beforeContent[edit.Start:edit.End])
		additions += lineCount(edit.New)
	}
	return additions, removals
}

// lineCount counts whole lines the way the unified renderer does: every
// newline starts a line, and a trailing fragment without one counts as a
// line of its own.
func lineCount(text string) int {
	n := strings.Count(text, "\n")
	if len(text) > 0 && !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}
