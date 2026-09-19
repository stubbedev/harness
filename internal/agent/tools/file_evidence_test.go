package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/filetracker"
)

func runFileTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params any) fantasy.ToolResponse {
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
	require.ErrorIs(t, commitFileChange(edit, "s", path, "alpha\nbeta\n", "ALPHA\nbeta\n", false), filetracker.ErrStale)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "alpha\r\nbeta\r\n", string(data))
}
