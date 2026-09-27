package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/filetracker"
)

// writeLargeViewFixture writes a file of count lines, each roughly
// lineLen bytes, and returns its path.
func writeLargeViewFixture(t testing.TB, dir string, count, lineLen int) string {
	t.Helper()

	var b strings.Builder
	for range count {
		b.WriteString(strings.Repeat("a", lineLen-1))
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, "large.txt")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
	return path
}

// BenchmarkViewLargeFileSection views 100 lines near the end of a
// 50k-line file, the shape the view tool is supposed to serve without
// holding the whole file in memory. It runs with the real evidence
// tracker, whose version hash over the file's bytes is part of the
// read's cost in production.
func BenchmarkViewLargeFileSection(b *testing.B) {
	dir := b.TempDir()
	path := writeLargeViewFixture(b, dir, 50_000, 40)

	tool := NewViewTool(nil, filetracker.NewService(nil), nil, dir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "bench-session")
	input, err := json.Marshal(ViewParams{FilePath: path, Offset: 49_000, Limit: 100})
	require.NoError(b, err)
	call := fantasy.ToolCall{ID: "bench", Name: ViewToolName, Input: string(input)}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		resp, err := tool.Run(ctx, call)
		require.NoError(b, err)
		if resp.IsError {
			b.Fatal("view failed: " + resp.Content)
		}
	}
}

// TestStreamTextMatchesWholeFileRead pins the streaming read to the
// whole-file read it replaced: same section, same has-more, the same
// version hash over the file's bytes, and the same evidence ranges
// seenTextRanges locates in those bytes.
func TestStreamTextMatchesWholeFileRead(t *testing.T) {
	t.Parallel()

	longLine := strings.Repeat("x", MaxLineLength+50)
	// The 2000th byte of this line falls in the middle of a rune.
	splitRune := strings.Repeat("y", MaxLineLength-1) + "日日日" + strings.Repeat("z", 20)
	files := map[string]string{
		"trailing_newline":      "alpha\nbeta\ngamma\ndelta\n",
		"no_trailing_newline":   "alpha\nbeta\ngamma",
		"empty_lines":           "\n\n\n",
		"empty":                 "",
		"crlf":                  "a\r\nb\r\nc\r\n",
		"single_line":           "only\n",
		"overlong_lines":        strings.Join([]string{longLine, "short", longLine, "tail"}, "\n"),
		"rune_split_truncation": splitRune + "\nnext\n",
	}

	for name, body := range files {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "file.txt")
			require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

			limits := []int{1, 2, 3, DefaultReadLimit, 10_000}
			for offset := range 6 {
				for _, limit := range limits {
					streamed, err := streamTextFile(path, offset, limit, 0)
					require.NoError(t, err, "offset %d limit %d", offset, limit)
					whole, err := readTextWhole(path, offset, limit, 0)
					require.NoError(t, err, "offset %d limit %d", offset, limit)

					require.Equal(t, whole.content, streamed.content,
						"content offset %d limit %d", offset, limit)
					require.Equal(t, whole.hasMore, streamed.hasMore,
						"hasMore offset %d limit %d", offset, limit)
					require.Equal(t, whole.lines, streamed.lines,
						"lines offset %d limit %d", offset, limit)
					require.Equal(t, sha256.Sum256([]byte(body)), streamed.version,
						"version offset %d limit %d", offset, limit)
					if offset >= 0 {
						require.Equal(t,
							seenTextRanges([]byte(body), offset, streamed.content), streamed.ranges,
							"ranges offset %d limit %d", offset, limit)
					} else {
						require.Nil(t, streamed.ranges, "offset %d", offset)
					}
				}
			}
		})
	}
}

// TestViewToolEvidenceCoversStreamedRead views a section through the
// tool with the real evidence tracker, then runs the check an edit
// runs: the streamed version hash must certify the viewed lines, refuse
// the rest, and go stale when the file changes.
func TestViewToolEvidenceCoversStreamedRead(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	path := filepath.Join(workingDir, "guarded.txt")
	body := "one\ntwo\nthree\nfour\nfive\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	tracker := filetracker.NewService(nil)
	tool := NewViewTool(nil, tracker, nil, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp := runViewTool(t, tool, ctx, ViewParams{FilePath: path, Offset: 1, Limit: 2})
	require.False(t, resp.IsError, "%q", resp.Content)

	evidence := tracker.(filetracker.Evidence)
	data := []byte(body)
	viewed := lineRange(data, 1, 2)
	require.NoError(t, evidence.Check(ctx, "test-session", path, data, []filetracker.Range{viewed}))
	require.ErrorIs(t,
		evidence.Check(ctx, "test-session", path, data, []filetracker.Range{lineRange(data, 4, 1)}),
		filetracker.ErrUnread)
	require.ErrorIs(t,
		evidence.Check(ctx, "test-session", path, []byte("one\ntwo\nCHANGED\nfour\nfive\n"), []filetracker.Range{viewed}),
		filetracker.ErrStale)
}

// TestViewToolNegativeOffset pins the output of the negative-offset
// fallback: lines number from the clamped start and the section is the
// head of the file.
func TestViewToolNegativeOffset(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	path := filepath.Join(workingDir, "negative.txt")
	require.NoError(t, os.WriteFile(path, []byte("a\nb\nc\n"), 0o644))

	tool := newViewToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp := runViewTool(t, tool, ctx, ViewParams{FilePath: path, Offset: -2, Limit: 2})

	require.False(t, resp.IsError, "%q", resp.Content)
	require.Contains(t, resp.Content, "-1|a\n0|b")
	require.Contains(t, resp.Content, "read beyond line 0")
}
