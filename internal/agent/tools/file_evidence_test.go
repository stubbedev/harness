package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/filetracker"
)

func runFileTool(t testing.TB, tool fantasy.AgentTool, ctx context.Context, params any) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "test", Name: tool.Info().Name, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func TestFileEvidenceSameSecondConflictAndRetry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeViewFixture(t, dir, "file", "alpha\nbeta\n")
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	tracker := filetracker.NewService(nil)
	view := NewViewTool(nil, tracker, nil, dir)
	edit := NewEditTool(nil, &mockHistoryService{}, tracker, dir)
	require.False(t, runViewTool(t, view, ctx, ViewParams{FilePath: path}).IsError)
	stamp := time.Now().Truncate(time.Second)
	require.NoError(t, os.WriteFile(path, []byte("alpha\nBETA\n"), 0o644))
	require.NoError(t, os.Chtimes(path, stamp, stamp))
	params := EditParams{FilePath: path, Edits: []EditOperation{{OldString: "alpha", NewString: "ALPHA"}}}
	resp := runFileTool(t, edit, ctx, params)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "modified since")
	require.Contains(t, resp.Content, "BETA")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "alpha\nBETA\n", string(data))
	require.NoError(t, tracker.(filetracker.Evidence).Check(ctx, "s", path, data, []filetracker.Range{{Start: 0, End: len(data)}}))
	require.False(t, runFileTool(t, edit, ctx, params).IsError)
}

func TestFileEvidencePartialEditDoesNotAuthorizeFullWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeViewFixture(t, dir, "file", "alpha\n"+strings.Repeat("unseen\n", 1000))
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	tracker := filetracker.NewService(nil)
	view := NewViewTool(nil, tracker, nil, dir)
	edit := NewEditTool(nil, &mockHistoryService{}, tracker, dir)
	write := NewWriteTool(nil, &mockHistoryService{}, tracker, dir)
	require.False(t, runViewTool(t, view, ctx, ViewParams{FilePath: path, Limit: 1}).IsError)
	require.False(t, runFileTool(t, edit, ctx, EditParams{FilePath: path, Edits: []EditOperation{{OldString: "alpha", NewString: "ALPHA"}}}).IsError)
	resp := runFileTool(t, write, ctx, WriteParams{FilePath: path, Content: "replacement"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "affected range")
	require.Less(t, len(resp.Content), 2600)
	require.True(t, runFileTool(t, write, ctx, WriteParams{FilePath: path, Content: "replacement"}).IsError)
	require.False(t, runViewTool(t, view, ctx, ViewParams{FilePath: path, Limit: 2000}).IsError)
	require.False(t, runFileTool(t, write, ctx, WriteParams{FilePath: path, Content: "replacement"}).IsError)
}

func TestFileEvidenceRelevantRangeAndTruncatedLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeViewFixture(t, dir, "file", "first\n"+strings.Repeat("x", MaxLineLength+100)+"\nlast\n")
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	tracker := filetracker.NewService(nil)
	view := NewViewTool(nil, tracker, nil, dir)
	require.False(t, runViewTool(t, view, ctx, ViewParams{FilePath: path, Offset: 1, Limit: 1}).IsError)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	evidence := tracker.(filetracker.Evidence)
	require.NoError(t, evidence.Check(ctx, "s", path, data, []filetracker.Range{{Start: 6, End: 6 + MaxLineLength}}))
	require.ErrorIs(t, evidence.Check(ctx, "s", path, data, []filetracker.Range{{Start: 0, End: 5}}), filetracker.ErrUnread)
	require.ErrorIs(t, evidence.Check(ctx, "s", path, data, []filetracker.Range{{Start: 6 + MaxLineLength, End: 7 + MaxLineLength}}), filetracker.ErrUnread)
}

