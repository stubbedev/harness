package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// fakeWaiter records what the tool asked for and answers with a fixed
// report, so the tool's own contract is what the test sees.
type fakeWaiter struct {
	report  WaitReport
	resp    fantasy.ToolResponse
	calls   []waitCall
	respond bool
}

type waitCall struct {
	sessionID      string
	waitFor        []string
	timeoutSeconds *int
}

func (f *fakeWaiter) Wait(_ context.Context, sessionID string, waitFor []string, timeoutSeconds *int) fantasy.ToolResponse {
	f.calls = append(f.calls, waitCall{sessionID: sessionID, waitFor: waitFor, timeoutSeconds: timeoutSeconds})
	if f.respond {
		return f.resp
	}
	return fantasy.NewTextResponse(RenderWaitReport(f.report))
}

func runWaitTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, input string) fantasy.ToolResponse {
	t.Helper()
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "test-call", Name: WaitToolName, Input: input})
	require.NoError(t, err)
	return resp
}

func waitCtx() context.Context {
	return context.WithValue(context.Background(), SessionIDContextKey, "sess")
}

func TestWaitToolInfo(t *testing.T) {
	t.Parallel()

	info := NewWaitTool(&fakeWaiter{}).Info()
	require.Equal(t, WaitToolName, info.Name)
	require.Equal(t, "wait", WaitToolName)
	require.True(t, info.Parallel)
	require.Empty(t, info.Required, "an empty call is the widen form")

	params, ok := info.Parameters["wait_for"].(map[string]any)
	require.True(t, ok, "wait_for is a parameter")
	require.Equal(t, "array", params["type"])
	require.Contains(t, params["description"], "job-N")
	require.Contains(t, params["description"], "timer-N")

	timeout, ok := info.Parameters["timeout_seconds"].(map[string]any)
	require.True(t, ok, "timeout_seconds is a parameter")
	require.Equal(t, "integer", timeout["type"])
	require.Contains(t, timeout["description"], "600")
	require.Contains(t, timeout["description"], "3600")

	// The schema must marshal to JSON without a null required list: some
	// providers reject the whole request otherwise.
	data, err := json.Marshal(info)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"required":null`)

	// The description states the bounds the coordinator clamps to; keep
	// the prose from drifting away from the constants.
	require.Contains(t, info.Description, strconv.Itoa(DefaultWaitTimeoutSeconds))
	require.Contains(t, info.Description, strconv.Itoa(MaxWaitTimeoutSeconds))
}

func TestWaitToolPassesParamsThrough(t *testing.T) {
	t.Parallel()

	waiter := &fakeWaiter{report: WaitReport{Summary: "Every waited piece of work finished"}}
	tool := NewWaitTool(waiter)

	resp := runWaitTool(t, tool, waitCtx(), `{"wait_for":["job-1","timer-2"],"timeout_seconds":30}`)
	require.False(t, resp.IsError, resp.Content)
	require.Len(t, waiter.calls, 1)
	require.Equal(t, "sess", waiter.calls[0].sessionID)
	require.Equal(t, []string{"job-1", "timer-2"}, waiter.calls[0].waitFor)
	require.NotNil(t, waiter.calls[0].timeoutSeconds)
	require.Equal(t, 30, *waiter.calls[0].timeoutSeconds)
}

// TestWaitToolEmptyInputWaitsOnEverything pins the empty call: no
// wait_for and no timeout reach the waiter as their zero values.
func TestWaitToolEmptyInputWaitsOnEverything(t *testing.T) {
	t.Parallel()

	waiter := &fakeWaiter{report: WaitReport{Summary: "Snapshot (timeout 0): nothing has finished yet."}}
	resp := runWaitTool(t, NewWaitTool(waiter), waitCtx(), `{}`)
	require.False(t, resp.IsError, resp.Content)
	require.Len(t, waiter.calls, 1)
	require.Empty(t, waiter.calls[0].waitFor)
	require.Nil(t, waiter.calls[0].timeoutSeconds)
}

func TestWaitToolRejections(t *testing.T) {
	t.Parallel()

	t.Run("nil waiter", func(t *testing.T) {
		t.Parallel()
		resp := runWaitTool(t, NewWaitTool(nil), waitCtx(), `{}`)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "waiting is unavailable")
	})

	t.Run("no session", func(t *testing.T) {
		t.Parallel()
		resp := runWaitTool(t, NewWaitTool(&fakeWaiter{}), context.Background(), `{}`)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "session ID is required")
	})

	t.Run("malformed input", func(t *testing.T) {
		t.Parallel()
		resp := runWaitTool(t, NewWaitTool(&fakeWaiter{}), waitCtx(), `{"wait_for":`)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "invalid parameters")
	})

	t.Run("waiter failure is a tool error", func(t *testing.T) {
		t.Parallel()
		waiter := &fakeWaiter{respond: true, resp: fantasy.NewTextErrorResponse("unknown wait target(s): job-9")}
		resp := runWaitTool(t, NewWaitTool(waiter), waitCtx(), `{"wait_for":["job-9"]}`)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "unknown wait target(s): job-9")
	})
}

// TestRenderWaitReportSnapshot pins the model-facing snapshot: a running
// job with its elapsed time and output tail, an armed timer with its
// remaining time, a queued agent and a finished result, each on its own.
func TestRenderWaitReportSnapshot(t *testing.T) {
	t.Parallel()

	exitCode := 3
	report := WaitReport{
		Summary: "Snapshot (timeout 0): nothing has finished yet.",
		Items: []WaitItem{
			{
				Kind:     WaitItemJob,
				ID:       "job-1",
				Subject:  "go test ./...",
				Status:   "running",
				Elapsed:  3200 * time.Millisecond,
				Output:   "ok  \texample.com/a\n",
				Finished: false,
			},
			{
				Kind:     WaitItemJob,
				ID:       "job-2",
				Subject:  "make",
				Status:   "failed",
				Finished: true,
				Elapsed:  time.Second,
				ExitCode: &exitCode,
				Output:   "boom",
			},
			{
				Kind:      WaitItemTimer,
				ID:        "timer-1",
				Subject:   "30s",
				Status:    "armed",
				Remaining: 12 * time.Second,
			},
			{
				Kind:             WaitItemTimer,
				ID:               "timer-2",
				Subject:          "5s",
				Status:           "fired",
				Finished:         true,
				AlreadyCollected: true,
			},
			{
				Kind:     WaitItemAgent,
				ID:       "bg-abcd1234",
				Subject:  "fast",
				Status:   "running",
				Queued:   true,
				Finished: false,
			},
			{
				Kind:     WaitItemAgent,
				ID:       "bg-5678",
				Subject:  "task",
				Status:   "completed",
				Finished: true,
				Result:   "the answer",
			},
			{
				Kind:      WaitItemAgent,
				ID:        "bg-9abc",
				Subject:   "task",
				Status:    "failed",
				Finished:  true,
				Result:    "boom",
				ResultErr: true,
			},
		},
		Messages: []WaitMessage{{AgentName: "task", Handle: "bg-5678", Text: "found it early"}},
	}

	got := RenderWaitReport(report)

	for _, want := range []string{
		"Snapshot (timeout 0): nothing has finished yet.",
		"background job job-1 (go test ./...): running for 3.2s",
		"    ok  \texample.com/a",
		"background job job-2 (make): failed, exit code 3 after 1s",
		"    boom",
		"timer timer-1 (30s): armed; fires in 12s",
		"timer timer-2 (5s): fired; result already returned by an earlier wait",
		"fast (bg-abcd1234): queued for a concurrency slot",
		"task (bg-5678): completed\n  Result:\nthe answer",
		"task (bg-9abc): failed\n  Error: boom",
		"[1] Message from task (bg-5678):\nfound it early",
	} {
		require.Contains(t, got, want)
	}
	require.True(t, strings.HasSuffix(got, "\n"), "the report ends with a newline")
	require.NotContains(t, got, "\n\n\n", "items are separated by one blank line")
}

func TestRenderWaitReportCollectedJobShowsItsTail(t *testing.T) {
	t.Parallel()

	report := WaitReport{
		Summary: "Every waited piece of work finished; the results are below.",
		Items: []WaitItem{{
			Kind:     WaitItemJob,
			ID:       "job-1",
			Subject:  "echo hi",
			Status:   "exited",
			Finished: true,
			Elapsed:  1500 * time.Millisecond,
			ExitCode: new(int),
			Output:   "hi\n",
		}},
	}
	var buf strings.Builder
	buf.WriteString(RenderWaitReport(report))
	require.Contains(t, buf.String(), "background job job-1 (echo hi): exited, exit code 0 after 1.5s")
	require.Contains(t, buf.String(), "\n  Output (tail):\n    hi")
}

// TestWaitToolNilWaiterIgnoresMalformedInput pins the order of the tool's
// guards: a nil waiter fails before the input is even decoded.
func TestWaitToolNilWaiterIgnoresMalformedInput(t *testing.T) {
	t.Parallel()

	resp := runWaitTool(t, NewWaitTool(nil), context.Background(), `not json`)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "waiting is unavailable")
}
