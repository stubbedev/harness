package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/log"
	"github.com/stubbedev/harness/internal/message"
)

// fakeLanguageModel is a [fantasy.LanguageModel] stub that records the
// context its methods were called with and can be configured with a custom
// stream body.
type fakeLanguageModel struct {
	generateCtx context.Context
	streamCtx   context.Context
	stream      func(yield func(fantasy.StreamPart) bool)
}

func (f *fakeLanguageModel) Generate(ctx context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	f.generateCtx = ctx
	return &fantasy.Response{}, nil
}

func (f *fakeLanguageModel) Stream(ctx context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	f.streamCtx = ctx
	if f.stream == nil {
		return func(yield func(fantasy.StreamPart) bool) {
			yield(fantasy.StreamPart{})
		}, nil
	}
	return f.stream, nil
}

func (f *fakeLanguageModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return &fantasy.ObjectResponse{}, nil
}

func (f *fakeLanguageModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}

func (f *fakeLanguageModel) Provider() string { return "fake" }
func (f *fakeLanguageModel) Model() string    { return "fake-model" }

func TestNewRequestTimeoutModel_Disabled(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	require.Same(t, inner, newRequestTimeoutModel(inner, 0))
	require.Same(t, inner, newRequestTimeoutModel(inner, -time.Second))
}

func TestRequestTimeoutModel_GenerateDeadline(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	m := newRequestTimeoutModel(inner, 5*time.Minute)

	_, err := m.Generate(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	_, ok := inner.generateCtx.Deadline()
	require.True(t, ok, "Generate should run under a deadline")
}

func TestRequestTimeoutModel_StreamDeadlineOutlivesCall(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	m := newRequestTimeoutModel(inner, 5*time.Minute)

	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	require.NoError(t, inner.streamCtx.Err(), "the idle timer must not fire while the stream is being consumed")

	for range stream {
	}

	require.ErrorIs(t, inner.streamCtx.Err(), context.Canceled, "the stream context should be released after the stream ends")
}

func TestRequestTimeoutModel_StreamAbortsWhenIdle(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// A stream that outlives the idle window: it waits for the context to
	// be done and reports what it observed.
	streamObserved := make(chan error, 1)
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		<-inner.streamCtx.Done()
		streamObserved <- inner.streamCtx.Err()
	}
	m := newRequestTimeoutModel(inner, 10*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range stream {
		}
	}()

	select {
	case err := <-streamObserved:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("stream was not aborted by the idle timeout")
	}
	<-done
}

func TestRequestTimeoutModel_ActiveStreamSurvives(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// A stream that keeps sending data: total runtime exceeds the timeout,
	// but every gap is shorter than the idle window, so it must finish.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		for range 6 {
			time.Sleep(10 * time.Millisecond)
			if !yield(fantasy.StreamPart{}) {
				return
			}
		}
	}
	m := newRequestTimeoutModel(inner, 25*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	parts := 0
	for part := range stream {
		require.NoError(t, part.Error)
		parts++
	}
	require.Equal(t, 6, parts)
}

// blockingModel blocks until the context is done and then returns the
// context error, the way a hung provider request would.
type blockingModel struct {
	fakeLanguageModel
}

func (b *blockingModel) Generate(ctx context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRequestTimeoutModel_GenerateReportsTimeout(t *testing.T) {
	t.Parallel()

	m := newRequestTimeoutModel(&blockingModel{}, 10*time.Millisecond)

	_, err := m.Generate(t.Context(), fantasy.Call{})
	require.Error(t, err)

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, err, &timeoutErr)
	require.Equal(t, 10*time.Millisecond, timeoutErr.timeout)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the deadline must stay detectable through the chain")
	require.Contains(t, err.Error(), "timed out after 10ms")
}

func TestRequestTimeoutModel_StreamReportsTimeout(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// A provider stream that fails with the context error once the deadline
	// fires, mirroring how SDKs surface mid-stream aborts.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		<-inner.streamCtx.Done()
		yield(fantasy.StreamPart{Error: inner.streamCtx.Err()})
	}
	m := newRequestTimeoutModel(inner, 10*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var got error
	for part := range stream {
		if part.Error != nil {
			got = part.Error
		}
	}

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, got, &timeoutErr)
	require.ErrorIs(t, got, context.DeadlineExceeded)
}

