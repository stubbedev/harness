package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestCapToolResponse(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HARNESS_SCRATCH_DIR", root)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "session")
	for _, size := range []int{0, 1, MaxToolResultBytes - 1, MaxToolResultBytes, MaxToolResultBytes + 1, 2 * MaxToolResultBytes, 10 * MaxToolResultBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			content := strings.Repeat("x", size)
			response := fantasy.ToolResponse{Type: "text", Content: content, IsError: true, StopTurn: true, Metadata: `{"exit_code":1}`}
			got := CapToolResponse(ctx, response)
			require.LessOrEqual(t, len(got.Content), maxToolResponseBytes(t))
			if size <= MaxToolResultBytes {
				require.Equal(t, response, got)
				return
			}
			require.LessOrEqual(t, len(got.Content), MaxToolPreviewBytes+2048, "spilled preview stays small")
			path := spilledResultPath(t, got.Content)
			require.True(t, filepath.IsAbs(path))
			require.Equal(t, filepath.Join(root, "session", "tool-results"), filepath.Dir(path))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, content, string(data))
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			info, err = os.Stat(filepath.Dir(path))
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
			require.Contains(t, got.Content, "view with offset/limit or shell")
			got.Content = response.Content
			require.Equal(t, response, got)
		})
	}
}

func TestCapToolResponseUTF8(t *testing.T) {
	t.Setenv("HARNESS_SCRATCH_DIR", t.TempDir())
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "unicode")
	for _, content := range []string{strings.Repeat("界é😀", MaxToolResultBytes), strings.Repeat("x\xff", MaxToolResultBytes)} {
		got := CapToolResponse(ctx, fantasy.NewTextResponse(content))
		require.True(t, utf8.ValidString(got.Content))
		require.LessOrEqual(t, len(got.Content), maxToolResponseBytes(t))
		data, err := os.ReadFile(spilledResultPath(t, got.Content))
		require.NoError(t, err)
		require.Equal(t, content, string(data))
	}
}

func TestCapToolResponseMedia(t *testing.T) {
	t.Setenv("HARNESS_SCRATCH_DIR", filepath.Join(t.TempDir(), "unused"))
	for _, response := range []fantasy.ToolResponse{
		{Type: "image", Content: strings.Repeat("a", MaxToolResultBytes+1), MediaType: "image/png"},
		{Type: "media", Content: strings.Repeat("b", MaxToolResultBytes+1), MediaType: "audio/wav"},
		{Type: "text", Content: strings.Repeat("c", MaxToolResultBytes+1), Data: []byte{1, 2, 3}},
	} {
		require.Equal(t, response, CapToolResponse(t.Context(), response))
	}
	_, err := os.Stat(scratchRoot())
	require.True(t, os.IsNotExist(err))
}

func TestCapToolResponseConcurrent(t *testing.T) {
	t.Setenv("HARNESS_SCRATCH_DIR", t.TempDir())
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "concurrent")
	const count = 32
	responses := make([]fantasy.ToolResponse, count)
	contents := make([]string, count)
	var wg sync.WaitGroup
	for i := range count {
		contents[i] = fmt.Sprintf("%d:", i) + strings.Repeat("x", MaxToolResultBytes)
		wg.Go(func() {
			responses[i] = CapToolResponse(ctx, fantasy.NewTextResponse(contents[i]))
		})
	}
	wg.Wait()
	paths := make(map[string]bool, count)
	for i, response := range responses {
		path := spilledResultPath(t, response.Content)
		require.False(t, paths[path])
		paths[path] = true
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, contents[i], string(data))
		require.LessOrEqual(t, len(response.Content), maxToolResponseBytes(t))
	}
}

func maxToolResponseBytes(t *testing.T) int {
	t.Helper()
	return MaxToolResultBytes
}

func TestCapToolResponseFailure(t *testing.T) {
	for _, mode := range []string{"blocked root", "missing session", "symlink", "public directory"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			session := "session"
			switch mode {
			case "blocked root":
				root = filepath.Join(root, "file")
				require.NoError(t, os.WriteFile(root, []byte("untouched"), 0o600))
			case "missing session":
				session = ""
			case "symlink":
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, session)))
			case "public directory":
				require.NoError(t, os.Chmod(root, 0o777))
			}
			t.Setenv("HARNESS_SCRATCH_DIR", root)
			ctx := context.WithValue(t.Context(), SessionIDContextKey, session)
			response := fantasy.ToolResponse{Type: "text", Content: strings.Repeat("é", MaxToolResultBytes), IsError: true, StopTurn: true, Metadata: "metadata"}
			got := CapToolResponse(ctx, response)
			require.LessOrEqual(t, len(got.Content), maxToolResponseBytes(t))
			require.True(t, utf8.ValidString(got.Content))
			require.Contains(t, got.Content, "WARNING: full result could not be saved")
			require.Contains(t, got.Content, "Omitted output is lost")
			require.Contains(t, got.Content, "Preview:\n")
			require.NotContains(t, got.Content, "saved in full to:")
			got.Content = response.Content
			require.Equal(t, response, got)
			if mode == "blocked root" {
				data, err := os.ReadFile(root)
				require.NoError(t, err)
				require.Equal(t, "untouched", string(data))
			}
		})
	}
}

func spilledResultPath(t *testing.T, content string) string {
	t.Helper()
	_, after, ok := strings.Cut(content, "saved in full to: ")
	require.True(t, ok, content[:min(len(content), 512)])
	path, _, ok := strings.Cut(after, "\n")
	require.True(t, ok)
	return path
}
