package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

func glmModel(effort string) Model {
	return Model{
		CatalogCfg: catalog.Model{ID: "glm-5.3", CanReason: true, ReasoningLevels: []string{"low", "high", "max"}},
		ModelCfg:   config.SelectedModel{Provider: "zai-coding-plan", Model: "glm-5.3", ReasoningEffort: effort},
	}
}

var zaiProvider = config.ProviderConfig{ID: string(catalog.InferenceProviderZAICoding), Type: openaicompat.Name}

func TestToolStepEffort(t *testing.T) {
	t.Parallel()

	claude := Model{
		CatalogCfg: catalog.Model{ID: "claude-opus-4-7", CanReason: true, ReasoningLevels: []string{"low", "medium", "high", "xhigh", "max"}},
		ModelCfg:   config.SelectedModel{ReasoningEffort: "max"},
	}
	routedClaude := claude
	routedClaude.CatalogCfg.ID = "anthropic/claude-opus-4.7"
	gpt := Model{
		CatalogCfg: catalog.Model{ID: "gpt-5", CanReason: true, ReasoningLevels: []string{"minimal", "low", "medium", "high"}},
		ModelCfg:   config.SelectedModel{ReasoningEffort: "high"},
	}
	routedGLM := glmModel("max")
	routedGLM.CatalogCfg.ID = "z-ai/glm-5.3"
	nonReasoning := Model{CatalogCfg: catalog.Model{ID: "glm-4"}}

	tests := []struct {
		name     string
		setting  string
		model    Model
		provider catalog.Type
		want     string
	}{
		{"auto steps max down to high", config.ToolStepReasoningEffortAuto, glmModel("max"), openaicompat.Name, "high"},
		{"auto steps high down to the next level the model has", config.ToolStepReasoningEffortAuto, glmModel("high"), openaicompat.Name, "low"},
		{"auto keeps low", config.ToolStepReasoningEffortAuto, glmModel("low"), openaicompat.Name, ""},
		{"auto steps the catalog default when none is set", config.ToolStepReasoningEffortAuto, glmModel(""), openaicompat.Name, "low"},
		{"same turns it off", config.ToolStepReasoningEffortSame, glmModel("max"), openaicompat.Name, ""},
		{"explicit level", "low", glmModel("max"), openaicompat.Name, "low"},
		{"explicit level is never above the configured one", "max", glmModel("high"), openaicompat.Name, ""},
		{"explicit level equal to the configured one", "high", glmModel("high"), openaicompat.Name, ""},
		{"explicit level the model lacks falls back to auto", "medium", glmModel("max"), openaicompat.Name, "high"},
		{"auto leaves Anthropic alone", config.ToolStepReasoningEffortAuto, claude, anthropic.Name, ""},
		{"auto leaves Claude on a router alone", config.ToolStepReasoningEffortAuto, routedClaude, openrouter.Name, ""},
		{"auto leaves OpenAI alone", config.ToolStepReasoningEffortAuto, gpt, openai.Name, ""},
		{"explicit level applies to Anthropic", "high", claude, anthropic.Name, "high"},
		{"auto steps GLM on a router", config.ToolStepReasoningEffortAuto, routedGLM, openrouter.Name, "high"},
		{"a model that cannot reason", config.ToolStepReasoningEffortAuto, nonReasoning, openaicompat.Name, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, toolStepEffort(tc.setting, tc.model, tc.provider))
		})
	}
}

// Z.AI takes the effort as reasoning_effort, and thinking stays on at the
// lower level.
func TestToolStepProviderOptionsForZAI(t *testing.T) {
	t.Parallel()

	opts := toolStepProviderOptions(glmModel("max"), zaiProvider, config.ToolStepReasoningEffortAuto)
	parsed, ok := opts[openaicompat.Name].(*openaicompat.ProviderOptions)
	require.True(t, ok)
	require.NotNil(t, parsed.ReasoningEffort)
	require.Equal(t, "high", string(*parsed.ReasoningEffort))
	require.Equal(t, map[string]any{"type": "enabled"}, parsed.ExtraBody["thinking"])

	full, ok := getProviderOptions(glmModel("max"), zaiProvider)[openaicompat.Name].(*openaicompat.ProviderOptions)
	require.True(t, ok)
	require.Equal(t, "max", string(*full.ReasoningEffort), "the call keeps the configured effort")

	require.Nil(t, toolStepProviderOptions(glmModel("max"), zaiProvider, config.ToolStepReasoningEffortSame))
	require.Nil(t, toolStepProviderOptions(glmModel("low"), zaiProvider, config.ToolStepReasoningEffortAuto))
}

// An effort pinned in provider_options wins over the configured one for
// every step, so there is nothing to vary.
func TestToolStepProviderOptionsRespectPinnedEffort(t *testing.T) {
	t.Parallel()

	model := glmModel("max")
	model.ModelCfg.ProviderOptions = map[string]any{"reasoning_effort": "max"}
	require.Nil(t, toolStepProviderOptions(model, zaiProvider, config.ToolStepReasoningEffortAuto))
}

