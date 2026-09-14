package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
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

	inner := &fakeTool{name: "shell"}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: `echo "blocked" >&2; exit 2`}},
	})
	tool := newHookedTool(inner, registry)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-3", Name: "shell"})
	require.NoError(t, err)
	require.False(t, inner.called, "denied call must not reach the inner tool")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "blocked")
}

func TestHookedTool_PreToolUseRewritesInput(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "shell"}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: `echo '{"updated_input":{"command":"deno test"}}'`}},
	})
	tool := newHookedTool(inner, registry)

	_, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "shell", Input: `{"command":"npm test","timeout":60000}`})
	require.NoError(t, err)
	require.True(t, inner.called)
	require.JSONEq(t, `{"command":"deno test","timeout":60000}`, inner.input)
}

func TestHookedTool_PostToolUseContext(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "shell", resp: fantasy.NewTextResponse("ran fine")}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPostToolUse: {{Command: `echo '{"context":"remember gofumpt"}'`}},
	})
	tool := newHookedTool(inner, registry)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "shell"})
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

	inner := &fakeTool{name: "shell", resp: fantasy.NewTextResponse("done")}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPostToolUse: {{Command: `echo '{"halt":true,"reason":"enough for today"}'`}},
	})
	tool := newHookedTool(inner, registry)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "shell"})
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

// TestHookedTool_ToolInputArrivesAsObject pins the payload contract of
// the tool events end to end: a hook process reading stdin sees
// tool_input as a parsed JSON object (not the raw string the model
// sent), PostToolUse sees the input after any PreToolUse rewrite along
// with the completed tool_response, and PreToolUse sees it before.
func TestHookedTool_ToolInputArrivesAsObject(t *testing.T) {
	t.Parallel()

	preLog := filepath.Join(t.TempDir(), "pre.log")
	postLog := filepath.Join(t.TempDir(), "post.log")
	inner := &fakeTool{name: "shell", resp: fantasy.NewTextResponse("ran fine")}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{
			Command: captureHookCmd(preLog, `printf '%s' '{"updated_input":{"command":"deno test"}}'`),
		}},
		hooks.EventPostToolUse: {{Command: captureHookCmd(postLog, "")}},
	})
	tool := newHookedTool(inner, registry)

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{
		ID:    "c1",
		Name:  "shell",
		Input: `{"command":"npm test","timeout":60000}`,
	})
	require.NoError(t, err)
	require.True(t, inner.called)
	require.Contains(t, resp.Content, "ran fine")

	pre := readHookPayloads(t, preLog)
	require.Len(t, pre, 1)
	require.Equal(t, hooks.EventPreToolUse, pre[0]["event"])
	require.Equal(t, "shell", pre[0]["tool_name"])
	require.JSONEq(t, `{"command":"npm test","timeout":60000}`,
		string(mustJSON(t, pre[0]["tool_input"])),
		"PreToolUse must see the input as the model sent it, as an object")

	post := readHookPayloads(t, postLog)
	require.Len(t, post, 1)
	require.Equal(t, hooks.EventPostToolUse, post[0]["event"])
	require.JSONEq(t, `{"command":"deno test","timeout":60000}`,
		string(mustJSON(t, post[0]["tool_input"])),
		"PostToolUse must see the input the tool ran with, after the PreToolUse rewrite")
	toolResponse, ok := post[0]["tool_response"].(map[string]any)
	require.True(t, ok, "tool_response must be an object")
	require.Equal(t, "ran fine", toolResponse["content"])
	require.Equal(t, false, toolResponse["is_error"])

	// The rewrite actually reached the tool.
	require.JSONEq(t, `{"command":"deno test","timeout":60000}`, inner.input)
}

// TestHookedTool_MatcherFiltersByToolName verifies the matcher against
// the tool name at the fire site: a hook scoped to bash must not run for
// an edit call.
func TestHookedTool_MatcherFiltersByToolName(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "hooks.log")
	inner := &fakeTool{name: "edit", resp: fantasy.NewTextResponse("edited")}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{
			Matcher: "^shell$",
			Command: captureHookCmd(logPath, ""),
		}},
	})
	tool := newHookedTool(inner, registry)

	_, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "edit", Input: `{"file_path":"a.go"}`})
	require.NoError(t, err)
	require.True(t, inner.called, "a non-matching hook must not block the call")
	require.Empty(t, readHookPayloads(t, logPath),
		"a hook whose matcher does not match the tool name must not run")
}

// mustJSON re-marshals a decoded payload fragment so assertions can use
// JSONEq against the captured document.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

// TestHookedTool_PreToolUseHaltEndsTurn pins that a PreToolUse halt
// both blocks the call and ends the whole turn, where a plain deny only
// blocks the call so the model can try something else.
func TestHookedTool_PreToolUseHaltEndsTurn(t *testing.T) {
	t.Parallel()

	halting := &fakeTool{name: "shell"}
	haltRegistry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: `echo '{"halt":true,"reason":"enough"}'`}},
	})
	haltResp, err := newHookedTool(halting, haltRegistry).Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "shell"})
	require.NoError(t, err)
	require.False(t, halting.called)
	require.True(t, haltResp.StopTurn, "a PreToolUse halt must end the turn")
	require.True(t, haltResp.IsError)
	require.Contains(t, haltResp.Content, "enough")

	denying := &fakeTool{name: "shell"}
	denyRegistry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: `echo "blocked" >&2; exit 2`}},
	})
	denyResp, err := newHookedTool(denying, denyRegistry).Run(t.Context(), fantasy.ToolCall{ID: "c1", Name: "shell"})
	require.NoError(t, err)
	require.False(t, denying.called)
	require.False(t, denyResp.StopTurn, "a plain PreToolUse deny blocks only this call")
	require.True(t, denyResp.IsError)
}
