package agent

import (
	"context"
	"errors"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/config"
)

func sessionCtx(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
}

func TestSendMessageTool_Run(t *testing.T) {
	t.Parallel()

	newTool := func() *sendMessageTool {
		return &sendMessageTool{coord: &coordinator{subagentMessages: newSubagentInbox()}}
	}

	t.Run("records the message on the child session inbox", func(t *testing.T) {
		t.Parallel()

		tool := newTool()
		resp, err := tool.Run(sessionCtx(t.Context(), "child-1"), fantasy.ToolCall{Input: `{"message":"  found it  "}`})
		require.NoError(t, err)
		require.False(t, resp.IsError)

		assert.Equal(t, []string{"found it"}, tool.coord.drainSubagentMessages("child-1"))
	})

	t.Run("appends in order and keeps sessions apart", func(t *testing.T) {
		t.Parallel()

		tool := newTool()
		for _, msg := range []string{"first", "second"} {
			_, err := tool.Run(sessionCtx(t.Context(), "child-1"), fantasy.ToolCall{Input: `{"message":"` + msg + `"}`})
			require.NoError(t, err)
		}
		_, err := tool.Run(sessionCtx(t.Context(), "child-2"), fantasy.ToolCall{Input: `{"message":"other"}`})
		require.NoError(t, err)

		assert.Equal(t, []string{"first", "second"}, tool.coord.drainSubagentMessages("child-1"))
		assert.Equal(t, []string{"other"}, tool.coord.drainSubagentMessages("child-2"))
	})

	t.Run("draining twice yields nothing the second time", func(t *testing.T) {
		t.Parallel()

		tool := newTool()
		_, err := tool.Run(sessionCtx(t.Context(), "child-1"), fantasy.ToolCall{Input: `{"message":"once"}`})
		require.NoError(t, err)

		assert.Equal(t, []string{"once"}, tool.coord.drainSubagentMessages("child-1"))
		assert.Empty(t, tool.coord.drainSubagentMessages("child-1"))
	})

	// Every rejection is a tool-error response rather than a Go error: a
	// malformed send_message must not abort the sub-agent's own run.
	t.Run("rejections", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name     string
			ctx      func(context.Context) context.Context
			input    string
			wants    string
			recorded bool
		}{
			{
				name:  "no session in context",
				ctx:   func(ctx context.Context) context.Context { return ctx },
				input: `{"message":"hi"}`,
				wants: "session id missing from context",
			},
			{
				name:  "malformed input",
				ctx:   func(ctx context.Context) context.Context { return sessionCtx(ctx, "child-1") },
				input: `{"message":`,
				wants: "invalid parameters",
			},
			{
				name:  "empty message",
				ctx:   func(ctx context.Context) context.Context { return sessionCtx(ctx, "child-1") },
				input: `{"message":"   "}`,
				wants: `"message" must not be empty`,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				tool := newTool()
				resp, err := tool.Run(tt.ctx(t.Context()), fantasy.ToolCall{Input: tt.input})
				require.NoError(t, err)
				assert.True(t, resp.IsError)
				assert.Contains(t, resp.Content, tt.wants)
				assert.Empty(t, tool.coord.drainSubagentMessages("child-1"))
			})
		}
	})
}

// TestRunSubAgentDeliversMessages covers the tool's whole point: messages a
// sub-agent sent mid-run reach the orchestrator's dispatch result whatever
// the run's outcome was.
func TestRunSubAgentDeliversMessages(t *testing.T) {
	const providerID = "test-provider"
	providerCfg := config.ProviderConfig{ID: providerID}

	tests := []struct {
		name    string
		result  func(*coordinator, SessionAgentCall) (*fantasy.AgentResult, error)
		isError bool
		content string
	}{
		{
			name: "completed run",
			result: func(_ *coordinator, _ SessionAgentCall) (*fantasy.AgentResult, error) {
				return agentResultWithText("done"), nil
			},
			content: "done",
		},
		{
			name: "failed run",
			result: func(_ *coordinator, _ SessionAgentCall) (*fantasy.AgentResult, error) {
				return nil, errors.New("boom")
			},
			isError: true,
			content: "Failed to generate response: boom",
		},
		{
			name: "cancelled run",
			result: func(_ *coordinator, _ SessionAgentCall) (*fantasy.AgentResult, error) {
				return nil, context.Canceled
			},
			isError: true,
			content: "Subagent cancelled by user",
		},
		{
			name: "run with no text output",
			result: func(_ *coordinator, _ SessionAgentCall) (*fantasy.AgentResult, error) {
				return agentResultWithText(""), nil
			},
			isError: true,
			content: "produced no text output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := testEnv(t)
			coord := newTestCoordinator(t, env, providerID, providerCfg)

			parentSession, err := env.sessions.Create(t.Context(), "Parent")
			require.NoError(t, err)

			agent := newMockAgent(providerID, 4096, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
				// Stand in for the sub-agent calling send_message: the tool
				// reaches the coordinator with the child session ID.
				coord.recordSubagentMessage(call.SessionID, "partial finding")
				return tt.result(coord, call)
			})

			resp, err := coord.runSubAgent(t.Context(), subAgentParams{
				Agent:          agent,
				SessionID:      parentSession.ID,
				AgentMessageID: "msg-1",
				ToolCallID:     "call-1",
				Prompt:         "test",
				SessionTitle:   "Test",
			})
			require.NoError(t, err)
			assert.Equal(t, tt.isError, resp.IsError)
			assert.Contains(t, resp.Content, tt.content)
			assert.Contains(t, resp.Content, "## Messages sent during this run")
			assert.Contains(t, resp.Content, "1. partial finding")
		})
	}
}

func TestAppendSubagentMessages(t *testing.T) {
	t.Parallel()

	t.Run("numbers the messages after the response", func(t *testing.T) {
		t.Parallel()

		resp := fantasy.NewTextResponse("report")
		appendSubagentMessages(&resp, []string{"one", "two"})
		assert.Equal(t, "report\n\n## Messages sent during this run\n1. one\n2. two\n", resp.Content)
	})

	t.Run("leaves the response alone when there is nothing to deliver", func(t *testing.T) {
		t.Parallel()

		resp := fantasy.NewTextResponse("report")
		appendSubagentMessages(&resp, nil)
		assert.Equal(t, "report", resp.Content)

		// A nil response must not panic: the dispatch defer runs on every
		// path, including ones that never assigned a response.
		appendSubagentMessages(nil, []string{"dropped"})
	})
}

// A coordinator with no inbox (a test that never wired one) drops messages
// rather than panicking, since the dispatch defer drains unconditionally.
func TestSubagentInboxOptional(t *testing.T) {
	t.Parallel()

	c := &coordinator{}
	c.recordSubagentMessage("child-1", "dropped")
	assert.Nil(t, c.drainSubagentMessages("child-1"))
}
