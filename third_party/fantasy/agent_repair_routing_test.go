package fantasy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// repairProbe is a repair function that records what it was asked and
// fills in the missing required parameter.
type repairProbe struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	value   string
}

func (p *repairProbe) fn(_ context.Context, opts ToolCallRepairOptions) (*ToolCallContent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.prompts = append(p.prompts, opts.SystemPrompt)
	c := opts.OriginalToolCall
	c.Input = `{"value":"` + p.value + `"}`
	return &c, nil
}

func (p *repairProbe) seen() (int, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, append([]string(nil), p.prompts...)
}

// repairTool requires "value" and records the inputs it ran with.
func repairTool(inputs *[]string, mu *sync.Mutex) *mockTool {
	return &mockTool{
		name:        "needs_value",
		description: "requires value",
		parameters:  map[string]any{"value": map[string]any{"type": "string"}},
		required:    []string{"value"},
		executeFunc: func(_ context.Context, call ToolCall) (ToolResponse, error) {
			mu.Lock()
			*inputs = append(*inputs, call.Input)
			mu.Unlock()
			return ToolResponse{Content: "ok"}, nil
		},
	}
}

// invalidCallStream answers the first request with a call missing its
// required parameter and every later one with plain text.
func invalidCallStream() *mockLanguageModel {
	var n atomic.Int32
	return &mockLanguageModel{
		streamFunc: func(context.Context, Call) (StreamResponse, error) {
			first := n.Add(1) == 1
			return func(yield func(StreamPart) bool) {
				if first {
					if !yield(StreamPart{Type: StreamPartTypeToolCall, ID: "c1", ToolCallName: "needs_value", ToolCallInput: `{}`}) {
						return
					}
					yield(StreamPart{Type: StreamPartTypeFinish, FinishReason: FinishReasonToolCalls, Usage: Usage{TotalTokens: 1}})
					return
				}
				if !yield(StreamPart{Type: StreamPartTypeTextStart, ID: "t"}) {
					return
				}
				if !yield(StreamPart{Type: StreamPartTypeTextDelta, ID: "t", Delta: "done"}) {
					return
				}
				if !yield(StreamPart{Type: StreamPartTypeTextEnd, ID: "t"}) {
					return
				}
				yield(StreamPart{Type: StreamPartTypeFinish, FinishReason: FinishReasonStop, Usage: Usage{TotalTokens: 1}})
			}, nil
		},
	}
}

// invalidCallGenerate is invalidCallStream for Generate.
func invalidCallGenerate() *mockLanguageModel {
	var n atomic.Int32
	return &mockLanguageModel{
		generateFunc: func(context.Context, Call) (*Response, error) {
			if n.Add(1) == 1 {
				return &Response{
					Content:      ResponseContent{ToolCallContent{ToolCallID: "c1", ToolName: "needs_value", Input: `{}`}},
					FinishReason: FinishReasonToolCalls,
					Usage:        Usage{TotalTokens: 1},
				}, nil
			}
			return &Response{
				Content:      ResponseContent{TextContent{Text: "done"}},
				FinishReason: FinishReasonStop,
				Usage:        Usage{TotalTokens: 1},
			}, nil
		},
	}
}

// The agent-level repair function applies to streamed steps. It used to
// apply only to Generate: Stream read the repair function from the raw
// call rather than the prepared one, so an agent configured with
// WithRepairToolCall fell back to the built-in JSON repair when
// streaming, and a call it could have fixed went back to the model as
// invalid.
func TestStreamingAgent_UsesAgentLevelRepair(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var inputs []string
	probe := &repairProbe{value: "agent"}
	agent := NewAgent(invalidCallStream(),
		WithTools(repairTool(&inputs, &mu)),
		WithRepairToolCall(probe.fn),
		WithStopConditions(StepCountIs(3)),
	)

	_, err := agent.Stream(context.Background(), AgentStreamCall{Prompt: "go"})
	require.NoError(t, err)
	calls, _ := probe.seen()
	require.Equal(t, 1, calls, "the agent-level repair never ran for a streamed step")
	require.Equal(t, []string{`{"value":"agent"}`}, inputs)
}

// A repair function on the call itself wins over the agent's, in both
// modes.
func TestAgent_CallRepairOverridesAgentRepair(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"stream", "generate"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var inputs []string
			agentProbe := &repairProbe{value: "agent"}
			callProbe := &repairProbe{value: "call"}
			model := invalidCallStream()
			if mode == "generate" {
				model = invalidCallGenerate()
			}
			agent := NewAgent(model,
				WithTools(repairTool(&inputs, &mu)),
				WithRepairToolCall(agentProbe.fn),
				WithStopConditions(StepCountIs(3)),
			)

			var err error
			if mode == "stream" {
				_, err = agent.Stream(context.Background(), AgentStreamCall{Prompt: "go", RepairToolCall: callProbe.fn})
			} else {
				_, err = agent.Generate(context.Background(), AgentCall{Prompt: "go", RepairToolCall: callProbe.fn})
			}
			require.NoError(t, err)
			agentCalls, _ := agentProbe.seen()
			callCalls, _ := callProbe.seen()
			require.Zero(t, agentCalls, "the agent's repair ran although the call brought its own")
			require.Equal(t, 1, callCalls)
			require.Equal(t, []string{`{"value":"call"}`}, inputs)
		})
	}
}

// Repair is shown the system prompt the step ran with. PrepareStep can
// replace it, and the streamed path used to pass the agent's original
// prompt instead.
func TestStreamingAgent_RepairSeesStepSystemPrompt(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var inputs []string
	probe := &repairProbe{value: "x"}
	stepPrompt := "step system prompt"
	agent := NewAgent(invalidCallStream(),
		WithSystemPrompt("agent system prompt"),
		WithTools(repairTool(&inputs, &mu)),
		WithRepairToolCall(probe.fn),
		WithStopConditions(StepCountIs(3)),
		WithPrepareStep(func(ctx context.Context, _ PrepareStepFunctionOptions) (context.Context, PrepareStepResult, error) {
			return ctx, PrepareStepResult{System: &stepPrompt}, nil
		}),
	)

	_, err := agent.Stream(context.Background(), AgentStreamCall{Prompt: "go"})
	require.NoError(t, err)
	calls, prompts := probe.seen()
	require.Equal(t, 1, calls)
	require.Equal(t, []string{stepPrompt}, prompts)
}
