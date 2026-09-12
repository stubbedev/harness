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
	gotCtx context.Context
	resp   fantasy.ToolResponse
}

func (f *fakeTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: f.name}
}

func (f *fakeTool) Run(ctx context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	f.called = true
	f.gotCtx = ctx
	return f.resp, nil
}

func (f *fakeTool) ProviderOptions() fantasy.ProviderOptions     { return nil }
func (f *fakeTool) SetProviderOptions(_ fantasy.ProviderOptions) {}

// newRunner builds a hooks.Runner from a single HookConfig, running the
// config-loader path that compiles the matcher regex.
func newRunner(t *testing.T, cmd string) *hooks.Runner {
	t.Helper()
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			hooks.EventPreToolUse: {{Command: cmd}},
		},
	}
	require.NoError(t, cfg.ValidateHooks())
	return hooks.NewRunner(cfg.Hooks[hooks.EventPreToolUse], t.TempDir(), t.TempDir())
}

func TestHookedTool_DenySkipsInnerTool(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "bash"}
	runner := newRunner(t, `echo "blocked" >&2; exit 2`)
	tool := newHookedTool(inner, runner)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-3", Name: "bash"})
	require.NoError(t, err)
	require.False(t, inner.called, "denied call must not reach the inner tool")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "blocked")
}

func TestWrapToolsWithHooks(t *testing.T) {
	t.Parallel()

	runner := newRunner(t, `exit 0`)
	inputs := []fantasy.AgentTool{&fakeTool{name: "a"}, &fakeTool{name: "b"}}

	t.Run("top-level agent wraps every tool", func(t *testing.T) {
		t.Parallel()
		out := wrapToolsWithHooks(inputs, runner, false)
		require.Len(t, out, len(inputs))
		for i, tool := range out {
			_, ok := tool.(*hookedTool)
			require.Truef(t, ok, "tool %d should be a *hookedTool", i)
		}
	})

	t.Run("sub-agent skips the wrap", func(t *testing.T) {
		t.Parallel()
		out := wrapToolsWithHooks(inputs, runner, true)
		require.Equal(t, inputs, out, "sub-agent tools should be returned unwrapped")
		for _, tool := range out {
			_, isHooked := tool.(*hookedTool)
			require.False(t, isHooked, "sub-agent tool should not be wrapped")
		}
	})

	t.Run("nil runner skips the wrap for both agent kinds", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, inputs, wrapToolsWithHooks(inputs, nil, false))
		require.Equal(t, inputs, wrapToolsWithHooks(inputs, nil, true))
	})
}
