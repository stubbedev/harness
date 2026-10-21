package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func writeCitedFiles(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		target := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, []byte("x"), 0o644))
	}
}

func TestMissingCitedPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCitedFiles(t, dir, "internal/ui/chat/messages.go", "internal/ui/chat/tools.go", "go.mod")

	report := strings.Join([]string{
		"Found it in internal/ui/chat/messages.go:45 and `internal/ui/chat/tools.go:281`.",
		"The package internal/coach/ui.go does not exist, nor does internal/coach/schemas/*.go,",
		"but internal/ui/chat/ and go.mod are real. See https://example.com/x.go and",
		"fast/task, read/write, Pre/PostToolUse for prose that must never match.",
	}, " ")

	missing := missingCitedPaths(report, []string{dir})

	require.Equal(t, []string{
		"internal/coach/ui.go",
		"internal/coach/schemas/*.go",
	}, missing)
}

func TestMissingCitedPathsNeverProbesOutsideTheRoots(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCitedFiles(t, dir, "real.go")

	report := strings.Join([]string{
		filepath.Join(dir, "real.go"),
		filepath.Join(dir, "ghost.go"),
		"/usr/share/dict/words.txt",
		"../../outside/x.go",
	}, " ")

	require.Equal(t, []string{filepath.Join(dir, "ghost.go")}, missingCitedPaths(report, []string{dir}))
}

func TestMissingCitedPathsReportShapes(t *testing.T) {
	t.Parallel()

	main := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "harness-worktree-123", "worktree")
	writeCitedFiles(t, main,
		"internal/workspace/workspace.go",
		"internal/client/errors.go",
		"internal/server/proto.go",
		"internal/server/config.go",
		"internal/ui/chat/assistant.go",
		"internal/ui/model/shell_prefix_test.go",
	)
	writeCitedFiles(t, worktree, "internal/agent/created_in_worktree.go")
	roots := []string{main, worktree}

	cases := []struct {
		name   string
		report string
		want   []string
	}{
		{"backticks with trailing text", "The type lives in `" + main + "/internal/workspace/workspace.go` and is exported.", nil},
		{"backticks inside parentheses", "(`" + main + "/internal/workspace/workspace.go`).", nil},
		{"backticks with possessive", "`" + main + "/internal/workspace/workspace.go`'s constructor", nil},
		{"backticks with line range", "`" + main + "/internal/workspace/workspace.go:12-40`", nil},
		{"anchor", main + "/internal/workspace/workspace.go#L12-L40", nil},
		{"bold and backticks", "**`" + main + "/internal/client/errors.go`**", nil},
		{"bold and backticks with colon", "**`" + main + "/internal/client/errors.go`**:", nil},
		{"comma list inside code span", "`internal/server/proto.go, internal/server/config.go`**", nil},
		{"comma list without space", "`internal/server/proto.go,internal/server/config.go`**", nil},
		{"relative to a package dir", "Edited chat/assistant.go and model/shell_prefix_test.go.", nil},
		{"own worktree", "Added `" + worktree + "/internal/agent/created_in_worktree.go`.", nil},
		{"worktree path of a main checkout file", worktree + "/internal/server/proto.go", nil},
		{"fixture name as data", "The test writes `\"sub/secret.txt\"` and checks sub/secret.txt is skipped.", nil},
		{"missing repo path in emphasis", "**`internal/ghost/x.go`**,", []string{"internal/ghost/x.go"}},
		{"missing absolute path in main", "`" + main + "/internal/ghost.go:3`", []string{main + "/internal/ghost.go"}},
		{"missing absolute path in worktree", worktree + "/internal/ghost.go", []string{worktree + "/internal/ghost.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, missingCitedPaths(tc.report, roots))
		})
	}
}

func TestWarnOnFabricatedPaths(t *testing.T) {
	t.Parallel()

	c := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	root := c.cfg.WorkingDir()
	require.NotEmpty(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, "real.go"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "internal"), 0o755))
	worktree := t.TempDir()
	writeCitedFiles(t, worktree, "internal/new.go")

	t.Run("appends a warning listing the missing paths", func(t *testing.T) {
		t.Parallel()
		resp := fantasy.NewTextResponse("Map done: real.go and internal/coach/ui.go.")
		c.warnOnFabricatedPaths(subAgentParams{AgentName: "fast"}, &resp)
		require.False(t, resp.IsError)
		require.Contains(t, resp.Content, "real.go")
		require.Contains(t, resp.Content, "internal/coach/ui.go")
		require.Contains(t, resp.Content, "## Warning: paths cited by this sub-agent do not exist")
		require.Contains(t, resp.Content, "verify with ls or glob")
	})

	t.Run("leaves grounded reports untouched", func(t *testing.T) {
		t.Parallel()
		resp := fantasy.NewTextResponse("Map done: real.go.")
		c.warnOnFabricatedPaths(subAgentParams{AgentName: "fast"}, &resp)
		require.NotContains(t, resp.Content, "## Warning")
	})

	t.Run("resolves against the isolated checkout", func(t *testing.T) {
		t.Parallel()
		report := "Created internal/new.go and " + filepath.Join(worktree, "internal", "new.go") + "."
		resp := fantasy.NewTextResponse(report)
		c.warnOnFabricatedPaths(subAgentParams{AgentName: "fast", WorkspaceRoot: worktree}, &resp)
		require.Equal(t, report, resp.Content)
	})

	t.Run("skips research dispatches and error responses", func(t *testing.T) {
		t.Parallel()
		resp := fantasy.NewTextResponse("saved to page-123.md, see internal/ghost/x.go")
		c.warnOnFabricatedPaths(subAgentParams{AgentName: "research"}, &resp)
		require.NotContains(t, resp.Content, "## Warning")

		errResp := fantasy.NewTextErrorResponse("boom")
		c.warnOnFabricatedPaths(subAgentParams{AgentName: "fast"}, &errResp)
		require.Equal(t, "boom", errResp.Content)
	})
}
