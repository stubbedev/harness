package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/subagents"
)

func TestBuildAgentDispatchInfo_NoSubagents(t *testing.T) {
	t.Parallel()

	info := buildAgentDispatchInfo(nil)

	require.Equal(t, "agent", info.Name)
	require.True(t, info.Parallel)
	require.Contains(t, info.Required, "prompt")

	subagentTypeParam, ok := info.Parameters["subagent_type"]
	require.True(t, ok, "Parameters should have a subagent_type key")

	paramMap, ok := subagentTypeParam.(map[string]any)
	require.True(t, ok, "subagent_type parameter should be a map[string]any")

	enum, ok := paramMap["enum"]
	require.True(t, ok, "subagent_type parameter should have an enum key")

	enumSlice, ok := enum.([]string)
	require.True(t, ok, "enum should be a []string")
	require.Contains(t, enumSlice, "task")
}

func TestBuildAgentDispatchInfo_WithSubagents(t *testing.T) {
	t.Parallel()

	activeSubagents := []*subagents.Subagent{
		{Name: "code-reviewer", Description: "Reviews code"},
		{Name: "tester", Description: "Writes tests"},
	}

	info := buildAgentDispatchInfo(activeSubagents)

	subagentTypeParam, ok := info.Parameters["subagent_type"]
	require.True(t, ok, "Parameters should have a subagent_type key")

	paramMap, ok := subagentTypeParam.(map[string]any)
	require.True(t, ok, "subagent_type parameter should be a map[string]any")

	enum, ok := paramMap["enum"]
	require.True(t, ok, "subagent_type parameter should have an enum key")

	enumSlice, ok := enum.([]string)
	require.True(t, ok, "enum should be a []string")
	require.Contains(t, enumSlice, "task")
	require.Contains(t, enumSlice, "code-reviewer")
	require.Contains(t, enumSlice, "tester")

	// subagent descriptions should appear in the subagent_type parameter description
	desc, ok := paramMap["description"]
	require.True(t, ok, "subagent_type parameter should have a description key")
	descStr, ok := desc.(string)
	require.True(t, ok, "description should be a string")
	require.Contains(t, descStr, "Reviews code")
	require.Contains(t, descStr, "Writes tests")
}

func TestBuildAgentDispatchInfo_PromptRequired(t *testing.T) {
	t.Parallel()

	info := buildAgentDispatchInfo(nil)

	require.Contains(t, info.Required, "prompt")

	// subagent_type is optional — should NOT appear in Required
	for _, r := range info.Required {
		require.NotEqual(t, "subagent_type", r, "subagent_type should not be required")
	}
}

// dispatcherTool tests — exercise the struct's Run and Info methods without a
// full coordinator. The dispatch closure is injected so no provider setup needed.

func TestDispatcherTool_Info_ReturnsBuildInfo(t *testing.T) {
	t.Parallel()

	info := buildAgentDispatchInfo([]*subagents.Subagent{{Name: "my-agent", Description: "Does stuff"}})
	dt := &dispatcherTool{info: info}

	got := dt.Info()
	require.Equal(t, "agent", got.Name)
	require.True(t, got.Parallel)
}

func TestDispatcherTool_Run_ParsesJSONAndCallsDispatch(t *testing.T) {
	t.Parallel()

	var capturedParams AgentDispatchParams
	dt := &dispatcherTool{
		info: buildAgentDispatchInfo(nil),
		dispatch: func(_ context.Context, params AgentDispatchParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			capturedParams = params
			return fantasy.NewTextResponse("ok"), nil
		},
	}

	input, _ := json.Marshal(AgentDispatchParams{SubagentType: "my-agent", Prompt: "do the thing"})
	resp, err := dt.Run(context.Background(), fantasy.ToolCall{Input: string(input)})

	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, "my-agent", capturedParams.SubagentType)
	require.Equal(t, "do the thing", capturedParams.Prompt)
}

func TestDispatcherTool_Run_InvalidJSON_ReturnsErrorResponse(t *testing.T) {
	t.Parallel()

	dt := &dispatcherTool{
		info: buildAgentDispatchInfo(nil),
		dispatch: func(_ context.Context, _ AgentDispatchParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			t.Fatal("dispatch should not be called for invalid JSON")
			return fantasy.ToolResponse{}, nil
		},
	}

	resp, err := dt.Run(context.Background(), fantasy.ToolCall{Input: "not-valid-json{"})

	require.NoError(t, err) // errors are surfaced as error responses, not Go errors
	require.True(t, resp.IsError)
}

