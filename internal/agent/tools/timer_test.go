package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"charm.land/fantasy"
)

// fakeTimers is a timer armer that records what the tool asked for
// instead of arming anything.
type fakeTimers struct {
	timer Timer
	err   error
	calls []timerCall
}

type timerCall struct {
	sessionID string
	seconds   int
}

func (f *fakeTimers) ArmTimer(_ context.Context, sessionID string, seconds int) (Timer, error) {
	f.calls = append(f.calls, timerCall{sessionID: sessionID, seconds: seconds})
	return f.timer, f.err
}

func runTimerTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params TimerParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "test-call", Name: TimerToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

// The tool arms a timer and answers at once: the response names the id,
// when it fires and where its result arrives, and the armer saw the
// call's session and duration.
func TestTimerToolArmsAndReportsTheTimer(t *testing.T) {
	t.Parallel()

	firesAt := time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC)
	armer := &fakeTimers{timer: Timer{ID: "timer-7", Duration: 30 * time.Second, FiresAt: firesAt}}
	tool := NewTimerTool(armer)

	info := tool.Info()
	require.Equal(t, TimerToolName, info.Name)
	require.Equal(t, "timer", TimerToolName)
	require.Equal(t, []string{"seconds"}, info.Required)
	require.Contains(t, info.Description, "1 to 600")
	require.True(t, info.Parallel)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess")
	resp := runTimerTool(t, tool, ctx, TimerParams{Seconds: 30})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "timer-7")
	require.Contains(t, resp.Content, "30s")
	require.Contains(t, resp.Content, firesAt.Format(time.RFC3339))
	require.Contains(t, resp.Content, "wait tool")
	require.Contains(t, resp.Content, "no polling")

	require.Len(t, armer.calls, 1)
	require.Equal(t, "sess", armer.calls[0].sessionID)
	require.Equal(t, 30, armer.calls[0].seconds)
}

// A duration outside the documented bound is refused before anything is
// armed, and the refusal names the bound.
func TestTimerToolRejectsSecondsOutsideTheBound(t *testing.T) {
	t.Parallel()

	armer := &fakeTimers{timer: Timer{ID: "timer-1"}}
	tool := NewTimerTool(armer)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess")

	for _, seconds := range []int{-1, 0, 601, 100000} {
		resp := runTimerTool(t, tool, ctx, TimerParams{Seconds: seconds})
		require.True(t, resp.IsError, seconds)
		require.Contains(t, resp.Content, "between 1 and 600", seconds)
	}
	require.Empty(t, armer.calls, "a refused duration never reaches the registry")
}

// A refusal from the registry, and a call with no session to arm for,
// come back as readable errors rather than a Go error that would abort
// the turn.
func TestTimerToolReportsArmingFailures(t *testing.T) {
	t.Parallel()

	armer := &fakeTimers{err: errors.New("this session already has 8 timers armed")}
	tool := NewTimerTool(armer)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess")

	resp := runTimerTool(t, tool, ctx, TimerParams{Seconds: 5})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "8 timers armed")

	resp = runTimerTool(t, tool, context.Background(), TimerParams{Seconds: 5})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "session ID is required for arming a timer")

	resp = runTimerTool(t, NewTimerTool(nil), ctx, TimerParams{Seconds: 5})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "unavailable")
}
