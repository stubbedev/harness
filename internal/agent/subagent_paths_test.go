package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestMissingCitedPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "internal/ui/chat"), 0o755))
	writeFile := func(rel string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte("x"), 0o644))
	}
	writeFile("internal/ui/chat/messages.go")
	writeFile("internal/ui/chat/tools.go")
	writeFile("go.mod")

	report := strings.Join([]string{
		"Found it in internal/ui/chat/messages.go:45 and `internal/ui/chat/tools.go:281`.",
		"The package internal/coach/ui.go does not exist, nor does internal/coach/schemas/*.go,",
		"but internal/ui/chat/ and go.mod are real. See https://example.com/x.go and",
		"fast/task, read/write, Pre/PostToolUse for prose that must never match.",
	}, " ")

	missing := missingCitedPaths(report, dir)

	require.Equal(t, []string{
		"internal/coach/ui.go",
		"internal/coach/schemas/*.go",
	}, missing)
}

func TestWarnOnFabricatedPaths(t *testing.T) {
	t.Parallel()

	c := newTestCoordinator(t, testEnv(t), "p", providerCfgP)
	root := c.cfg.WorkingDir()
	require.NotEmpty(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, "real.go"), []byte("x"), 0o644))

	t.Run("appends a warning listing the missing paths", func(t *testing.T) {
		resp := fantasy.NewTextResponse("Map done: real.go and internal/coach/ui.go.")
		c.warnOnFabricatedPaths(subAgentParams{AgentName: "fast"}, &resp)
		require.False(t, resp.IsError)
		require.Contains(t, resp.Content, "real.go")
		require.Contains(t, resp.Content, "internal/coach/ui.go")
		require.Contains(t, resp.Content, "## Warning: paths cited by this sub-agent do not exist")
		require.Contains(t, resp.Content, "verify with ls or glob")
	})

	t.Run("leaves grounded reports untouched", func(t *testing.T) {
		resp := fantasy.NewTextResponse("Map done: real.go.")
		c.warnOnFabricatedPaths(subAgentParams{AgentName: "fast"}, &resp)
		require.NotContains(t, resp.Content, "## Warning")
	})

	t.Run("skips research dispatches and error responses", func(t *testing.T) {
		resp := fantasy.NewTextResponse("saved to page-123.md, see internal/ghost/x.go")
		c.warnOnFabricatedPaths(subAgentParams{AgentName: "research"}, &resp)
		require.NotContains(t, resp.Content, "## Warning")

		errResp := fantasy.NewTextErrorResponse("boom")
		c.warnOnFabricatedPaths(subAgentParams{AgentName: "fast"}, &errResp)
		require.Equal(t, "boom", errResp.Content)
	})
}