func TestRequestTimeoutModel_ParentCancelPassesThrough(t *testing.T) {
	t.Parallel()

	m := newRequestTimeoutModel(&blockingModel{}, 5*time.Minute)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := m.Generate(ctx, fantasy.Call{})
	require.ErrorIs(t, err, context.Canceled)

	var timeoutErr *requestTimeoutError
	require.NotErrorAs(t, err, &timeoutErr, "user cancellation must not be reported as a timeout")
}

func TestRequestTimeoutErrorMessages(t *testing.T) {
	t.Parallel()

	err := &requestTimeoutError{timeout: time.Second}
	require.Equal(t, "LLM request timed out after 1s", err.Error())
	require.Contains(t, err.userMessage(), "1s")
	require.Contains(t, err.userMessage(), "request-timeout")

	err.cause = context.DeadlineExceeded
	require.Equal(t, "LLM request timed out after 1s: context deadline exceeded", err.Error())

	idle := &requestTimeoutError{timeout: 2 * time.Second, idle: true}
	require.Equal(t, "LLM stream received no data for 2s", idle.Error())
	require.Contains(t, idle.userMessage(), "stopped sending data for 2s")
	require.Contains(t, idle.userMessage(), "request-timeout")
}

// timeoutOnlyModel streams a single error part shaped exactly like the one
// requestTimeoutModel produces when its deadline fires.
type timeoutOnlyModel struct {
	fakeLanguageModel
}

func (m *timeoutOnlyModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	timeoutErr := &requestTimeoutError{timeout: time.Second, idle: true, cause: context.DeadlineExceeded}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: timeoutErr})
	}, nil
}

// TestRequestTimeoutRunFinishMessage pins what the user sees when a request
// exhausts its timeout: a "Request timed out" finish that names the elapsed
// budget and how to change it, instead of a bare provider error.
func TestRequestTimeoutRunFinishMessage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows for now")
	}

	env := testEnv(t)
	model := &timeoutOnlyModel{}
	agent, err := coderAgent(nil, env, model, model)
	require.NoError(t, err)

	session, err := env.sessions.Create(t.Context(), "timeout session")
	require.NoError(t, err)

	_, err = agent.Run(t.Context(), SessionAgentCall{
		Prompt:          "Hello",
		SessionID:       session.ID,
		MaxOutputTokens: 10000,
	})
	require.Error(t, err)

	msgs, err := env.messages.List(t.Context(), session.ID)
	require.NoError(t, err)

	var finish *message.Finish
	for _, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}
		if part := msg.FinishPart(); part != nil {
			finish = part
		}
	}
	require.NotNil(t, finish, "the assistant message should carry a finish part")
	require.Equal(t, message.FinishReasonError, finish.Reason)
	require.Equal(t, "Request timed out", finish.Message)
	require.Contains(t, finish.Details, "stopped sending data for 1s")
	require.Contains(t, finish.Details, "request-timeout")
}

