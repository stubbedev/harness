package fantasy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// optionsRecorder is a model that answers the first request with a call
// to the noop tool and every later one with text, recording the provider
// options each request carried.
type optionsRecorder struct {
	mu   sync.Mutex
	seen []ProviderOptions
	n    atomic.Int32
}

func (r *optionsRecorder) record(call Call) bool {
	r.mu.Lock()
	r.seen = append(r.seen, call.ProviderOptions)
	r.mu.Unlock()
	return r.n.Add(1) == 1
}

func (r *optionsRecorder) options() []ProviderOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ProviderOptions(nil), r.seen...)
}

func (r *optionsRecorder) model(mode string) *mockLanguageModel {
	if mode == "generate" {
		return &mockLanguageModel{
			generateFunc: func(_ context.Context, call Call) (*Response, error) {
				if r.record(call) {
					return &Response{
						Content:      ResponseContent{ToolCallContent{ToolCallID: "c1", ToolName: "noop", Input: `{}`}},
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
	return &mockLanguageModel{
		streamFunc: func(_ context.Context, call Call) (StreamResponse, error) {
			first := r.record(call)
			return func(yield func(StreamPart) bool) {
				if first {
					if !yield(StreamPart{Type: StreamPartTypeToolCall, ID: "c1", ToolCallName: "noop", ToolCallInput: `{}`}) {
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

// PrepareStep can replace the provider options of one step. The step it
// returns options for is sent with them, layered over the agent's own; a
// step it returns none for keeps the call's.
func TestAgent_PrepareStepReplacesProviderOptions(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"stream", "generate"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			rec := &optionsRecorder{}
			noop := &mockTool{
				name:       "noop",
				parameters: map[string]any{},
				executeFunc: func(context.Context, ToolCall) (ToolResponse, error) {
					return ToolResponse{Content: "ok"}, nil
				},
			}
			agentOpts := ProviderOptions{"agent": &mockProviderData{Key: "agent"}}
			callOpts := ProviderOptions{"p": &mockProviderData{Key: "call"}}
			stepOpts := ProviderOptions{"p": &mockProviderData{Key: "step"}}
			prepare := func(ctx context.Context, opts PrepareStepFunctionOptions) (context.Context, PrepareStepResult, error) {
				if opts.StepNumber == 0 {
					return ctx, PrepareStepResult{}, nil
				}
				return ctx, PrepareStepResult{ProviderOptions: stepOpts}, nil
			}
			agent := NewAgent(rec.model(mode),
				WithTools(noop),
				WithProviderOptions(agentOpts),
				WithStopConditions(StepCountIs(3)),
			)

			var err error
			if mode == "stream" {
				_, err = agent.Stream(context.Background(), AgentStreamCall{Prompt: "go", ProviderOptions: callOpts, PrepareStep: prepare})
			} else {
				_, err = agent.Generate(context.Background(), AgentCall{Prompt: "go", ProviderOptions: callOpts, PrepareStep: prepare})
			}
			require.NoError(t, err)

			seen := rec.options()
			require.Len(t, seen, 2)
			require.Equal(t, &mockProviderData{Key: "call"}, seen[0]["p"], "a step PrepareStep left alone keeps the call's options")
			require.Equal(t, &mockProviderData{Key: "agent"}, seen[0]["agent"])
			require.Equal(t, &mockProviderData{Key: "step"}, seen[1]["p"], "the step's options replace the call's")
			require.Equal(t, &mockProviderData{Key: "agent"}, seen[1]["agent"], "the step's options are layered over the agent's")
			require.Equal(t, &mockProviderData{Key: "call"}, callOpts["p"], "the call's options are not modified")
		})
	}
}
