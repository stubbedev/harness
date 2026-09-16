package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateContextFile(t *testing.T) {
	t.Parallel()

	small := ContextFile{Path: "AGENTS.md", Content: "one\ntwo\n"}
	assert.Equal(t, small, truncateContextFile(small, 100), "content within the limit is untouched")

	big := ContextFile{Path: "AGENTS.md", Content: strings.Repeat("0123456789\n", 100)}
	cut := truncateContextFile(big, 105)
	assert.True(t, strings.HasPrefix(cut.Content, strings.Repeat("0123456789\n", 9)), "the cut lands on a line boundary")
	assert.Contains(t, cut.Content, "[context file AGENTS.md truncated: 1001 bytes not shown")
	assert.Less(t, len(cut.Content), 105+120)

	oneLine := ContextFile{Path: "x", Content: strings.Repeat("a", 50)}
	cutLine := truncateContextFile(oneLine, 10)
	assert.True(t, strings.HasPrefix(cutLine.Content, "aaaaaaaaaa\n["), "a single long line is cut mid-line rather than dropped")
}

func TestCapContextFilesSharesTheTotal(t *testing.T) {
	t.Parallel()

	files := []ContextFile{
		{Path: "a", Content: strings.Repeat("a\n", 50)}, // 100 bytes
		{Path: "b", Content: strings.Repeat("b\n", 50)}, // 100 bytes
		{Path: "c", Content: strings.Repeat("c\n", 50)}, // 100 bytes
	}
	capped := capContextFiles(files, 1000, 150)
	require.Len(t, capped, 3)
	assert.Equal(t, files[0].Content, capped[0].Content, "the first file fits whole")
	assert.Contains(t, capped[1].Content, "truncated", "the second gets what is left")
	assert.Contains(t, capped[2].Content, "truncated: 100 bytes not shown", "the third has no room and is its marker alone")
	assert.False(t, strings.HasPrefix(capped[2].Content, "c\n"))

	perFile := capContextFiles(files, 40, 1000)
	for _, f := range perFile {
		assert.Contains(t, f.Content, "truncated", "the per-file limit applies to each")
	}
}