func TestToolResultStep(t *testing.T) {
	t.Parallel()

	toolResult := func(output fantasy.ToolResultOutputContent) fantasy.Message {
		return fantasy.Message{
			Role:    fantasy.MessageRoleTool,
			Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "c1", Output: output}},
		}
	}
	ok := toolResult(fantasy.ToolResultOutputContentText{Text: "ok"})
	failed := toolResult(fantasy.ToolResultOutputContentError{Error: errors.New("boom")})
	user := fantasy.NewUserMessage("go")

	tests := []struct {
		name string
		step int
		msgs []fantasy.Message
		want bool
	}{
		{"tool result", 1, []fantasy.Message{user, assistantText("call"), ok}, true},
		{"failed tool result", 1, []fantasy.Message{user, assistantText("call"), failed}, false},
		{"user message", 1, []fantasy.Message{user}, false},
		{"first step of a turn", 0, []fantasy.Message{user, assistantText("call"), ok}, false},
		{"no messages", 1, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, toolResultStep(fantasy.PrepareStepFunctionOptions{StepNumber: tc.step, Messages: tc.msgs}))
		})
	}
}

func TestToolStepReasoningEffortOption(t *testing.T) {
	t.Parallel()

	var nilOpts *config.Options
	require.Equal(t, config.ToolStepReasoningEffortAuto, nilOpts.GetToolStepReasoningEffort())
	require.Equal(t, config.ToolStepReasoningEffortAuto, (&config.Options{}).GetToolStepReasoningEffort())
	require.Equal(t, "low", (&config.Options{ToolStepReasoningEffort: "low"}).GetToolStepReasoningEffort())
}

// stubTool answers every call with its fixed result.
type stubTool struct {
	name string
	fail bool
}

func (s *stubTool) Info() fantasy.ToolInfo { return fantasy.ToolInfo{Name: s.name} }

func (s *stubTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if s.fail {
		return fantasy.NewTextErrorResponse("it failed"), nil
	}
	return fantasy.NewTextResponse("it worked"), nil
}

func (s *stubTool) ProviderOptions() fantasy.ProviderOptions     { return nil }
func (s *stubTool) SetProviderOptions(_ fantasy.ProviderOptions) {}

func effortOptions(effort string) fantasy.ProviderOptions {
	e := openai.ReasoningEffort(effort)
	return fantasy.ProviderOptions{openaicompat.Name: &openaicompat.ProviderOptions{ReasoningEffort: &e}}
}

// sentEfforts returns the reasoning effort each request to the model was
// sent with.
func sentEfforts(t *testing.T, model *scriptedModel) []string {
	t.Helper()
	var efforts []string
	for _, call := range model.sentCalls() {
		opts, ok := call.ProviderOptions[openaicompat.Name].(*openaicompat.ProviderOptions)
		require.True(t, ok)
		require.NotNil(t, opts.ReasoningEffort)
		efforts = append(efforts, string(*opts.ReasoningEffort))
	}
	return efforts
}

// The step answering the user runs at the call's effort, the steps that
// digest tool results at the tool-step effort, and a step after a failed
// tool at the call's again. The next turn starts at the call's.
func TestRunSendsToolStepsAtTheToolStepEffort(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := newScriptedModel(
		scriptedTurn{calls: []scriptedCall{{name: "ok"}}},
		scriptedTurn{calls: []scriptedCall{{name: "fail"}}},
		scriptedTurn{calls: []scriptedCall{{name: "ok"}}},
		scriptedTurn{text: "done"},
		scriptedTurn{text: "second turn"},
	)
	sa := testSessionAgent(env, model, textModel("title"), "system", &stubTool{name: "ok"}, &stubTool{name: "fail", fail: true})
	sess, err := env.sessions.Create(t.Context(), "effort")
	require.NoError(t, err)

	call := SessionAgentCall{
		SessionID:               sess.ID,
		ProviderOptions:         effortOptions("max"),
		ToolStepProviderOptions: effortOptions("high"),
	}
	for _, prompt := range []string{"start", "again"} {
		call.Prompt = prompt
		_, err = sa.Run(t.Context(), call)
		require.NoError(t, err)
	}

	require.Equal(t, []string{"max", "high", "max", "high", "max"}, sentEfforts(t, model))
}

// A user prompt folded into the step after a tool result is answered at
// the turn's effort.
func TestRunAnswersAFoldedPromptAtTheTurnEffort(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	gated := &gatedAgentTool{entered: make(chan struct{}), release: make(chan struct{})}
	model := newScriptedModel(
		scriptedTurn{calls: []scriptedCall{{name: "gated"}}},
		scriptedTurn{text: "acknowledged"},
	)
	sa := testSessionAgent(env, model, textModel("title"), "system", gated).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "fold")
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		_, runErr := sa.Run(t.Context(), SessionAgentCall{
			SessionID:               sess.ID,
			Prompt:                  "start",
			ProviderOptions:         effortOptions("max"),
			ToolStepProviderOptions: effortOptions("high"),
		})
		runDone <- runErr
	}()
	select {
	case <-gated.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool call never started")
	}
	sa.enqueueCallLocked(SessionAgentCall{SessionID: sess.ID, Prompt: "STEER", acceptSeq: 1})
	close(gated.release)
	select {
	case runErr := <-runDone:
		require.NoError(t, runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never finished")
	}

	require.Equal(t, []string{"max", "max"}, sentEfforts(t, model))
}
