package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
)

// directoryTestRoot returns a symlink-resolved temp directory. The tracker
// canonicalizes its root, so a TMPDIR reached through a symlink (macOS /var)
// would otherwise make every path assertion diverge from the tracker's.
func directoryTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return root
}

func directoryTestFile(t *testing.T, root, name, body string) string {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func directoryTestContext(t *testing.T, session string) context.Context {
	t.Helper()
	return context.WithValue(t.Context(), tools.SessionIDContextKey, session)
}

func directoryTestTool(name string, calls *atomic.Int32) fantasy.AgentTool {
	return fantasy.NewAgentTool(name, "test", func(_ context.Context, _ map[string]any, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		calls.Add(1)
		return fantasy.NewTextResponse("underlying content"), nil
	})
}

func directoryTestRun(t *testing.T, ctx context.Context, tool fantasy.AgentTool, args map[string]any) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(args)
	require.NoError(t, err)
	response, err := tool.Run(ctx, fantasy.ToolCall{Name: tool.Info().Name, Input: string(input)})
	require.NoError(t, err)
	return response
}

func TestDirectoryInstructionsNestedPrecedenceAndMultiView(t *testing.T) {
	t.Parallel()
	root := directoryTestRoot(t)
	directoryTestFile(t, root, "AGENTS.md", "root rules")
	directoryTestFile(t, root, "a/AGENTS.md", "parent rules")
	directoryTestFile(t, root, "a/deep/AGENTS.md", "deep rules")
	directoryTestFile(t, root, "z/AGENTS.md", "sibling rules")
	directoryTestFile(t, root, "untouched/AGENTS.md", "unrelated rules")
	tracker := NewDirectoryInstructions(root, nil)
	var calls atomic.Int32
	tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("view", &calls)})[0]
	ctx := directoryTestContext(t, "one")
	args := map[string]any{"files": []map[string]string{{"file_path": "z/file.go"}, {"file_path": "a/deep/file.go"}, {"file_path": "a/deep/other.go"}}}
	response := directoryTestRun(t, ctx, tool, args)
	require.Contains(t, response.Content, "underlying content")
	require.NotContains(t, response.Content, "unrelated rules")
	require.Equal(t, 1, strings.Count(response.Content, "root rules"))
	require.Less(t, strings.Index(response.Content, "root rules"), strings.Index(response.Content, "parent rules"))
	require.Less(t, strings.Index(response.Content, "parent rules"), strings.Index(response.Content, "deep rules"))
	require.Contains(t, response.Content, "sibling rules")
	require.Contains(t, response.Content, `scope="`+filepath.Join(root, "a", "deep")+`"`)
	require.Equal(t, "underlying content", directoryTestRun(t, ctx, tool, args).Content)
}

func TestDirectoryInstructionsMutationRequiresNextModelTurn(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"write", "edit"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := directoryTestRoot(t)
			directoryTestFile(t, root, "nested/AGENTS.md", "review before mutation")
			tracker := NewDirectoryInstructions(root, nil)
			var calls atomic.Int32
			tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool(name, &calls)})[0]
			ctx := directoryTestContext(t, "one")
			args := map[string]any{"file_path": "nested/new/deeper/file.go"}
			response := directoryTestRun(t, ctx, tool, args)
			require.True(t, response.IsError)
			require.Contains(t, response.Content, "review before mutation")
			require.Contains(t, response.Content, "explicitly retry")
			require.True(t, directoryTestRun(t, ctx, tool, args).IsError)
			require.Zero(t, calls.Load())
			messages := tracker.Prepare(ctx, nil)
			require.Len(t, messages, 1)
			require.False(t, directoryTestRun(t, ctx, tool, args).IsError)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestDirectoryInstructionsReadCannotUnlockMutationSameTurn(t *testing.T) {
	t.Parallel()
	root := directoryTestRoot(t)
	directoryTestFile(t, root, "nested/AGENTS.md", "rules")
	tracker := NewDirectoryInstructions(root, nil)
	var calls atomic.Int32
	wrapped := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("view", &calls), directoryTestTool("write", &calls)})
	ctx := directoryTestContext(t, "one")
	args := map[string]any{"file_path": "nested/file.go"}
	require.False(t, directoryTestRun(t, ctx, wrapped[0], args).IsError)
	require.True(t, directoryTestRun(t, ctx, wrapped[1], args).IsError)
	require.EqualValues(t, 1, calls.Load())
	tracker.Prepare(ctx, nil)
	require.False(t, directoryTestRun(t, ctx, wrapped[1], args).IsError)
}

