package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/hooks"
)

// fakeMCPTool is a tool that reports a server, the way the real MCP tool
// wrapper does.
type fakeMCPTool struct {
	fakeTool
	server string
}

func newFakeMCPTool(name, server string) *fakeMCPTool {
	tool := &fakeMCPTool{server: server}
	tool.name = name
	return tool
}

func (f *fakeMCPTool) MCP() string { return f.server }

func TestLiveMCPServers(t *testing.T) {
	t.Parallel()

	tools := []fantasy.AgentTool{
		&fakeTool{name: "shell"},
		newFakeMCPTool("mcp_sentry_get_issue", "sentry"),
		&mcpSearchTool{server: "jenkins"},
	}

	live := liveMCPServers(tools)
	require.True(t, live["sentry"], "a server with one of its own tools is live")
	require.False(t, live["jenkins"], "a server standing behind its search stub is not live")
	require.False(t, live["shell"], "a non-MCP tool contributes no server")
}

// TestLiveMCPServersSeesThroughHooks covers the reason hookedTool forwards
// MCP(): with any tool hook configured every tool in the list is wrapped, and
// a wrapper that swallowed the server name would make every server look
// deferred and silently drop every server's instructions.
func TestLiveMCPServersSeesThroughHooks(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: "exit 0"}},
	})
	wrapped := wrapToolsWithHooks([]fantasy.AgentTool{
		newFakeMCPTool("mcp_sentry_get_issue", "sentry"),
	}, registry, nil)
	require.IsType(t, &hookedTool{}, wrapped[0], "the hook registry must actually wrap")

	require.True(t, liveMCPServers(wrapped)["sentry"])
}

func TestHookedToolMCPIsEmptyForPlainTools(t *testing.T) {
	t.Parallel()

	tool := newHookedTool(&fakeTool{name: "shell"}, nil, nil)
	require.Empty(t, tool.MCP())
}

// TestDecoratorsForwardMCP pushes an MCP tool through every palette-wide
// wrapper, stacked the way the agent installs them, and requires the server
// name to survive. A wrapper that swallowed it would make the server look
// deferred and silently drop its instructions.
func TestDecoratorsForwardMCP(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: "exit 0"}},
	})
	wrapped := []fantasy.AgentTool{newFakeMCPTool("mcp_sentry_get_issue", "sentry")}
	wrapped = wrapToolsWithHooks(wrapped, registry, nil)
	wrapped = wrapToolsResilient(wrapped)
	wrapped = withResultCap(wrapped)

	require.True(t, liveMCPServers(wrapped)["sentry"])
}

// TestToolWrappersEmbedToolDecorator holds every tool wrapper in the
// package to embedding toolDecorator rather than a bare fantasy.AgentTool,
// so none can forget to forward MCP() and the other optional methods.
func TestToolWrappersEmbedToolDecorator(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok || spec.Name.Name == "toolDecorator" {
				return true
			}
			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				sel, ok := field.Type.(*ast.SelectorExpr)
				if len(field.Names) == 0 && ok && sel.Sel.Name == "AgentTool" {
					t.Errorf("%s: %s embeds fantasy.AgentTool directly; embed toolDecorator instead", fset.Position(spec.Pos()), spec.Name.Name)
				}
			}
			return true
		})
	}
}
