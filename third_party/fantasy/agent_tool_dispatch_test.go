package fantasy

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStreamingAgent_OnToolDispatchRunsBeforeTools checks that
// OnToolDispatch fires once, after every OnToolCall of the step and before
// any tool that was not dispatched early starts.
func TestStreamingAgent_OnToolDispatchRunsBeforeTools(t *testing.T) {
	t.Parallel()

	var log eventLog
	work := NewParallelAgentTool("work", "records that it ran",
		func(ctx context.Context, in earlyInput, call ToolCall) (ToolResponse, error) {
			log.add(fmt.Sprintf("run %d", in.N))
			return NewTextResponse("ran"), nil
		})
	first := func(yield func(StreamPart) bool) {
		if !yield(toolCallPart("call-1", "work", `{"n":1}`)) {
			return
		}
		if !yield(toolCallPart("call-2", "work", `{"n":2}`)) {
			return
		}
		yield(finishPart(FinishReasonToolCalls))
	}

	agent := NewAgent(earlyDispatchModel(first), WithTools(work))
	_, err := agent.Stream(context.Background(), AgentStreamCall{
		Prompt: "work twice",
		OnToolCall: func(tc ToolCallContent) error {
			log.add("call " + tc.ToolCallID)
			return nil
		},
		OnToolDispatch: func() error {
			log.add("dispatch")
			return nil
		},
	})
	require.NoError(t, err)

	events := log.snapshot()
	require.Len(t, events, 5)
	require.Equal(t, []string{"call call-1", "call call-2", "dispatch"}, events[:3])
	require.ElementsMatch(t, []string{"run 1", "run 2"}, events[3:])
}

// TestStreamingAgent_OnToolDispatchErrorRunsNoTool checks that a failing
// OnToolDispatch fails the step without running any of its tools.
func TestStreamingAgent_OnToolDispatchErrorRunsNoTool(t *testing.T) {
	t.Parallel()

	var ran atomic.Int32
	work := NewAgentTool("work", "must not run",
		func(ctx context.Context, in earlyInput, call ToolCall) (ToolResponse, error) {
			ran.Add(1)
			return NewTextResponse("ran"), nil
		})
	first := func(yield func(StreamPart) bool) {
		if !yield(toolCallPart("call-1", "work", `{"n":1}`)) {
			return
		}
		yield(finishPart(FinishReasonToolCalls))
	}
	errRecord := errors.New("record failed")

	agent := NewAgent(earlyDispatchModel(first), WithTools(work))
	_, err := agent.Stream(context.Background(), AgentStreamCall{
		Prompt:         "work once",
		OnToolDispatch: func() error { return errRecord },
	})
	require.ErrorIs(t, err, errRecord)
	require.Zero(t, ran.Load())
}

// TestStreamingAgent_OnToolDispatchSkippedWithoutDispatch checks that a
// step that runs no tool does not call OnToolDispatch.
func TestStreamingAgent_OnToolDispatchSkippedWithoutDispatch(t *testing.T) {
	t.Parallel()

	var dispatched atomic.Int32
	first := func(yield func(StreamPart) bool) {
		yield(StreamPart{Type: StreamPartTypeTextStart, ID: "t"})
		yield(StreamPart{Type: StreamPartTypeTextDelta, ID: "t", Delta: "hi"})
		yield(StreamPart{Type: StreamPartTypeTextEnd, ID: "t"})
		yield(finishPart(FinishReasonStop))
	}

	agent := NewAgent(earlyDispatchModel(first))
	_, err := agent.Stream(context.Background(), AgentStreamCall{
		Prompt: "just answer",
		OnToolDispatch: func() error {
			dispatched.Add(1)
			return nil
		},
	})
	require.NoError(t, err)
	require.Zero(t, dispatched.Load())
}