func TestDirectoryInstructionsReloadAndExclusions(t *testing.T) {
	t.Parallel()
	root := directoryTestRoot(t)
	directoryTestFile(t, root, "AGENTS.md", "already in prompt")
	path := directoryTestFile(t, root, "nested/AGENTS.md", "first version")
	tracker := NewDirectoryInstructions(root, nil)
	tracker.ExcludePromptPaths([]string{"AGENTS.md"})
	var calls atomic.Int32
	tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("edit", &calls)})[0]
	ctx := directoryTestContext(t, "one")
	args := map[string]any{"file_path": "nested/file.go"}
	response := directoryTestRun(t, ctx, tool, args)
	require.NotContains(t, response.Content, "already in prompt")
	tracker.Prepare(ctx, nil)
	require.False(t, directoryTestRun(t, ctx, tool, args).IsError)
	require.NoError(t, os.WriteFile(path, []byte("other version"), 0o644))
	response = directoryTestRun(t, ctx, tool, args)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "other version")
	tracker.Prepare(ctx, nil)
	require.NoError(t, os.Remove(path))
	require.Empty(t, tracker.Prepare(ctx, nil))
	require.False(t, directoryTestRun(t, ctx, tool, args).IsError)
	directoryTestFile(t, root, "AGENTS.md", "changed root")
	response = directoryTestRun(t, ctx, tool, args)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "changed root")
}

func TestDirectoryInstructionsCompactionAndResultDedup(t *testing.T) {
	t.Parallel()
	root := directoryTestRoot(t)
	directoryTestFile(t, root, "nested/AGENTS.md", "persistent rules")
	tracker := NewDirectoryInstructions(root, nil)
	var calls atomic.Int32
	tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("view", &calls)})[0]
	ctx := directoryTestContext(t, "one")
	response := directoryTestRun(t, ctx, tool, map[string]any{"file_path": "nested/file.go"})
	for _, output := range []fantasy.ToolResultOutputContent{
		fantasy.ToolResultOutputContentText{Text: response.Content},
		fantasy.ToolResultOutputContentError{Error: errors.New(response.Content)},
		fantasy.ToolResultOutputContentMedia{Text: response.Content},
	} {
		messages := []fantasy.Message{{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{Output: output}}}}
		require.Len(t, tracker.Prepare(ctx, messages), 1)
	}
	compacted := []fantasy.Message{fantasy.NewUserMessage("summary without instructions")}
	replayed := tracker.Prepare(ctx, compacted)
	require.Len(t, replayed, 2)
	require.Len(t, tracker.Prepare(ctx, replayed), 2)
	directoryTestFile(t, root, "nested/AGENTS.md", "latest replay rules")
	updated := tracker.Prepare(ctx, compacted)
	require.True(t, directoryInstructionPresent(updated, "latest replay rules"))
	require.False(t, directoryInstructionPresent(updated, "persistent rules"))
}

func TestDirectoryInstructionsConcurrentSessionIsolation(t *testing.T) {
	t.Parallel()
	root := directoryTestRoot(t)
	directoryTestFile(t, root, "nested/AGENTS.md", "rules")
	tracker := NewDirectoryInstructions(root, nil)
	var calls atomic.Int32
	tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("write", &calls)})[0]
	one := directoryTestContext(t, "one")
	two := directoryTestContext(t, "two")
	args := map[string]any{"file_path": "nested/file.go"}
	var wg sync.WaitGroup
	var blocked atomic.Int32
	for range 24 {
		wg.Go(func() {
			response, err := tool.Run(one, fantasy.ToolCall{Name: "write", Input: `{"file_path":"nested/file.go"}`})
			if err == nil && response.IsError {
				blocked.Add(1)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 24, blocked.Load())
	require.Zero(t, calls.Load())
	require.Empty(t, tracker.Prepare(two, nil))
	tracker.Prepare(one, nil)
	require.False(t, directoryTestRun(t, one, tool, args).IsError)
	require.True(t, directoryTestRun(t, two, tool, args).IsError)
	require.EqualValues(t, 1, calls.Load())
}

func TestDirectoryInstructionsWorkspaceBoundaries(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	directoryTestFile(t, parent, "AGENTS.md", "never ancestor")
	root := filepath.Join(parent, "workspace")
	directoryTestFile(t, root, "AGENTS.md", "workspace rules")
	outside := t.TempDir()
	directoryTestFile(t, outside, "AGENTS.md", "never outside")
	directoryTestFile(t, outside, "file.go", "content")
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	tracker := NewDirectoryInstructions(root, nil)
	var calls atomic.Int32
	tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("view", &calls)})[0]
	ctx := directoryTestContext(t, "one")
	for _, path := range []string{"escape/file.go", filepath.Join(outside, "file.go"), "../AGENTS.md"} {
		response := directoryTestRun(t, ctx, tool, map[string]any{"file_path": path})
		require.Equal(t, "underlying content", response.Content)
	}
	response := directoryTestRun(t, ctx, tool, map[string]any{"file_path": "new.go"})
	require.Contains(t, response.Content, "workspace rules")
	require.NotContains(t, response.Content, "never ancestor")
	require.NotContains(t, response.Content, "never outside")
}