func TestFileEvidenceLSPSourceContext(t *testing.T) {
	t.Parallel()
	path := writeViewFixture(t, t.TempDir(), "file", "zero\none\ntwo\nthree\nfour\n")
	tracker := filetracker.NewService(nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	ctx = context.WithValue(ctx, sourceEvidenceKey{}, tracker)
	require.Contains(t, readSourceContext(path, 2, 0, ctx), "two")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	evidence := tracker.(filetracker.Evidence)
	require.NoError(t, evidence.Check(ctx, "s", path, data, []filetracker.Range{lineRange(data, 2, 1)}))
	require.ErrorIs(t, evidence.Check(ctx, "s", path, data, []filetracker.Range{lineRange(data, 0, 1)}), filetracker.ErrUnread)
}

func TestViewDirectorySortedCappedNonrecursive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "000-directory"), 0o755))
	writeViewFixture(t, filepath.Join(dir, "000-directory"), "nested", "not listed")
	for i := MaxDirectoryEntries + 2; i >= 0; i-- {
		writeViewFixture(t, dir, fmt.Sprintf("file-%03d", i), "")
	}
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	tracker := filetracker.NewService(nil)
	tool := NewViewTool(nil, tracker, nil, dir)
	resp := runViewTool(t, tool, ctx, ViewParams{FilePath: dir, Limit: 9999})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "000-directory/")
	require.NotContains(t, resp.Content, "nested")
	require.Contains(t, resp.Content, "offset 200")
	require.Equal(t, MaxDirectoryEntries, strings.Count(resp.Content, "\n\""))
	require.Less(t, strings.Index(resp.Content, "file-001"), strings.Index(resp.Content, "file-002"))
	require.NotContains(t, resp.Content, "file-199")
	next := runViewTool(t, tool, ctx, ViewParams{FilePath: dir, Offset: 200, Limit: 1})
	require.Contains(t, next.Content, "file-199")
	require.ErrorIs(t, tracker.(filetracker.Evidence).Check(ctx, "s", dir, nil, nil), filetracker.ErrUnread)
}

func TestGuardedWriteRejectsChangedVersionAndCreationCollision(t *testing.T) {
	t.Parallel()
	path := writeViewFixture(t, t.TempDir(), "file", "current")
	require.ErrorIs(t, guardedWrite(path, []byte("old"), []byte("new"), false), filetracker.ErrStale)
	require.Error(t, guardedWrite(path, nil, []byte("new"), true))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "current", string(data))
}

func TestFileEvidenceConcurrentEditsSerialize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeViewFixture(t, dir, "file", "alpha beta")
	tracker := filetracker.NewService(nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	require.False(t, runViewTool(t, NewViewTool(nil, tracker, nil, dir), ctx, ViewParams{FilePath: path}).IsError)
	tool := NewEditTool(nil, &mockHistoryService{}, tracker, dir)
	var wg sync.WaitGroup
	for _, pair := range [][2]string{{"alpha", "ALPHA"}, {"beta", "BETA"}} {
		wg.Go(func() {
			require.False(t, runFileTool(t, tool, ctx, EditParams{FilePath: path, Edits: []EditOperation{{OldString: pair[0], NewString: pair[1]}}}).IsError)
		})
	}
	wg.Wait()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "ALPHA BETA", string(data))
}

func TestFileEvidenceStaleWriteAndReplaceAll(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeViewFixture(t, dir, "file", "same\n"+strings.Repeat("padding\n", 600)+"same\n")
	tracker := filetracker.NewService(nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	view := NewViewTool(nil, tracker, nil, dir)
	require.False(t, runViewTool(t, view, ctx, ViewParams{FilePath: path, Limit: 1}).IsError)
	edit := NewEditTool(nil, &mockHistoryService{}, tracker, dir)
	resp := runFileTool(t, edit, ctx, EditParams{FilePath: path, Edits: []EditOperation{{OldString: "same", NewString: "changed", ReplaceAll: true}}})
	require.True(t, resp.IsError)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(data), "same"))
	require.False(t, runViewTool(t, view, ctx, ViewParams{FilePath: path, Limit: 1000}).IsError)
	data = append(data, []byte("external change")...)
	require.NoError(t, os.WriteFile(path, data, 0o644))
	stamp := time.Now().Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, stamp, stamp))
	write := NewWriteTool(nil, &mockHistoryService{}, tracker, dir)
	resp = runFileTool(t, write, ctx, WriteParams{FilePath: path, Content: "replacement"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "modified since")
	require.Less(t, len(resp.Content), 2600)
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, current)
	require.NoError(t, tracker.(filetracker.Evidence).Check(ctx, "s", path, current, []filetracker.Range{{Start: 0, End: 2048}}))
	require.ErrorIs(t, tracker.(filetracker.Evidence).Check(ctx, "s", path, current, []filetracker.Range{{Start: 0, End: len(current)}}), filetracker.ErrUnread)
}