// TestRequestTimeoutModel_PingsKeepToolCallAlive reproduces the stall of
// GitHub issue #129: Anthropic opens a tool_use block, then sends nothing but
// ping events for longer than the idle timeout while it buffers the
// arguments. The SDK drops pings before fantasy sees them, so only the bytes
// on the wire can tell the timer the stream is alive.
func TestRequestTimeoutModel_PingsKeepToolCallAlive(t *testing.T) {
	t.Parallel()

	const idleTimeout = 150 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		send := func(event, data string) {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
			flusher.Flush()
		}
		send("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"write","input":{}}}`)
		for deadline := time.Now().Add(4 * idleTimeout); time.Now().Before(deadline); {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(idleTimeout / 5):
			}
			send("ping", `{"type":"ping"}`)
		}
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.txt\","}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"content\":\"hi\"}"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
		send("message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(server.Close)

	provider, err := anthropic.New(
		anthropic.WithAPIKey("test"),
		anthropic.WithBaseURL(server.URL),
		anthropic.WithHTTPClient(log.NewProviderHTTPClient(false, false)),
	)
	require.NoError(t, err)
	model, err := provider.LanguageModel(t.Context(), "claude-test")
	require.NoError(t, err)

	stream, err := newRequestTimeoutModel(model, idleTimeout).Stream(t.Context(), fantasy.Call{
		Prompt: fantasy.Prompt{fantasy.NewUserMessage("write a.txt")},
	})
	require.NoError(t, err)

	var input string
	var finished bool
	for part := range stream {
		require.NoError(t, part.Error)
		switch part.Type {
		case fantasy.StreamPartTypeToolCall:
			input = part.ToolCallInput
		case fantasy.StreamPartTypeFinish:
			finished = true
		}
	}
	require.True(t, finished, "the stream must run to its finish instead of timing out")
	require.JSONEq(t, `{"path":"a.txt","content":"hi"}`, input)
}

// TestRequestTimeoutModel_StreamReportsTimeoutCause covers the error net/http
// gives a read canceled by the idle timer: the context's cause, which is the
// timeout error itself. Wrapping it in itself made Error recurse forever.
func TestRequestTimeoutModel_StreamReportsTimeoutCause(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		<-inner.streamCtx.Done()
		yield(fantasy.StreamPart{Error: fmt.Errorf("read body: %w", context.Cause(inner.streamCtx))})
	}
	m := newRequestTimeoutModel(inner, 10*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var got error
	for part := range stream {
		if part.Error != nil {
			got = part.Error
		}
	}

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, got, &timeoutErr)
	require.ErrorIs(t, got, context.DeadlineExceeded)
	require.Equal(t, "read body: LLM stream received no data for 10ms: context deadline exceeded", got.Error())
}

// cutOffToolCallModel opens a write call, streams part of its arguments and
// then fails with the idle timeout, as the stream of GitHub issue #129 did.
type cutOffToolCallModel struct {
	fakeLanguageModel
}

func (m *cutOffToolCallModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	timeoutErr := &requestTimeoutError{timeout: time.Second, idle: true, cause: context.DeadlineExceeded}
	return func(yield func(fantasy.StreamPart) bool) {
		_ = yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputStart, ID: "call-1", ToolCallName: "write"}) &&
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputDelta, ID: "call-1", ToolCallInput: `{"path":"a.txt","content":"par`}) &&
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: timeoutErr})
	}, nil
}

// TestRequestTimeoutCutOffToolCallResult pins what the model is told about a
// call whose arguments the timeout cut off: that it never ran, and why,
// instead of the generic tool failure that read as a broken write.
func TestRequestTimeoutCutOffToolCallResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows for now")
	}

	env := testEnv(t)
	model := &cutOffToolCallModel{}
	agent, err := coderAgent(nil, env, model, model)
	require.NoError(t, err)

	session, err := env.sessions.Create(t.Context(), "cut-off tool call session")
	require.NoError(t, err)

	_, err = agent.Run(t.Context(), SessionAgentCall{
		Prompt:          "Write a.txt",
		SessionID:       session.ID,
		MaxOutputTokens: 10000,
	})
	require.Error(t, err)

	msgs, err := env.messages.List(t.Context(), session.ID)
	require.NoError(t, err)

	var calls []message.ToolCall
	var results []message.ToolResult
	for _, msg := range msgs {
		calls = append(calls, msg.ToolCalls()...)
		results = append(results, msg.ToolResults()...)
	}
	require.Len(t, calls, 1)
	require.True(t, calls[0].Finished)
	require.JSONEq(t, `{}`, calls[0].Input, "cut-off arguments are not valid JSON to replay")
	require.Len(t, results, 1)
	require.Equal(t, "call-1", results[0].ToolCallID)
	require.True(t, results[0].IsError)
	require.False(t, results[0].Canceled)
	require.Equal(t,
		"Error: the tool was not run: the response ended before its arguments finished streaming (LLM stream received no data for 1s: context deadline exceeded)",
		results[0].Content)
}
