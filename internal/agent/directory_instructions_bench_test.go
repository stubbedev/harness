package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
)

// directoryBenchTree builds a tree depth directories deep with width plain
// files in every directory, plus the named instruction files, and returns
// the root and the path of a file in the deepest directory. Every entry is
// backdated so the tracker's recently-modified guard does not keep it from
// caching what it lists; a freshly created tree would otherwise measure the
// uncached path for its first seconds.
func directoryBenchTree(b *testing.B, depth, width int, instructions ...string) (string, string) {
	b.Helper()
	root, err := filepath.EvalSymlinks(b.TempDir())
	require.NoError(b, err)
	dir := root
	for level := range depth {
		for i := range width {
			require.NoError(b, os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%03d.go", i)), nil, 0o644))
		}
		if level < depth-1 {
			dir = filepath.Join(dir, fmt.Sprintf("level%d", level))
			require.NoError(b, os.Mkdir(dir, 0o755))
		}
	}
	for _, name := range instructions {
		path := filepath.Join(root, name)
		require.NoError(b, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(b, os.WriteFile(path, []byte("rules for "+name), 0o644))
	}
	old := time.Now().Add(-time.Hour)
	require.NoError(b, filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, old, old)
	}))
	return root, filepath.Join(dir, "file000.go")
}

// directoryBenchRepoRoot returns the root of the repository the test runs
// in, so the benchmark also measures a real tree.
func directoryBenchRepoRoot(b *testing.B) string {
	b.Helper()
	dir, err := os.Getwd()
	require.NoError(b, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			resolved, err := filepath.EvalSymlinks(dir)
			require.NoError(b, err)
			return resolved
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			b.Skip("no go.mod above the test directory")
		}
		dir = parent
	}
}

// BenchmarkDirectoryInstructionsActivate measures what one wrapped view call
// pays for instruction discovery once its instructions are active, which is
// the steady state of a session: every later call in the same tree repeats
// the walk.
func BenchmarkDirectoryInstructionsActivate(b *testing.B) {
	run := func(b *testing.B, root, file string) {
		tracker := NewDirectoryInstructions(root, nil)
		var calls atomic.Int32
		tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("view", &calls)})[0]
		ctx := context.WithValue(b.Context(), tools.SessionIDContextKey, "bench")
		input, err := json.Marshal(map[string]string{"file_path": file})
		require.NoError(b, err)
		call := fantasy.ToolCall{Name: "view", Input: string(input)}
		_, err = tool.Run(ctx, call)
		require.NoError(b, err)
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if _, err := tool.Run(ctx, call); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("synthetic-none", func(b *testing.B) {
		root, file := directoryBenchTree(b, 8, 300)
		run(b, root, file)
	})
	b.Run("synthetic-three", func(b *testing.B) {
		root, file := directoryBenchTree(b, 8, 300, "AGENTS.md", "level0/level1/CLAUDE.md", "level0/level1/level2/level3/.github/copilot-instructions.md")
		run(b, root, file)
	})
	b.Run("repo", func(b *testing.B) {
		root := directoryBenchRepoRoot(b)
		run(b, root, filepath.Join(root, "internal", "agent", "directory_instructions.go"))
	})
}

// BenchmarkDirectoryInstructionsPrepare measures the per-step re-check with
// three active instruction files and a 400-message history that already
// carries them.
func BenchmarkDirectoryInstructionsPrepare(b *testing.B) {
	root, file := directoryBenchTree(b, 4, 10, "AGENTS.md", "level0/AGENTS.md", "level0/level1/AGENTS.md")
	tracker := NewDirectoryInstructions(root, nil)
	var calls atomic.Int32
	tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("view", &calls)})[0]
	ctx := context.WithValue(b.Context(), tools.SessionIDContextKey, "bench")
	input, err := json.Marshal(map[string]string{"file_path": file})
	require.NoError(b, err)
	response, err := tool.Run(ctx, fantasy.ToolCall{Name: "view", Input: string(input)})
	require.NoError(b, err)
	filler := strings.Repeat("ordinary conversation text ", 40)
	var messages []fantasy.Message
	for i := range 400 {
		messages = append(messages, fantasy.NewUserMessage(fmt.Sprintf("%s %d", filler, i)))
		// The instructions arrive with a tool result part way through
		// the session, so a scan from the start pays for the history
		// before them.
		if i == 300 {
			messages = append(messages, fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{Output: fantasy.ToolResultOutputContentText{Text: response.Content}}}})
		}
	}
	require.Len(b, tracker.Prepare(ctx, messages), len(messages))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		tracker.Prepare(ctx, messages)
	}
}