func TestDirectoryInstructionsSymlinkInstructionOutside(t *testing.T) {
	t.Parallel()
	root := directoryTestRoot(t)
	outside := directoryTestFile(t, t.TempDir(), "secret.md", "never expose")
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "AGENTS.md")))
	tracker := NewDirectoryInstructions(root, nil)
	var calls atomic.Int32
	tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("write", &calls)})[0]
	response := directoryTestRun(t, directoryTestContext(t, "one"), tool, map[string]any{"file_path": "new.go"})
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "read instruction")
	require.NotContains(t, response.Content, "never expose")
	require.Zero(t, calls.Load())
}

func TestDirectoryInstructionsDefaultsAndConfiguredPaths(t *testing.T) {
	t.Parallel()
	root := directoryTestRoot(t)
	for _, name := range []string{"HARNESS.md", "CLAUDE.local.md", "GEMINI.md", "custom.md"} {
		directoryTestFile(t, root, filepath.Join("nested", name), "rules "+name)
	}
	tracker := NewDirectoryInstructions(root, []string{"custom.md", "custom.md", "../outside.md", "/outside.md"})
	var calls atomic.Int32
	tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("view", &calls)})[0]
	response := directoryTestRun(t, directoryTestContext(t, "one"), tool, map[string]any{"file_path": "nested/file.go"})
	for _, name := range []string{"HARNESS.md", "CLAUDE.local.md", "GEMINI.md", "custom.md"} {
		require.Equal(t, 1, strings.Count(response.Content, "rules "+name))
	}
}

func TestDirectoryInstructionsStructuredPaths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		input    string
		paths    []string
		mutation bool
	}{
		{"shell", `{"command":"cat nested/file.go"}`, []string{}, false},
		{"shell", `{"command":"anything","working_dir":"nested"}`, []string{"nested"}, false},
		{"lsp", `{"action":"definition","path":"nested","file_path":"ignored"}`, []string{"nested"}, false},
		{"lsp", `{"action":"replace_symbol","file_path":"nested/file.go","path":"ignored"}`, []string{"nested/file.go"}, true},
		{"lsp", `{"action":"rename","path":"nested"}`, []string{"nested"}, true},
		{"lsp", `{"action":"restart","path":"ignored"}`, nil, false},
		{"view", `{"file_path":"one","files":[{"file_path":"two"}]}`, nil, false},
		{"view", `{"file_path":"harness://skills/skill"}`, []string{}, false},
		{"unknown", `{"file_path":"ignored"}`, nil, false},
	} {
		paths, mutation := directoryInstructionPaths(tc.name, tc.input)
		require.Equal(t, tc.paths, paths, tc.input)
		require.Equal(t, tc.mutation, mutation, tc.input)
	}
}

func TestDirectoryInstructionsBoundedOutput(t *testing.T) {
	t.Parallel()
	for _, total := range []bool{false, true} {
		t.Run(map[bool]string{false: "per-file", true: "total"}[total], func(t *testing.T) {
			t.Parallel()
			root := directoryTestRoot(t)
			if total {
				for _, name := range []string{"AGENTS.md", "CLAUDE.md", "HARNESS.md", "GEMINI.md"} {
					directoryTestFile(t, root, name, strings.Repeat("x", directoryInstructionFileLimit))
				}
			} else {
				directoryTestFile(t, root, "AGENTS.md", strings.Repeat("x", directoryInstructionFileLimit+1))
			}
			tracker := NewDirectoryInstructions(root, nil)
			var calls atomic.Int32
			tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("write", &calls)})[0]
			response := directoryTestRun(t, directoryTestContext(t, "one"), tool, map[string]any{"file_path": "file.go"})
			require.True(t, response.IsError)
			require.Contains(t, response.Content, "limit")
			require.Contains(t, response.Content, "truncated")
			require.Less(t, len(response.Content), 2048)
			require.Zero(t, calls.Load())
		})
	}
}

