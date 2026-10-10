package fantasy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// earlyWait bounds how long a test waits for something that should happen
// promptly, so that a regression fails instead of hanging.
const earlyWait = 5 * time.Second

type earlyInput struct {
	N int `json:"n"`
}

// earlyDispatchModel serves the first step from first and answers any later
// step, one that carries tool results, with a plain stop.
func earlyDispatchModel(first func(yield func(StreamPart) bool)) *mockLanguageModel {
	return &mockLanguageModel{
		streamFunc: func(ctx context.Context, call Call) (StreamResponse, error) {
			for _, msg := range call.Prompt {
				if msg.Role == MessageRoleTool {
					return func(yield func(StreamPart) bool) {
						yield(StreamPart{Type: StreamPartTypeTextStart, ID: "t"})
						yield(StreamPart{Type: StreamPartTypeTextDelta, ID: "t", Delta: "done"})
						yield(StreamPart{Type: StreamPartTypeTextEnd, ID: "t"})
						yield(StreamPart{Type: StreamPartTypeFinish, FinishReason: FinishReasonStop})
					}, nil
				}
			}
			return first, nil
		},
	}
}

func allowEarly(ToolCallContent) bool { return true }

func toolCallPart(id, name, input string) StreamPart {
	return StreamPart{Type: StreamPartTypeToolCall, ID: id, ToolCallName: name, ToolCallInput: input}
}

func finishPart(reason FinishReason) StreamPart {
	return StreamPart{Type: StreamPartTypeFinish, FinishReason: reason, Usage: Usage{TotalTokens: 10}}
}

func toolResultsOf(step StepResult) []ToolResultContent {
	var results []ToolResultContent
	for _, c := range step.Content {
		if tr, ok := AsContentType[ToolResultContent](c); ok {
			results = append(results, tr)
		}
	}
	return results
}

func toolResultIDs(step StepResult) []string {
	var ids []string
	for _, tr := range toolResultsOf(step) {
		ids = append(ids, tr.ToolCallID)
	}
	return ids
}

// eventLog records callback order across goroutines.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// TestStreamingAgent_EarlyDispatchStartsParallelToolBeforeFinish checks
// that a parallel tool the consumer opts in starts while the stream is
// still running, and that consumers still see every OnToolCall, after the
// stream finished, before any OnToolResult.
func TestStreamingAgent_EarlyDispatchStartsParallelToolBeforeFinish(t *testing.T) {
	t.Parallel()

	var probesStarted atomic.Int32
	allProbesStarted := make(chan struct{})
	probe := NewParallelAgentTool("probe", "starts early",
		func(ctx context.Context, in earlyInput, call ToolCall) (ToolResponse, error) {
			if probesStarted.Add(1) == 2 {
				close(allProbesStarted)
			}
			return NewTextResponse(fmt.Sprintf("probed %d", in.N)), nil
		})
	seq := NewAgentTool("seq", "sequential, never early",
		func(ctx context.Context, in earlyInput, call ToolCall) (ToolResponse, error) {
			return NewTextResponse("sequenced"), nil
		})

	var log eventLog
	first := func(yield func(StreamPart) bool) {
		if !yield(toolCallPart("call-1", "probe", `{"n":1}`)) {
			return
		}
		if !yield(toolCallPart("call-2", "seq", `{"n":2}`)) {
			return
		}
		if !yield(toolCallPart("call-3", "probe", `{"n":3}`)) {
			return
		}
		select {
		case <-allProbesStarted:
			log.add("probes started")
		case <-time.After(earlyWait):
			log.add("probes not started")
		}
		yield(finishPart(FinishReasonToolCalls))
	}

	agent := NewAgent(earlyDispatchModel(first), WithTools(probe, seq), WithEarlyToolDispatch(allowEarly))
	result, err := agent.Stream(context.Background(), AgentStreamCall{
		Prompt: "probe twice",
		OnStreamFinish: func(Usage, FinishReason, ProviderMetadata) error {
			log.add("finish")
			return nil
		},
		OnToolCall: func(tc ToolCallContent) error {
			log.add("call " + tc.ToolCallID)
			return nil
		},
		OnToolResult: func(tr ToolResultContent) error {
			log.add("result " + tr.ToolCallID)
			return nil
		},
	})
	require.NoError(t, err)
	require.Len(t, result.Steps, 2)

	events := log.snapshot()
	require.GreaterOrEqual(t, len(events), 8)
	require.Equal(t, []string{"probes started", "finish", "call call-1", "call call-2", "call call-3"}, events[:5],
		"probes must start before the finish, and OnToolCall must wait for it")
	require.ElementsMatch(t, []string{"result call-1", "result call-2", "result call-3"}, events[5:8])

	require.Equal(t, []string{"call-1", "call-2", "call-3"}, toolResultIDs(result.Steps[0]),
		"results must follow call order")
	results := toolResultsOf(result.Steps[0])
	text, ok := results[0].Result.(ToolResultOutputContentText)
	require.True(t, ok)
	require.Equal(t, "probed 1", text.Text)
}

