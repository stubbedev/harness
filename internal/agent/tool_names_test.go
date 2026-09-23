package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/extensions"
)

// nameOnlyTool is a stand-in for a tool whose name is all that matters
// to the check under test.
type nameOnlyTool struct{ name string }

func (n nameOnlyTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: n.name, Required: []string{}}
}
func (nameOnlyTool) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (nameOnlyTool) SetProviderOptions(fantasy.ProviderOptions) {}
func (nameOnlyTool) Run(_ context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.NewTextResponse(""), nil
}

func TestValidateToolNames(t *testing.T) {
	t.Parallel()

	longName := strings.Repeat("a", maxToolNameLen+1)
	in := []fantasy.AgentTool{
		nameOnlyTool{"view"},
		nameOnlyTool{"mcp_server_do-thing"},
		nameOnlyTool{"view"},               // duplicate: the built-in above keeps it
		nameOnlyTool{"bad name!"},          // spaces and punctuation
		nameOnlyTool{"mcp_my.server_read"}, // a dotted MCP server key
		nameOnlyTool{longName},
		nameOnlyTool{""},
	}

	got := make([]string, 0, len(in))
	for _, tool := range validateToolNames(in) {
		got = append(got, tool.Info().Name)
	}
	assert.Equal(t, []string{"view", "mcp_server_do-thing"}, got)
}

// TestBuildToolsRejectsUnusableExtensionNames pins the end-to-end
// behaviour: an extension can register any non-empty string as a tool
// name, including one a built-in already owns. Both used to reach the
// provider -- a duplicate function name and an illegal one -- which
// fails the whole request, not just that tool, on every turn.
func TestBuildToolsRejectsUnusableExtensionNames(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "shadow")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "init.lua"), []byte(`
harness.register_tool({ name = "view", description = "shadows a built-in", handler = function() return "" end })
harness.register_tool({ name = "bad name!", description = "illegal", handler = function() return "" end })
harness.register_tool({ name = "fine_tool", description = "usable", handler = function() return "" end })
`), 0o644))

	host := extensions.New(t.Context(), extensions.Options{Paths: []string{root}, WorkingDir: t.TempDir()})
	t.Cleanup(host.Close)
	require.Len(t, host.Tools(), 3, "the host registers all three; the filtering is buildTools' job")

	env := testEnv(t)
	coord := newTestCoordinator(t, env, "p", config.ProviderConfig{ID: "p"})
	coord.extensions = host

	built, err := coord.buildTools(t.Context(), coord.cfg.Config().Agents[config.AgentCoder], false, nil)
	require.NoError(t, err)

	counts := map[string]int{}
	for _, tool := range built {
		counts[tool.Info().Name]++
	}
	assert.Equal(t, 1, counts["view"], "the built-in view keeps the name it owns")
	assert.Zero(t, counts["bad name!"], "an illegal name never reaches the provider")
	assert.Equal(t, 1, counts["fine_tool"], "a usable extension tool is untouched")
}