func TestDirectoryInstructionsWorktreeRoot(t *testing.T) {
	t.Parallel()
	root := directoryTestRoot(t)
	worktree := t.TempDir()
	directoryTestFile(t, root, "nested/AGENTS.md", "main workspace")
	directoryTestFile(t, worktree, "nested/AGENTS.md", "worktree workspace")
	tracker := NewDirectoryInstructions(worktree, nil)
	var calls atomic.Int32
	tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("view", &calls)})[0]
	response := directoryTestRun(t, directoryTestContext(t, "child"), tool, map[string]any{"file_path": "nested/file.go"})
	require.Contains(t, response.Content, "worktree workspace")
	require.NotContains(t, response.Content, "main workspace")
}

func TestDirectoryInstructionsWrapperIdentity(t *testing.T) {
	t.Parallel()
	tracker := NewDirectoryInstructions(t.TempDir(), nil)
	var calls atomic.Int32
	inner := directoryTestTool("view", &calls)
	wrapped := tracker.WrapTools([]fantasy.AgentTool{inner})[0]
	require.Equal(t, inner.Info(), wrapped.Info())
	options := fantasy.ProviderOptions{"test": &openaicompat.ProviderOptions{ExtraBody: map[string]any{"key": "value"}}}
	wrapped.SetProviderOptions(options)
	require.Equal(t, options, inner.ProviderOptions())
	require.Equal(t, options, wrapped.ProviderOptions())
	require.Empty(t, wrapped.(interface{ MCP() string }).MCP())
	mcp := &directoryTestMCPTool{AgentTool: inner}
	require.Same(t, mcp, tracker.WrapTools([]fantasy.AgentTool{mcp})[0])
}

type directoryTestMCPTool struct{ fantasy.AgentTool }

func (t *directoryTestMCPTool) MCP() string { return "server" }

func TestDirectoryInstructionsRootSymlinkAndConfiguredScope(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "real")
	directoryTestFile(t, root, "nested/policy/rules.md", "configured nested rules")
	alias := filepath.Join(parent, "alias")
	require.NoError(t, os.Symlink(root, alias))
	tracker := NewDirectoryInstructions(alias, []string{"policy/rules.md"})
	var calls atomic.Int32
	tool := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("view", &calls)})[0]
	ctx := directoryTestContext(t, "one")
	response := directoryTestRun(t, ctx, tool, map[string]any{"file_path": filepath.Join(alias, "nested/file.go")})
	require.Contains(t, response.Content, "configured nested rules")
	require.Contains(t, response.Content, `scope="`+filepath.Join(root, "nested")+`"`)
	replayed := tracker.Prepare(ctx, nil)
	require.True(t, directoryInstructionPresent(replayed, `scope="`+filepath.Join(root, "nested")+`"`))
}

func TestDirectoryInstructionsLSPAndShellScopes(t *testing.T) {
	t.Parallel()
	root := directoryTestRoot(t)
	directoryTestFile(t, root, "nested/AGENTS.md", "nested rules")
	tracker := NewDirectoryInstructions(root, nil)
	var calls atomic.Int32
	wrapped := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("shell", &calls), directoryTestTool("lsp", &calls)})
	ctx := directoryTestContext(t, "one")
	response := directoryTestRun(t, ctx, wrapped[0], map[string]any{"command": "cat nested/file.go"})
	require.NotContains(t, response.Content, "nested rules")
	response = directoryTestRun(t, ctx, wrapped[0], map[string]any{"working_dir": "nested"})
	require.Contains(t, response.Content, "nested rules")
	response = directoryTestRun(t, ctx, wrapped[1], map[string]any{"action": "rename", "path": "nested"})
	require.True(t, response.IsError)
	tracker.Prepare(ctx, nil)
	response = directoryTestRun(t, ctx, wrapped[1], map[string]any{"action": "replace_symbol", "file_path": "nested/file.go"})
	require.False(t, response.IsError)
}