// TestStreamingAgent_EarlyDispatchHoldsBackIneligibleCalls checks every way
// a call can fail to qualify for early dispatch: each one must not start
// until the stream has finished, and must still run after it.
func TestStreamingAgent_EarlyDispatchHoldsBackIneligibleCalls(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		parallel bool
		input    string
		allow    EarlyToolDispatchFunction
	}{
		{name: "sequential tool", parallel: false, input: `{"n":1}`, allow: allowEarly},
		{name: "not opted in", parallel: true, input: `{"n":1}`, allow: nil},
		{name: "declined by consumer", parallel: true, input: `{"n":1}`, allow: func(ToolCallContent) bool { return false }},
		// Truncated JSON is repaired after the stream ends; repair must
		// never run mid-stream.
		{name: "arguments need repair", parallel: true, input: `{"n":1`, allow: allowEarly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var ran, ranBeforeFinish atomic.Bool
			run := func(ctx context.Context, in earlyInput, call ToolCall) (ToolResponse, error) {
				ran.Store(true)
				return NewTextResponse("ran"), nil
			}
			tool := NewAgentTool("tool", "held back", run)
			if tc.parallel {
				tool = NewParallelAgentTool("tool", "held back", run)
			}

			first := func(yield func(StreamPart) bool) {
				if !yield(toolCallPart("call-1", "tool", tc.input)) {
					return
				}
				// Long enough for an early dispatch to have started it.
				time.Sleep(50 * time.Millisecond)
				ranBeforeFinish.Store(ran.Load())
				yield(finishPart(FinishReasonToolCalls))
			}

			agent := NewAgent(earlyDispatchModel(first), WithTools(tool), WithEarlyToolDispatch(tc.allow))
			result, err := agent.Stream(context.Background(), AgentStreamCall{Prompt: "run"})
			require.NoError(t, err)
			require.False(t, ranBeforeFinish.Load(), "tool must not start before the stream finished")
			require.True(t, ran.Load(), "tool must still run after a tool-calls finish")
			require.Equal(t, []string{"call-1"}, toolResultIDs(result.Steps[0]))
		})
	}
}

// TestStreamingAgent_EarlyDispatchDiscardedOnAbnormalFinish checks that an
// early call the step then does not dispatch (any finish but tool-calls) is
// canceled and leaves no trace beyond the recorded call: no tool result in
// the step and no OnToolResult, and OnToolCall only where a non-dispatching
// finish fires it today (stop).
func TestStreamingAgent_EarlyDispatchDiscardedOnAbnormalFinish(t *testing.T) {
	t.Parallel()

	for _, reason := range []FinishReason{FinishReasonLength, FinishReasonError, FinishReasonContentFilter, FinishReasonUnknown, FinishReasonStop} {
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()

			started := make(chan struct{})
			var canceled atomic.Bool
			probe := NewParallelAgentTool("probe", "starts early, then blocks",
				func(ctx context.Context, in earlyInput, call ToolCall) (ToolResponse, error) {
					close(started)
					select {
					case <-ctx.Done():
						canceled.Store(true)
					case <-time.After(earlyWait):
					}
					return NewTextResponse("probed"), nil
				})

			var startedBeforeFinish atomic.Bool
			first := func(yield func(StreamPart) bool) {
				if !yield(toolCallPart("call-1", "probe", `{"n":1}`)) {
					return
				}
				select {
				case <-started:
					startedBeforeFinish.Store(true)
				case <-time.After(earlyWait):
				}
				yield(finishPart(reason))
			}

			var toolCalls, toolResults atomic.Int32
			agent := NewAgent(earlyDispatchModel(first), WithTools(probe), WithEarlyToolDispatch(allowEarly))
			result, err := agent.Stream(context.Background(), AgentStreamCall{
				Prompt: "probe",
				OnToolCall: func(ToolCallContent) error {
					toolCalls.Add(1)
					return nil
				},
				OnToolResult: func(ToolResultContent) error {
					toolResults.Add(1)
					return nil
				},
			})
			require.NoError(t, err)
			require.True(t, startedBeforeFinish.Load(), "probe must have started early")
			require.True(t, canceled.Load(), "a discarded early call must be canceled")
			require.Zero(t, toolResults.Load(), "no result may be reported for a call that was not dispatched")

			wantCalls := int32(0)
			if reason == FinishReasonStop {
				wantCalls = 1
			}
			require.Equal(t, wantCalls, toolCalls.Load())

			require.Len(t, result.Steps, 1)
			require.Empty(t, toolResultsOf(result.Steps[0]))
			require.Len(t, result.Steps[0].Content.ToolCalls(), 1, "the call itself is still recorded")
		})
	}
}