func TestDispatcherTool_Run_EmptySubagentType_RoutesToTask(t *testing.T) {
	t.Parallel()

	var capturedParams AgentDispatchParams
	dt := &dispatcherTool{
		info: buildAgentDispatchInfo(nil),
		dispatch: func(_ context.Context, params AgentDispatchParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			capturedParams = params
			return fantasy.NewTextResponse("ok"), nil
		},
	}

	input, _ := json.Marshal(AgentDispatchParams{Prompt: "search for something"})
	_, err := dt.Run(context.Background(), fantasy.ToolCall{Input: string(input)})

	require.NoError(t, err)
	require.Empty(t, capturedParams.SubagentType) // dispatch receives params as-is; routing is in the closure
}

func TestDispatcherTool_ProviderOptions_RoundTrip(t *testing.T) {
	t.Parallel()

	dt := &dispatcherTool{info: buildAgentDispatchInfo(nil)}
	require.Nil(t, dt.ProviderOptions())

	opts := fantasy.ProviderOptions{}
	dt.SetProviderOptions(opts)
	require.NotNil(t, dt.ProviderOptions())
}

func TestFindSubagentByName(t *testing.T) {
	t.Parallel()

	active := []*subagents.Subagent{
		{Name: "alpha"},
		{Name: "beta"},
	}

	require.NotNil(t, findSubagentByName(active, "alpha"))
	require.Equal(t, "alpha", findSubagentByName(active, "alpha").Name)
	require.Equal(t, "beta", findSubagentByName(active, "beta").Name)
	require.Nil(t, findSubagentByName(active, "missing"))
	require.Nil(t, findSubagentByName(active, ""))
	require.Nil(t, findSubagentByName(nil, "alpha"))
}

// TestDispatcherTool_Run_UnknownSubagent_ReturnsErrorResponse exercises the
// dispatcher routing for a subagent_type not in the active list. The closure
// here mirrors the lookup performed by (*coordinator).agentTool.
func TestDispatcherTool_Run_UnknownSubagent_ReturnsErrorResponse(t *testing.T) {
	t.Parallel()

	active := []*subagents.Subagent{
		{Name: "code-reviewer", Description: "ok"},
	}

	dt := &dispatcherTool{
		info: buildAgentDispatchInfo(active),
		dispatch: func(_ context.Context, params AgentDispatchParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			sa := findSubagentByName(active, params.SubagentType)
			if sa == nil {
				return fantasy.NewTextErrorResponse("unknown subagent type: \"" + params.SubagentType + "\""), nil
			}
			return fantasy.NewTextResponse("would have run " + sa.Name), nil
		},
	}

	input, _ := json.Marshal(AgentDispatchParams{SubagentType: "imaginary", Prompt: "do thing"})
	resp, err := dt.Run(context.Background(), fantasy.ToolCall{Input: string(input)})

	require.NoError(t, err)
	require.True(t, resp.IsError)
}

