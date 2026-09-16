package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"sync"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// scriptedModel is a fantasy.LanguageModel that replays a fixed script
// instead of calling a provider.
//
// The agent tests below exercise the agent loop - that a tool call the
// model asks for is dispatched, that its result is persisted and paired
// back to the call by id, that parallel calls all land. None of that
// needs a real model, only a decision to react to. Recording a live
// provider to supply those decisions made the fixtures depend on an API
// key and made them drift: the recorded request carries the system
// prompt, every tool definition, and every prior tool result, so editing
// a prompt or changing a tool's output text invalidated cassettes that
// were testing something else entirely.
//
// A script is the same decision with none of that coupling. What the
// model chose is written in the test that needs it, and nothing outside
// that test can invalidate it.
type scriptedModel struct {
	provider string
	model    string

	// usage is reported on every scripted finish. Tests that need to
	// push a session over the auto-summarize threshold override it.
	usage fantasy.Usage

	mu    sync.Mutex
	turns []scriptedTurn
	next  int
	// calls records what the agent actually sent, so a test can assert on
	// the prompt or the advertised tools when that is the point.
	calls []fantasy.Call
}

// scriptedTurn is one model response: some text, and any tool calls the
// model decides to make alongside it.
type scriptedTurn struct {
	text  string
	calls []scriptedCall
	// finish overrides the finish reason the turn ends with; empty means
	// stop for a text turn and tool-calls for a turn with calls. Tests of
	// the max_tokens continuation set it to fantasy.FinishReasonLength.
	finish fantasy.FinishReason
}

// scriptedCall is one tool call in a turn. Input is marshalled to JSON,
// so tests write the arguments as a map rather than as a quoted string.
type scriptedCall struct {
	name  string
	input map[string]any
}

func newScriptedModel(turns ...scriptedTurn) *scriptedModel {
	return &scriptedModel{
		provider: "scripted",
		model:    "scripted-model",
		turns:    turns,
		usage: fantasy.Usage{
			InputTokens:  10,
			OutputTokens: 10,
			TotalTokens:  20,
		},
	}
}

// textModel is the trivial script: answer once, in words, and stop. It
// stands in for the small model that generates titles and summaries.
func textModel(text string) *scriptedModel {
	return newScriptedModel(scriptedTurn{text: text})
}

func (m *scriptedModel) Provider() string { return m.provider }
func (m *scriptedModel) Model() string    { return m.model }

// take returns the next scripted turn. Once the script runs out the model
// keeps answering with plain text, which ends the loop: a test scripts the
// turns it cares about and does not have to spell out the trailing one.
func (m *scriptedModel) take(call fantasy.Call) scriptedTurn {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
	if m.next >= len(m.turns) {
		return scriptedTurn{text: "done"}
	}
	turn := m.turns[m.next]
	m.next++
	return turn
}

// sentCalls returns the calls the agent made, oldest first.
func (m *scriptedModel) sentCalls() []fantasy.Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]fantasy.Call(nil), m.calls...)
}

func (m *scriptedModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	turn := m.take(call)
	// The call id must be unique across the whole conversation: results
	// are paired back to calls by it, and a repeated id would make a
	// second turn's result overwrite the first.
	base := m.next

	return iter.Seq[fantasy.StreamPart](func(yield func(fantasy.StreamPart) bool) {
		if turn.text != "" {
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "text-0"}) {
				return
			}
			if !yield(fantasy.StreamPart{
				Type:  fantasy.StreamPartTypeTextDelta,
				ID:    "text-0",
				Delta: turn.text,
			}) {
				return
			}
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "text-0"}) {
				return
			}
		}

		for i, c := range turn.calls {
			input, err := json.Marshal(c.input)
			if err != nil {
				yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: err})
				return
			}
			if !yield(fantasy.StreamPart{
				Type:          fantasy.StreamPartTypeToolCall,
				ID:            fmt.Sprintf("call-%d-%d", base, i),
				ToolCallName:  c.name,
				ToolCallInput: string(input),
			}) {
				return
			}
		}

		reason := fantasy.FinishReasonStop
		if len(turn.calls) > 0 {
			reason = fantasy.FinishReasonToolCalls
		}
		if turn.finish != "" {
			reason = turn.finish
		}
		yield(fantasy.StreamPart{
			Type:         fantasy.StreamPartTypeFinish,
			FinishReason: reason,
			Usage:        m.usage,
		})
	}), nil
}