func TestFileEvidenceCreatedFileAllowsRewrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "new")
	tracker := filetracker.NewService(nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	edit := NewEditTool(nil, &mockHistoryService{}, tracker, dir)
	require.False(t, runFileTool(t, edit, ctx, EditParams{FilePath: path, Edits: []EditOperation{{NewString: "created"}}}).IsError)
	write := NewWriteTool(nil, &mockHistoryService{}, tracker, dir)
	require.False(t, runFileTool(t, write, ctx, WriteParams{FilePath: path, Content: "rewritten"}).IsError)
}

func TestFileEvidenceLineEndingChangeBeforeCommitIsStale(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeViewFixture(t, dir, "file", "alpha\nbeta\n")
	tracker := filetracker.NewService(nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s")
	require.False(t, runViewTool(t, NewViewTool(nil, tracker, nil, dir), ctx, ViewParams{FilePath: path}).IsError)
	require.NoError(t, os.WriteFile(path, []byte("alpha\r\nbeta\r\n"), 0o644))
	edit := editContext{ctx: ctx, files: &mockHistoryService{}, filetracker: tracker, workingDir: dir}
	_, err := commitFileChange(edit, "s", path, "alpha\nbeta\n", "ALPHA\nbeta\n", false)
	require.ErrorIs(t, err, filetracker.ErrStale)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "alpha\r\nbeta\r\n", string(data))
}

// seenTextRangesReference is the original seenTextRanges, which located
// every returned line with its own lineRange scan from the top of the file.
// It is kept as the specification the single-pass version must match.
func seenTextRangesReference(data []byte, offset int, text string) []filetracker.Range {
	var ranges []filetracker.Range
	for i, line := range strings.Split(text, "\n") {
		r := lineRange(data, offset+i, 1)
		raw := strings.TrimSuffix(strings.TrimSuffix(string(data[r.Start:r.End]), "\n"), "\r")
		if len(raw) > MaxLineLength {
			prefix := strings.ToValidUTF8(raw[:MaxLineLength], "")
			if line == prefix+"..." {
				ranges = append(ranges, filetracker.Range{Start: r.Start, End: r.Start + len(prefix)})
			}
		} else if line == raw {
			ranges = append(ranges, r)
		}
	}
	return ranges
}

func TestSeenTextRangesMatchesReference(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", MaxLineLength)
	files := []string{
		"",
		"one",
		"one\n",
		"one\ntwo\nthree",
		"one\ntwo\nthree\n",
		"one\r\ntwo\r\n\r\nfour\r\n",
		"\n\n\n",
		"first\n" + long + "\nlast\n",
		"first\n" + strings.Repeat("x", MaxLineLength+5) + "\nlast",
	}
	for _, file := range files {
		data := []byte(file)
		lines := strings.Split(file, "\n")
		for offset := -2; offset <= len(lines)+2; offset++ {
			for limit := 0; limit <= len(lines)+2; limit++ {
				// The text a view returns for the window, and a copy with
				// one line altered, which must not be credited as seen.
				from, to := min(max(offset, 0), len(lines)), min(max(offset, 0)+limit, len(lines))
				window := slices.Clone(lines[from:to])
				for i, l := range window {
					l = strings.TrimSuffix(l, "\r")
					if len(l) > MaxLineLength {
						l = strings.ToValidUTF8(l[:MaxLineLength], "") + "..."
					}
					window[i] = l
				}
				texts := []string{strings.Join(window, "\n")}
				if len(window) > 0 {
					altered := slices.Clone(window)
					altered[len(altered)/2] += "!"
					texts = append(texts, strings.Join(altered, "\n"))
				}
				for _, text := range texts {
					require.Equal(t, seenTextRangesReference(data, offset, text), seenTextRanges(data, offset, text),
						"file %q offset %d text %q", file, offset, text)
				}
			}
		}
	}
}

