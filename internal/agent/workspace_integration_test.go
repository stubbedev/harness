package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	// The allowlisted view tool, plus the plumbing timer tool every
	// agent gets whatever its allow list says.
	require.Len(t, built, 2)
	view := findTool(t, built, tools.ViewToolName)
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, "child")
	response, err := view.Run(ctx, fantasy.ToolCall{Input: `{"file_path":"note.txt"}`})
	require.NoError(t, err)
	require.False(t, response.IsError, response.Content)
	require.Contains(t, response.Content, "child file")
	require.Equal(t, root, workspace.store.WorkingDir())
	require.Equal(t, env.workingDir, coord.cfg.WorkingDir())
}

func testIsolatedDispatch(t *testing.T, root string) *isolatedDispatch {
	t.Helper()
	worktree, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	store := config.NewTestStoreWithWorkingDir(&config.Config{Options: &config.Options{}}, worktree.Path)
	return &isolatedDispatch{worktree: worktree, workspace: &agentWorkspace{store: store, manager: lsp.NewManager(store)}}
}

func finishIsolatedDispatch(t *testing.T, dispatch *isolatedDispatch) (fantasy.ToolResponse, WorktreeResult) {
	t.Helper()
	response := fantasy.NewTextResponse("finished")
	dispatch.finish(&response)
	var meta struct {
		Worktree WorktreeResult `json:"worktree"`
	}
	require.NoError(t, json.Unmarshal([]byte(response.Metadata), &meta))
	return response, meta.Worktree
}

func TestIsolatedDispatchPromptAllowsCommitsOnTheWorktreeBranch(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	dispatch := testIsolatedDispatch(t, root)
	t.Cleanup(func() { dispatch.finish(nil) })
	prompt := dispatch.prompt(root, "fix it and commit")
	require.True(t, strings.HasSuffix(prompt, "\n\nfix it and commit"))
	require.Contains(t, prompt, dispatch.worktree.Path)
	require.Contains(t, prompt, "parent checkout "+root)
	require.Contains(t, prompt, "retained on branch "+dispatch.worktree.Branch)
	require.NotContains(t, strings.ToLower(prompt), "committing")
	require.NotContains(t, strings.ToLower(prompt), "not commit")
}

func TestIsolatedDispatchFinishReportsCommitsForCherryPick(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	base := strings.TrimSpace(worktreeTestGit(t, root, "rev-parse", "HEAD"))
	dispatch := testIsolatedDispatch(t, root)
	child := dispatch.worktree.Path
	worktreeTestWrite(t, child, "tracked", "first\n")
	worktreeTestGit(t, child, "commit", "-am", "fix: first")
	first := strings.TrimSpace(worktreeTestGit(t, child, "rev-parse", "HEAD"))
	worktreeTestWrite(t, child, "tracked", "second\n")
	worktreeTestGit(t, child, "commit", "-am", "fix: second")
	second := strings.TrimSpace(worktreeTestGit(t, child, "rev-parse", "HEAD"))
	worktreeTestWrite(t, child, "untracked", "pending\n")

	response, result := finishIsolatedDispatch(t, dispatch)
	require.Contains(t, response.Content, "branch "+dispatch.worktree.Branch)
	require.Contains(t, response.Content, "- "+first+" fix: first\n- "+second+" fix: second")
	require.Contains(t, response.Content, "git cherry-pick "+first+" "+second)
	require.Equal(t, base, result.Base)
	require.Equal(t, []WorktreeCommit{{Hash: first, Subject: "fix: first"}, {Hash: second, Subject: "fix: second"}}, result.Commits)
	require.FileExists(t, result.PatchPath)
	require.Equal(t, base, strings.TrimSpace(worktreeTestGit(t, root, "rev-parse", "HEAD")))

	worktreeTestGit(t, root, "cherry-pick", first, second)
	picked, err := os.ReadFile(filepath.Join(root, "tracked"))
	require.NoError(t, err)
	require.Equal(t, "second\n", string(picked))
}

func TestIsolatedDispatchFinishRetainsPatchAndMetadata(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	dispatch := testIsolatedDispatch(t, root)
	worktreeTestWrite(t, dispatch.worktree.Path, "tracked", "changed by child\n")
	response, result := finishIsolatedDispatch(t, dispatch)
	require.Contains(t, response.Content, "Nothing has been merged")
	require.NotContains(t, response.Content, "cherry-pick")
	require.Empty(t, result.Commits)
	require.False(t, result.Removed)
	require.FileExists(t, result.PatchPath)
	original, err := os.ReadFile(filepath.Join(root, "tracked"))
	require.NoError(t, err)
	require.Equal(t, "committed\n", string(original))
}