func (m *scriptedModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	turn := m.take(call)
	var content fantasy.ResponseContent
	if turn.text != "" {
		content = append(content, fantasy.TextContent{Text: turn.text})
	}
	return &fantasy.Response{
		Content:      content,
		FinishReason: fantasy.FinishReasonStop,
		Usage:        m.usage,
	}, nil
}

func (m *scriptedModel) GenerateObject(ctx context.Context, call fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("scripted model: GenerateObject is not scripted")
}

func (m *scriptedModel) StreamObject(ctx context.Context, call fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("scripted model: StreamObject is not scripted")
}

var _ fantasy.LanguageModel = (*scriptedModel)(nil)

func TestScriptedModel_StreamsTextThenStops(t *testing.T) {
	t.Parallel()

	m := textModel("hello there")
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var text string
	var reason fantasy.FinishReason
	for part := range stream {
		switch part.Type {
		case fantasy.StreamPartTypeTextDelta:
			text += part.Delta
		case fantasy.StreamPartTypeFinish:
			reason = part.FinishReason
		}
	}

	require.Equal(t, "hello there", text)
	require.Equal(t, fantasy.FinishReasonStop, reason)
}

func TestScriptedModel_StreamsToolCallsAndFinishesForThem(t *testing.T) {
	t.Parallel()

	m := newScriptedModel(scriptedTurn{
		text: "looking",
		calls: []scriptedCall{
			{name: "glob", input: map[string]any{"pattern": "*.go"}},
			{name: "ls", input: map[string]any{"path": "."}},
		},
	})

	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var names, ids []string
	inputs := map[string]string{}
	var reason fantasy.FinishReason
	for part := range stream {
		switch part.Type {
		case fantasy.StreamPartTypeToolCall:
			names = append(names, part.ToolCallName)
			ids = append(ids, part.ID)
			inputs[part.ToolCallName] = part.ToolCallInput
		case fantasy.StreamPartTypeFinish:
			reason = part.FinishReason
		}
	}

	require.JSONEq(t, `{"pattern":"*.go"}`, inputs["glob"])
	require.JSONEq(t, `{"path":"."}`, inputs["ls"])

	require.Equal(t, []string{"glob", "ls"}, names)
	require.Equal(t, fantasy.FinishReasonToolCalls, reason)
	require.NotEqual(t, ids[0], ids[1], "tool call ids must be unique")
}

// Ids must stay unique across turns, not just within one: a result is
// paired back to its call by id.
func TestScriptedModel_CallIDsAreUniqueAcrossTurns(t *testing.T) {
	t.Parallel()

	m := newScriptedModel(
		scriptedTurn{calls: []scriptedCall{{name: "ls"}}},
		scriptedTurn{calls: []scriptedCall{{name: "ls"}}},
	)

	seen := map[string]bool{}
	for range 2 {
		stream, err := m.Stream(t.Context(), fantasy.Call{})
		require.NoError(t, err)
		for part := range stream {
			if part.Type == fantasy.StreamPartTypeToolCall {
				require.False(t, seen[part.ID], "duplicate tool call id %q", part.ID)
				seen[part.ID] = true
			}
		}
	}
	require.Len(t, seen, 2)
}

func TestScriptedModel_RunsOutIntoPlainText(t *testing.T) {
	t.Parallel()

	m := newScriptedModel(scriptedTurn{calls: []scriptedCall{{name: "ls"}}})

	// First turn is the scripted one, the second falls off the end.
	for _, want := range []fantasy.FinishReason{fantasy.FinishReasonToolCalls, fantasy.FinishReasonStop} {
		stream, err := m.Stream(t.Context(), fantasy.Call{})
		require.NoError(t, err)
		var got fantasy.FinishReason
		for part := range stream {
			if part.Type == fantasy.StreamPartTypeFinish {
				got = part.FinishReason
			}
		}
		require.Equal(t, want, got)
	}
}

func TestScriptedModel_RecordsWhatTheAgentSent(t *testing.T) {
	t.Parallel()

	m := textModel("ok")
	_, err := m.Stream(t.Context(), fantasy.Call{UserAgent: "probe"})
	require.NoError(t, err)

	sent := m.sentCalls()
	require.Len(t, sent, 1)
	require.Equal(t, "probe", sent[0].UserAgent)
}
