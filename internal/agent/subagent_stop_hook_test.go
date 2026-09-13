package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/hooks"
)

// TestSubagentStopHook pins the SubagentStop contract end to end: the
// event fires once per dispatched sub-agent when it finishes, the
// matcher is tested against the sub-agent type, the payload carries the
// type and final status, and context returned by the hook is appended to
// the report the orchestrating model receives. This path had never been
// seen working until the deferred hook was fixed to annotate the
// response itself, so it is pinned from the dispatch site down.
func TestSubagentStopHook(t *testing.T) {
	const providerID = "test-provider"

	newCoord := func(t *testing.T, buildCmd func(logPath string) string, matcher string) (fakeEnv, *coordinator, *string) {
		t.Helper()

		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
		logPath := filepath.Join(t.TempDir(), "hooks.log")
		coord.hooks = newTestRegistry(t, map[string][]config.HookConfig{
			hooks.EventSubagentStop: {{
				Matcher: matcher,
				Command: buildCmd(logPath),
			}},
		})
		return env, coord, &logPath
	}

	capture := func(logPath string) string {
		return captureHookCmd(logPath, `printf '%s' '{"context":"child audited"}'`)
	}

	t.Run("completed dispatch: type and status in payload, context in the report", func(t *testing.T) {
		env, coord, logPath := newCoord(t, capture, "")
		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		var childSessionID string
		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return agentResultWithText("child done"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parent.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "do something",
			SessionTitle:   "Test Session",
			AgentName:      "task",
			SessionSetup:   func(sessionID string) { childSessionID = sessionID },
		})
		require.NoError(t, err)
		require.False(t, resp.IsError)
		require.Contains(t, resp.Content, "child done")
		require.Contains(t, resp.Content, "child audited",
			"SubagentStop context must reach the orchestrating model")

		payloads := readHookPayloads(t, *logPath)
		require.Len(t, payloads, 1, "SubagentStop fires once per dispatched sub-agent")
		require.Equal(t, hooks.EventSubagentStop, payloads[0]["event"])
		require.Equal(t, "task", payloads[0]["subagent_type"])
		require.Equal(t, "completed", payloads[0]["message"])
		require.Equal(t, childSessionID, payloads[0]["session_id"],
			"the payload reports the child session the sub-agent ran in")
	})

	t.Run("matcher is tested against the sub-agent type", func(t *testing.T) {
		env, coord, logPath := newCoord(t, func(logPath string) string {
			return captureHookCmd(logPath, "")
		}, "^fast$")
		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return agentResultWithText("child done"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parent.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "do something",
			SessionTitle:   "Test Session",
			AgentName:      "task",
		})
		require.NoError(t, err)
		require.Equal(t, "child done", resp.Content,
			"a non-matching hook must not annotate the report")
		require.Empty(t, readHookPayloads(t, *logPath),
			"a hook scoped to another sub-agent type must not run")
	})

	t.Run("failed dispatch carries the failed status", func(t *testing.T) {
		env, coord, logPath := newCoord(t, func(logPath string) string {
			return captureHookCmd(logPath, "")
		}, "")
		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return nil, errors.New("boom")
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parent.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "do something",
			SessionTitle:   "Test Session",
			AgentName:      "task",
		})
		require.NoError(t, err, "a failed child is a tool error response, not a Go error")
		require.True(t, resp.IsError)

		payloads := readHookPayloads(t, *logPath)
		require.Len(t, payloads, 1)
		require.Equal(t, "failed", payloads[0]["message"])
	})

	t.Run("cancelled dispatch carries the cancelled status", func(t *testing.T) {
		env, coord, logPath := newCoord(t, func(logPath string) string {
			return captureHookCmd(logPath, "")
		}, "")
		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return nil, context.Canceled
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parent.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "do something",
			SessionTitle:   "Test Session",
			AgentName:      "task",
		})
		require.NoError(t, err)
		require.True(t, resp.IsError)

		payloads := readHookPayloads(t, *logPath)
		require.Len(t, payloads, 1)
		require.Equal(t, "cancelled", payloads[0]["message"])
	})
}
