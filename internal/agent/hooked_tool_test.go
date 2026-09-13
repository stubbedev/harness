package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/hooks"
)

// fakeTool records the context it was invoked with so tests can assert on
// values stamped onto it by the hookedTool decorator.
type fakeTool struct {
	name   string
	called bool
	input  string
	resp   fantasy.ToolResponse
}

func (f *fakeTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: f.name}
}

func (f *fakeTool) Run(_ context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	f.called = true
	f.input = call.Input
	return f.resp, nil
}

func (f *fakeTool) ProviderOptions() fantasy.ProviderOptions     { return nil }
func (f *fakeTool) SetProviderOptions(_ fantasy.ProviderOptions) {}

// newTestRegistry builds a hooks.Registry over a test config store so hook
// commands run through the same config-loader path as production.
func newTestRegistry(t *testing.T, byEvent map[string][]config.HookConfig) *hooks.Registry {
	t.Helper()
	cfg := &config.Config{Hooks: byEvent}
	require.NoError(t, cfg.ValidateHooks())
	return hooks.NewRegistry(config.NewTestStore(cfg), t.TempDir(), t.TempDir())
}

func TestHookedTool_DenySkipsInnerTool(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "bash"}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: `echo "blocked" >&2; exit 2`}},
	})
	tool := newHookedTool(inner, registry)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-3", Name: "bash"})
	require.NoError(t, err)
	require.False(t, inner.called, "denied call must not reach the inner tool")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "blocked")
}

func TestHookedTool_PreToolUseRewritesInput(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "bash"}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: `echo '{"updated_input":{"command":"deno test"}}'`}},
	})
	tool := newHookedTool(inner, registry)

	_, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "bash", Input: `{"command":"npm test","timeout":60000}`})
	require.NoError(t, err)
	require.True(t, inner.called)
	require.JSONEq(t, `{"command":"deno test","timeout":60000}`, inner.input)
}

func TestHookedTool_PostToolUseContext(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "bash", resp: fantasy.NewTextResponse("ran fine")}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPostToolUse: {{Command: `echo '{"context":"remember gofumpt"}'`}},
	})
	tool := newHookedTool(inner, registry)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "bash"})
	require.NoError(t, err)
	require.True(t, inner.called)
	require.Contains(t, resp.Content, "ran fine")
	require.Contains(t, resp.Content, "remember gofumpt")
	require.False(t, resp.StopTurn)
}

func TestHookedTool_PostToolUseDenyAppendsFeedback(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "write", resp: fantasy.NewTextResponse("wrote 3 lines")}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPostToolUse: {{Command: `echo "run the linter" >&2; exit 2`}},
	})
	tool := newHookedTool(inner, registry)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "write"})
	require.NoError(t, err)
	require.True(t, inner.called, "PostToolUse deny cannot un-run the tool")
	require.Contains(t, resp.Content, "wrote 3 lines")
	require.Contains(t, resp.Content, "Hook feedback: run the linter")
	require.False(t, resp.IsError, "feedback is appended, the response is not converted to an error")
	require.False(t, resp.StopTurn)
}

func TestHookedTool_PostToolUseHaltStopsTurn(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "bash", resp: fantasy.NewTextResponse("done")}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPostToolUse: {{Command: `echo '{"halt":true,"reason":"enough for today"}'`}},
	})
	tool := newHookedTool(inner, registry)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "bash"})
	require.NoError(t, err)
	require.True(t, resp.StopTurn)
	require.Contains(t, resp.Content, "enough for today")
}

func TestWrapToolsWithHooks(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: `exit 0`}},
	})
	inputs := []fantasy.AgentTool{&fakeTool{name: "a"}, &fakeTool{name: "b"}}

	t.Run("wraps every tool", func(t *testing.T) {
		t.Parallel()
		out := wrapToolsWithHooks(inputs, registry)
		require.Len(t, out, len(inputs))
		for i, tool := range out {
			_, ok := tool.(*hookedTool)
			require.Truef(t, ok, "tool %d should be a *hookedTool", i)
		}
	})

	t.Run("nil registry skips the wrap", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, inputs, wrapToolsWithHooks(inputs, nil))
	})

	t.Run("registry without tool events skips the wrap", func(t *testing.T) {
		t.Parallel()
		stopOnly := newTestRegistry(t, map[string][]config.HookConfig{
			hooks.EventStop: {{Command: `exit 0`}},
		})
		require.Equal(t, inputs, wrapToolsWithHooks(inputs, stopOnly))
	})

	t.Run("PostToolUse-only registry still wraps", func(t *testing.T) {
		t.Parallel()
		postOnly := newTestRegistry(t, map[string][]config.HookConfig{
			hooks.EventPostToolUse: {{Command: `exit 0`}},
		})
		out := wrapToolsWithHooks(inputs, postOnly)
		for _, tool := range out {
			_, ok := tool.(*hookedTool)
			require.True(t, ok)
		}
	})
}

// TestBuildTools_WrapsSubAgentTools pins the policy that sub-agents are
// hookable the same way the top-level agent is: they call the same toolset,
// so a PreToolUse rule has to see their calls to mean anything.
func TestBuildTools_WrapsSubAgentTools(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	coord := newTestCoordinator(t, env, "p", config.ProviderConfig{ID: "p"})
	coord.hooks = newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: `exit 0`}},
	})

	agentCfg := config.Agent{ID: config.AgentTask, Name: "Task", AllowedTools: []string{"glob", "grep", "view"}}
	toolsList, err := coord.buildTools(t.Context(), agentCfg, true, "")
	require.NoError(t, err)
	require.NotEmpty(t, toolsList)
	for _, tool := range toolsList {
		_, hooked := tool.(*hookedTool)
		require.Truef(t, hooked, "sub-agent tool %s should fire hooks", tool.Info().Name)
	}
}
