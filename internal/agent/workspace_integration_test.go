package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/lsp"
)

func TestDirectoryInstructionsAgentStepAcknowledgesMutation(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	directoryTestFile(t, env.workingDir, "nested/AGENTS.md", "nested special procedure")
	tracker := NewDirectoryInstructions(env.workingDir, nil)
	var executions atomic.Int32
	wrapped := tracker.WrapTools([]fantasy.AgentTool{directoryTestTool("write", &executions)})
	large := newScriptedModel(
		scriptedTurn{calls: []scriptedCall{{name: "write", input: map[string]any{"file_path": "nested/new.go", "content": "package nested"}}}},
		scriptedTurn{calls: []scriptedCall{{name: "write", input: map[string]any{"file_path": "nested/new.go", "content": "package nested"}}}},
		scriptedTurn{text: "done"},
	)
	sa := testSessionAgent(env, large, textModel("title"), "system", wrapped...).(*sessionAgent)
	sa.directoryInstructions = tracker
	sess, err := env.sessions.Create(t.Context(), "directory integration")
	require.NoError(t, err)
	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "write the file"})
	require.NoError(t, err)
	require.Equal(t, int32(1), executions.Load())
	require.Len(t, large.sentCalls(), 3)
}

func TestWorkspaceToolsResolveLSPPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inner := &fakeTool{name: tools.LSPToolName, resp: fantasy.NewTextResponse("ok")}
	tool := workspaceTools([]fantasy.AgentTool{inner}, root)[0]
	_, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"definition","symbol":"Foo"}`})
	require.NoError(t, err)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(inner.input), &input))
	require.Equal(t, root, input["path"])
	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"symbols","file_path":"main.go"}`})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(inner.input), &input))
	require.Equal(t, filepath.Join(root, "main.go"), input["file_path"])
}

func TestWorkspaceBuildToolsUseChildRoot(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	coord := newTestCoordinator(t, env, "p", config.ProviderConfig{ID: "p"})
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "note.txt"), []byte("child file"), 0o600))
	workspace := coord.newAgentWorkspace(root)
	t.Cleanup(workspace.Close)
	require.NotSame(t, coord.lspManager, workspace.manager)
	built, err := coord.buildTools(t.Context(), config.Agent{ID: config.AgentTask, AllowedTools: []string{"view"}}, true, workspace)
	require.NoError(t, err)
	require.Len(t, built, 1)
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, "child")
	response, err := built[0].Run(ctx, fantasy.ToolCall{Input: `{"file_path":"note.txt"}`})
	require.NoError(t, err)
	require.False(t, response.IsError, response.Content)
	require.Contains(t, response.Content, "child file")
	require.Equal(t, root, workspace.store.WorkingDir())
	require.Equal(t, env.workingDir, coord.cfg.WorkingDir())
}

func TestIsolatedDispatchFinishRetainsPatchAndMetadata(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	worktree, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	store := config.NewTestStoreWithWorkingDir(&config.Config{Options: &config.Options{}}, worktree.Path)
	dispatch := &isolatedDispatch{worktree: worktree, workspace: &agentWorkspace{store: store, manager: lsp.NewManager(store)}}
	worktreeTestWrite(t, worktree.Path, "tracked", "changed by child\n")
	response := fantasy.NewTextResponse("finished")
	dispatch.finish(&response)
	require.Contains(t, response.Content, "have not been merged")
	var meta struct {
		Worktree WorktreeResult `json:"worktree"`
	}
	require.NoError(t, json.Unmarshal([]byte(response.Metadata), &meta))
	require.True(t, meta.Worktree.Preserved)
	require.FileExists(t, meta.Worktree.PatchPath)
	original, err := os.ReadFile(filepath.Join(root, "tracked"))
	require.NoError(t, err)
	require.Equal(t, "committed\n", string(original))
}