// TestStreamingAgent_EarlyDispatchCanceledWithContext checks that canceling
// the caller's context reaches a tool that started early.
func TestStreamingAgent_EarlyDispatchCanceledWithContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	var once sync.Once
	var canceled atomic.Bool
	probe := NewParallelAgentTool("probe", "blocks until canceled",
		func(ctx context.Context, in earlyInput, call ToolCall) (ToolResponse, error) {
			once.Do(func() { close(started) })
			select {
			case <-ctx.Done():
				canceled.Store(true)
			case <-time.After(earlyWait):
			}
			return NewTextResponse("probed"), nil
		})

	first := func(yield func(StreamPart) bool) {
		if !yield(toolCallPart("call-1", "probe", `{"n":1}`)) {
			return
		}
		select {
		case <-started:
		case <-time.After(earlyWait):
		}
		cancel()
		yield(StreamPart{Type: StreamPartTypeError, Error: context.Canceled})
	}

	agent := NewAgent(earlyDispatchModel(first), WithTools(probe), WithEarlyToolDispatch(allowEarly))
	_, err := agent.Stream(ctx, AgentStreamCall{Prompt: "probe"})
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, canceled.Load(), "the early call must see the cancellation")
}

// TestStreamingAgent_SequentialToolsKeepOrder checks that sequential tools
// still run one at a time, in the order they were called, even when an
// earlier one is the slowest.
func TestStreamingAgent_SequentialToolsKeepOrder(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var order []int
	var running, maxRunning atomic.Int32
	seq := NewAgentTool("seq", "sequential",
		func(ctx context.Context, in earlyInput, call ToolCall) (ToolResponse, error) {
			now := running.Add(1)
			defer running.Add(-1)
			for {
				peak := maxRunning.Load()
				if now <= peak || maxRunning.CompareAndSwap(peak, now) {
					break
				}
			}
			// The first call is the slowest, so a reorder would show.
			time.Sleep(time.Duration(4-in.N) * 10 * time.Millisecond)
			mu.Lock()
			order = append(order, in.N)
			mu.Unlock()
			return NewTextResponse(fmt.Sprintf("seq %d", in.N)), nil
		})

	first := func(yield func(StreamPart) bool) {
		for n := 1; n <= 3; n++ {
			if !yield(toolCallPart(fmt.Sprintf("call-%d", n), "seq", fmt.Sprintf(`{"n":%d}`, n))) {
				return
			}
		}
		yield(finishPart(FinishReasonToolCalls))
	}

	agent := NewAgent(earlyDispatchModel(first), WithTools(seq), WithEarlyToolDispatch(allowEarly))
	result, err := agent.Stream(context.Background(), AgentStreamCall{Prompt: "run in order"})
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, order)
	require.Equal(t, int32(1), maxRunning.Load(), "sequential tools must not overlap")
	require.Equal(t, []string{"call-1", "call-2", "call-3"}, toolResultIDs(result.Steps[0]))
}

// TestStreamingAgent_ParallelToolNotBlockedBySequential checks that a
// parallel tool called after a slow sequential one runs alongside it: the
// sequential tool here cannot finish until the parallel one has, so a
// dispatcher that waits for it would stall until the timeout.
func TestStreamingAgent_ParallelToolNotBlockedBySequential(t *testing.T) {
	t.Parallel()

	fastDone := make(chan struct{})
	slow := NewAgentTool("slow", "sequential, waits for fast",
		func(ctx context.Context, in earlyInput, call ToolCall) (ToolResponse, error) {
			select {
			case <-fastDone:
				return NewTextResponse("slow done"), nil
			case <-time.After(earlyWait):
				return NewTextErrorResponse("fast was held behind slow"), nil
			}
		})
	fast := NewParallelAgentTool("fast", "parallel",
		func(ctx context.Context, in earlyInput, call ToolCall) (ToolResponse, error) {
			close(fastDone)
			return NewTextResponse("fast done"), nil
		})

	first := func(yield func(StreamPart) bool) {
		if !yield(toolCallPart("call-1", "slow", `{"n":1}`)) {
			return
		}
		if !yield(toolCallPart("call-2", "fast", `{"n":2}`)) {
			return
		}
		yield(finishPart(FinishReasonToolCalls))
	}

	// No early dispatch: this is about the post-stream dispatcher.
	agent := NewAgent(earlyDispatchModel(first), WithTools(slow, fast))
	result, err := agent.Stream(context.Background(), AgentStreamCall{Prompt: "slow then fast"})
	require.NoError(t, err)

	results := toolResultsOf(result.Steps[0])
	require.Equal(t, []string{"call-1", "call-2"}, toolResultIDs(result.Steps[0]))
	text, ok := results[0].Result.(ToolResultOutputContentText)
	require.True(t, ok, "slow tool failed: %v", results[0].Result)
	require.True(t, strings.HasPrefix(text.Text, "slow done"))
}