// TestAgentTool_SubagentBuildFailure_SurfacedAsToolError verifies that when a
// named subagent fails to build (because its model: names a model no provider
// offers), the dispatcher returns a ToolResponse with IsError==true and a nil
// Go error. A nil Go error is critical: fantasy treats a non-nil error as a
// hard abort of the whole agent turn, whereas an error response lets the
// parent model see the failure and continue.
func TestAgentTool_SubagentBuildFailure_SurfacedAsToolError(t *testing.T) {
	t.Parallel()

	env := testEnv(t)

	// Build a minimal offline config with one provider and one model, mirroring
	// agenttest.NewCoordinator so no network call is needed.
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)

	const (
		providerID = "test-openai-compat"
		modelID    = "test-model"
	)
	cfg.Config().Providers.Set(providerID, config.ProviderConfig{
		ID:      providerID,
		Name:    "Test",
		Type:    openaicompat.Name,
		BaseURL: "http://127.0.0.1:0/v1",
		APIKey:  "test",
		Models:  []catwalk.Model{{ID: modelID, DefaultMaxTokens: 4096}},
	})
	selected := config.SelectedModel{Provider: providerID, Model: modelID}
	cfg.Config().Models[config.SelectedModelTypeLarge] = selected
	cfg.Config().Models[config.SelectedModelTypeSmall] = selected
	cfg.SetupAgents()

	// Clear AllowedTools on both agents so buildTools stays cheap and offline.
	for _, agentID := range []string{config.AgentCoder, config.AgentTask} {
		a := cfg.Config().Agents[agentID]
		a.AllowedTools = nil
		cfg.Config().Agents[agentID] = a
	}

	c, err := NewCoordinator(t.Context(), CoordinatorOptions{
		Config:   cfg,
		Sessions: env.sessions,
		Messages: env.messages,
	})
	require.NoError(t, err)

	// Type-assert to *coordinator so we can access unexported fields and methods.
	coord := c.(*coordinator)

	// Inject a broken subagent whose model is not offered by any provider.
	// activeSubagentsList falls back to activeSubagents when subagentsMgr is nil.
	coord.activeSubagents = []*subagents.Subagent{
		{Name: "broken", Description: "intentionally broken", Model: "no-such-model"},
	}

	// Retrieve the real dispatcher tool built by agentTool.
	tool, err := coord.agentTool(t.Context())
	require.NoError(t, err)

	dt := tool.(*dispatcherTool)

	// Inject session and message IDs into context; the dispatch closure returns
	// a hard error when either is absent, which is a different code path.
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, "sess-1")
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "msg-1")

	input, err := json.Marshal(AgentDispatchParams{SubagentType: "broken", Prompt: "do it"})
	require.NoError(t, err)

	resp, err := dt.Run(ctx, fantasy.ToolCall{ID: "call-1", Input: string(input)})

	// The turn must not abort: fantasy treats a non-nil error as critical.
	require.NoError(t, err)
	// The build failure must be surfaced as a tool-error response so the
	// parent model can report it and continue.
	require.True(t, resp.IsError)
	// The subagent name must appear in the error message.
	require.Contains(t, resp.Content, "broken")
}

// TestConfirmBypassPermissions verifies the per-dispatch confirmation gate for
// TestAgentTool_TaskDispatch_BuildsOnLocalGroup verifies the task path of the
// dispatcher end-to-end with a real (offline) coordinator: the dispatch waits
// for the task agent's local build group before running, the run failure
// (unreachable provider) surfaces as a tool-error response rather than a turn
// abort, and the coordinator-wide readyWg stays clean throughout — a task
// build living on readyWg would risk both a promptless start and a sticky
// error failing every later turn.
func TestAgentTool_TaskDispatch_BuildsOnLocalGroup(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	coord := newOfflineCoordinator(t, env)
	require.NoError(t, coord.readyWg.Wait())

	parentSession, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	tool, err := coord.agentTool(t.Context())
	require.NoError(t, err)
	dt := tool.(*dispatcherTool)

	// Deadline-bound: the unreachable provider is retried with backoff, and
	// without a deadline the failure takes over a minute to surface.
	runCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second) // generous: suite-parallel load can exceed 2s between dispatches
	defer cancel()
	ctx := context.WithValue(runCtx, tools.SessionIDContextKey, parentSession.ID)
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "msg-1")

	input, err := json.Marshal(AgentDispatchParams{Prompt: "find something"})
	require.NoError(t, err)

	resp, err := dt.Run(ctx, fantasy.ToolCall{ID: "call-1", Input: string(input)})

	require.NoError(t, err, "a task run failure must not abort the turn")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Failed to generate response")
	require.NoError(t, coord.readyWg.Wait(), "the coordinator-wide readyWg must stay clean after a task dispatch")
}