// BenchmarkEditLargeFile times exact edits on files of roughly 300 KB and
// 3 MB: one replacement, and three in one call.
func BenchmarkEditLargeFile(b *testing.B) {
	for _, size := range []int{6_000, 60_000} {
		target := func(line int) string {
			return fmt.Sprintf("\tvalue%d := compute(%d, \"item-%d\") // step %d", line, line*7, line%13, line%101)
		}
		b.Run(fmt.Sprintf("lines=%d/single", size), func(b *testing.B) {
			benchmarkFileEdit(b, size, EditOperation{OldString: target(size / 2), NewString: "\tchanged := 1"})
		})
		b.Run(fmt.Sprintf("lines=%d/multi", size), func(b *testing.B) {
			benchmarkFileEdit(b, size,
				EditOperation{OldString: target(size / 4), NewString: "\tfirst := 1"},
				EditOperation{OldString: target(size / 2), NewString: "\tsecond := 2"},
				EditOperation{OldString: target(3 * size / 4), NewString: "\tthird := 3"},
			)
		})
	}
}

// BenchmarkWriteLargeFile times the write tool replacing a file of roughly
// 300 KB and 1.5 MB (write caps a call at 2 MB) with a copy that differs in
// one line.
func BenchmarkWriteLargeFile(b *testing.B) {
	for _, size := range []int{6_000, 30_000} {
		b.Run(fmt.Sprintf("lines=%d", size), func(b *testing.B) {
			dir := b.TempDir()
			content := benchmarkSource(size)
			updated := strings.Replace(content, "value17 :=", "renamed17 :=", 1)
			path := writeViewFixture(b, dir, "file.go", content)
			ctx := context.WithValue(b.Context(), SessionIDContextKey, "s")
			tracker := filetracker.NewService(nil)
			tool := NewWriteTool(nil, &mockHistoryService{}, tracker, dir)
			params := WriteParams{FilePath: path, Content: updated}
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				require.NoError(b, os.WriteFile(path, []byte(content), 0o644))
				filetracker.Observe(ctx, tracker, "s", path, []byte(content), []filetracker.Range{{Start: 0, End: len(content)}})
				b.StartTimer()
				resp := runFileTool(b, tool, ctx, params)
				require.False(b, resp.IsError, resp.Content)
			}
		})
	}
}

// BenchmarkViewOffset times a view of the last page of a large file, the
// read whose evidence ranges used to be located by rescanning the file from
// the top once per returned line.
func BenchmarkViewOffset(b *testing.B) {
	for _, size := range []int{5_000, 50_000} {
		for _, offset := range []int{0, size - DefaultReadLimit} {
			b.Run(fmt.Sprintf("lines=%d/offset=%d", size, offset), func(b *testing.B) {
				dir := b.TempDir()
				var content strings.Builder
				for i := range size {
					fmt.Fprintf(&content, "line %d of the benchmark fixture\n", i)
				}
				path := writeViewFixture(b, dir, "file", content.String())
				ctx := context.WithValue(b.Context(), SessionIDContextKey, "s")
				view := NewViewTool(nil, filetracker.NewService(nil), nil, dir)
				params := ViewParams{FilePath: path, Offset: offset}
				b.ReportAllocs()
				for b.Loop() {
					require.False(b, runViewTool(b, view, ctx, params).IsError)
				}
			})
		}
	}
}