// TestAgentTool_TaskBuildFailureIsRetryable verifies that a failed task-agent
// build does not stick for the tool's lifetime: once the config problem is
// fixed, the next dispatch builds and runs instead of replaying the cached
// error (sync.Once semantics would fail every later dispatch the same way).
func TestAgentTool_TaskBuildFailureIsRetryable(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	coord := newOfflineCoordinator(t, env)
	require.NoError(t, coord.readyWg.Wait())

	parentSession, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	tool, err := coord.agentTool(t.Context())
	require.NoError(t, err)
	dt := tool.(*dispatcherTool)

	input, err := json.Marshal(AgentDispatchParams{Prompt: "find something"})
	require.NoError(t, err)

	// Break the task build: no small model selected.
	smallModel := coord.cfg.Config().Models[config.SelectedModelTypeSmall]
	delete(coord.cfg.Config().Models, config.SelectedModelTypeSmall)

	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, parentSession.ID)
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "msg-1")

	resp, err := dt.Run(ctx, fantasy.ToolCall{ID: "call-1", Input: string(input)})
	require.NoError(t, err, "a task build failure must not abort the turn")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "build task agent")

	// Fix the config; the next dispatch must retry the build instead of
	// replaying the cached failure.
	coord.cfg.Config().Models[config.SelectedModelTypeSmall] = smallModel

	runCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second) // generous: suite-parallel load can exceed 2s between dispatches
	defer cancel()
	ctx = context.WithValue(runCtx, tools.SessionIDContextKey, parentSession.ID)
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "msg-2")

	resp, err = dt.Run(ctx, fantasy.ToolCall{ID: "call-2", Input: string(input)})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.NotContains(t, resp.Content, "build task agent",
		"the task build must be retried after a failure, not replay the cached error")
	require.Contains(t, resp.Content, "Failed to generate response")
}

// dispatchTypeParam extracts the subagent_type parameter's enum and
// description from a dispatcher ToolInfo.
func dispatchTypeParam(t *testing.T, info fantasy.ToolInfo) (enum []string, desc string) {
	t.Helper()
	paramMap, ok := info.Parameters["subagent_type"].(map[string]any)
	require.True(t, ok, "subagent_type parameter should be a map[string]any")
	enum, ok = paramMap["enum"].([]string)
	require.True(t, ok, "enum should be a []string")
	desc, ok = paramMap["description"].(string)
	require.True(t, ok, "description should be a string")
	return enum, desc
}

// TestBuildAgentDispatchInfo_FastType pins the cheap built-in type: it must be
// dispatchable with no subagents configured at all, since it is the zero-config
// fan-out target.
func TestBuildAgentDispatchInfo_FastType(t *testing.T) {
	t.Parallel()

	enum, desc := dispatchTypeParam(t, buildAgentDispatchInfo(nil))

	require.Contains(t, enum, config.AgentTask)
	require.Contains(t, enum, config.AgentFast)
	require.Contains(t, desc, "small model")
	require.Contains(t, desc, "large model")
}

// TestBuildAgentDispatchInfo_SurfacesModel verifies each subagent line carries
// the model it runs on, so the dispatching model can weigh cost, and that only
// the `small` alias is advertised as cheap.
func TestBuildAgentDispatchInfo_SurfacesModel(t *testing.T) {
	t.Parallel()

	_, desc := dispatchTypeParam(t, buildAgentDispatchInfo([]*subagents.Subagent{
		{Name: "scout", Description: "Narrow lookups", Model: subagents.ModelAliasSmall, Effort: "low"},
		{Name: "architect", Description: "Designs changes"},
		{Name: "pinned", Description: "Specific model", Model: "gpt-5"},
	}))

	require.Contains(t, desc, "scout (model: small, effort: low): Narrow lookups")
	require.Contains(t, desc, "[cheap: prefer fanning several out in parallel]")

	// An absent model: reports the large default, and effort is omitted when
	// unset rather than rendered empty.
	require.Contains(t, desc, "architect (model: large): Designs changes")
	require.NotContains(t, desc, "effort: )")

	// A pinned model id is reported but never assumed cheap.
	require.Contains(t, desc, "pinned (model: gpt-5): Specific model")
	require.Equal(t, 1, strings.Count(desc, "[cheap:"))
}

// TestBuildAgentDispatchInfo_BuiltinNameCollision verifies a subagent named
// after a built-in type is dropped from the enum instead of being offered as a
// choice that dispatch would resolve to the built-in.
func TestBuildAgentDispatchInfo_BuiltinNameCollision(t *testing.T) {
	t.Parallel()

	enum, desc := dispatchTypeParam(t, buildAgentDispatchInfo([]*subagents.Subagent{
		{Name: config.AgentFast, Description: "shadowed by the built-in"},
		{Name: config.AgentTask, Description: "also shadowed"},
		{Name: "real", Description: "reachable"},
	}))

	require.Equal(t, []string{config.AgentTask, config.AgentFast, "real"}, enum,
		"colliding names must not be added a second time")
	require.NotContains(t, desc, "shadowed by the built-in")
	require.NotContains(t, desc, "also shadowed")
	require.Contains(t, desc, "real (model: large): reachable")
}
