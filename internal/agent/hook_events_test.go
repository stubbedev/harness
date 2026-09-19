package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/hooks"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
)

// This file pins the dynamic half of the hook contract described in
// docs/hooks/README.md: that each event actually fires when the docs say
// it does, carrying the payload the reference section promises. Hook
// commands append their stdin payload to a log file behind a marker line
// so tests can assert on exactly what the hook process received.

// shQuotePath single-quotes a path for the embedded shell. Paths come
// from t.TempDir and never contain quotes.
func shQuotePath(s string) string {
	return "'" + s + "'"
}

// captureHookCmd builds a hook command that appends each fire's stdin
// payload to logPath behind a marker line. extra, when non-empty, is
// appended so a hook can capture and decide in one command.
func captureHookCmd(logPath, extra string) string {
	q := shQuotePath(logPath)
	cmd := "echo --- >> " + q + "; cat >> " + q + "; echo >> " + q
	if extra != "" {
		cmd += "; " + extra
	}
	return cmd
}

// readHookPayloads parses the payloads captured by captureHookCmd, in
// fire order. A missing file means the hook never fired.
func readHookPayloads(t *testing.T, logPath string) []map[string]any {
	t.Helper()

	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)

	lines := strings.Split(string(data), "\n")
	var out []map[string]any
	for i := 0; i < len(lines); i++ {
		if lines[i] != "---" {
			continue
		}
		var payload []string
		for i+1 < len(lines) && lines[i+1] != "" && lines[i+1] != "---" {
			i++
			payload = append(payload, lines[i])
		}
		var doc map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.Join(payload, "")), &doc),
			"captured payload is not JSON: %q", payload)
		out = append(out, doc)
	}
	return out
}

// hookedCoderAgent builds a coder agent over a scripted model and wires
// a hooks registry into the session agent, mirroring what the
// coordinator does for the top-level agent in production.
func hookedCoderAgent(
	t *testing.T,
	byEvent map[string][]config.HookConfig,
	turns ...scriptedTurn,
) (*sessionAgent, fakeEnv, *scriptedModel) {
	t.Helper()

	agent, env, large := scriptedAgent(t, nil, turns...)
	sa := agent.(*sessionAgent)
	sa.hooks = newTestRegistry(t, byEvent)
	return sa, env, large
}

// newHookSession creates a session to run hook-test turns in.
func newHookSession(t *testing.T, env fakeEnv) string {
	t.Helper()

	sess, err := env.sessions.Create(t.Context(), "Hook Test")
	require.NoError(t, err)
	return sess.ID
}

// runHookTurn runs one agent turn, returning the run result and error
// without asserting on either.
func runHookTurn(t *testing.T, sa SessionAgent, sessionID, prompt string) (*fantasy.AgentResult, error) {
	t.Helper()

	return sa.Run(t.Context(), SessionAgentCall{
		Prompt:          prompt,
		SessionID:       sessionID,
		MaxOutputTokens: 10000,
	})
}

// lastUserText returns the text of the last user message in a model
// call's prompt: the outbound prompt the model actually saw.
func lastUserText(t *testing.T, call fantasy.Call) string {
	t.Helper()

	for _, msg := range slices.Backward(call.Prompt) {
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		var sb strings.Builder
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && !strings.HasPrefix(text.Text, "<harness_runtime>\n") {
				sb.WriteString(text.Text)
			}
		}
		return sb.String()
	}
	t.Fatal("no user message in the model call prompt")
	return ""
}

// storedUserText returns the text of the stored user message, which must
// keep the prompt exactly as the user typed it.
func storedUserText(t *testing.T, env fakeEnv, sessionID string) string {
	t.Helper()

	msgs, err := env.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	for _, msg := range msgs {
		if msg.Role == message.User {
			return msg.Content().Text
		}
	}
	return ""
}

func TestUserPromptSubmitHook(t *testing.T) {
	t.Parallel()

	t.Run("fires before the model and context reaches it", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		sa, env, large := hookedCoderAgent(t,
			map[string][]config.HookConfig{
				hooks.EventUserPromptSubmit: {{
					Command: captureHookCmd(logPath, `printf '%s' '{"context":"current branch: main"}'`),
				}},
			},
			scriptedTurn{text: "ok"},
		)
		sessionID := newHookSession(t, env)

		_, err := runHookTurn(t, sa, sessionID, "Fix the login flow")
		require.NoError(t, err)

		payloads := readHookPayloads(t, logPath)
		require.Len(t, payloads, 1, "UserPromptSubmit must fire exactly once per dispatched prompt")
		require.Equal(t, hooks.EventUserPromptSubmit, payloads[0]["event"])
		require.Equal(t, "Fix the login flow", payloads[0]["prompt"])
		require.Equal(t, sessionID, payloads[0]["session_id"])

		calls := large.sentCalls()
		require.NotEmpty(t, calls)
		require.Contains(t, lastUserText(t, calls[0]),
			"<hook-context>\ncurrent branch: main\n</hook-context>",
			"hook context must be appended to the outbound prompt inside a hook-context block")
		require.Equal(t, "Fix the login flow", storedUserText(t, env, sessionID),
			"the stored message must keep the prompt as typed")
	})

	t.Run("updated_prompt rewrites only the outbound prompt", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		sa, env, large := hookedCoderAgent(t,
			map[string][]config.HookConfig{
				hooks.EventUserPromptSubmit: {{
					Command: captureHookCmd(logPath, `printf '%s' '{"updated_prompt":"fix the login flow on the staging stack"}'`),
				}},
			},
			scriptedTurn{text: "ok"},
		)
		sessionID := newHookSession(t, env)

		_, err := runHookTurn(t, sa, sessionID, "fix the login flow")
		require.NoError(t, err)

		calls := large.sentCalls()
		require.NotEmpty(t, calls)
		require.Equal(t, "fix the login flow on the staging stack", lastUserText(t, calls[0]),
			"the model must see the rewritten prompt and nothing else")
		require.Equal(t, "fix the login flow", storedUserText(t, env, sessionID),
			"the transcript must stay honest about what the user typed")
	})

	t.Run("deny blocks the turn before anything is persisted", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		sa, env, _ := hookedCoderAgent(t,
			map[string][]config.HookConfig{
				hooks.EventUserPromptSubmit: {{
					Command: captureHookCmd(logPath, `echo "mentions production.env" >&2; exit 2`),
				}},
			},
			scriptedTurn{text: "ok"},
		)
		sessionID := newHookSession(t, env)

		_, err := runHookTurn(t, sa, sessionID, "deploy production.env")
		require.ErrorContains(t, err, "prompt blocked by hook: mentions production.env")

		msgs, listErr := env.messages.List(t.Context(), sessionID)
		require.NoError(t, listErr)
		require.Empty(t, msgs, "a denied prompt must leave no turn behind")
		require.Len(t, readHookPayloads(t, logPath), 1, "the hook still ran and saw the prompt")
	})

	t.Run("halt blocks the turn like deny", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		sa, env, _ := hookedCoderAgent(t,
			map[string][]config.HookConfig{
				hooks.EventUserPromptSubmit: {{
					Command: captureHookCmd(logPath, `printf '%s' '{"halt":true,"reason":"stop here"}'`),
				}},
			},
			scriptedTurn{text: "ok"},
		)
		sessionID := newHookSession(t, env)

		_, err := runHookTurn(t, sa, sessionID, "go on")
		require.ErrorContains(t, err, "prompt blocked by hook: stop here")

		msgs, listErr := env.messages.List(t.Context(), sessionID)
		require.NoError(t, listErr)
		require.Empty(t, msgs, "a halted prompt must leave no turn behind")
	})
}

func TestSessionStartHook(t *testing.T) {
	t.Parallel()

	logPath := filepath.Join(t.TempDir(), "hooks.log")
	sa, env, large := hookedCoderAgent(t,
		map[string][]config.HookConfig{
			hooks.EventSessionStart: {{
				Command: captureHookCmd(logPath, `printf '%s' '{"context":"remember to run gofumpt"}'`),
			}},
		},
		scriptedTurn{text: "first answer"},
	)
	sessionID := newHookSession(t, env)

	_, err := runHookTurn(t, sa, sessionID, "First prompt")
	require.NoError(t, err)

	payloads := readHookPayloads(t, logPath)
	require.Len(t, payloads, 1, "SessionStart must fire on the first prompt of a session")
	require.Equal(t, hooks.EventSessionStart, payloads[0]["event"])
	require.Equal(t, "First prompt", payloads[0]["prompt"])
	require.Equal(t, sessionID, payloads[0]["session_id"])

	calls := large.sentCalls()
	require.NotEmpty(t, calls)
	require.Contains(t, lastUserText(t, calls[0]),
		"<hook-context>\nremember to run gofumpt\n</hook-context>",
		"SessionStart context must reach the first outbound prompt")

	// The second prompt of the session must not fire it again, and its
	// outbound prompt must not carry the stale context.
	_, err = runHookTurn(t, sa, sessionID, "Second prompt")
	require.NoError(t, err)

	require.Len(t, readHookPayloads(t, logPath), 1,
		"SessionStart must fire only on the first prompt of a session")
	calls = large.sentCalls()
	require.Len(t, calls, 2)
	require.NotContains(t, lastUserText(t, calls[1]), "hook-context")
}

func TestStopHook(t *testing.T) {
	t.Parallel()

	t.Run("fires once after a successful top-level turn", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		sa, env, _ := hookedCoderAgent(t,
			map[string][]config.HookConfig{
				hooks.EventStop: {{Command: captureHookCmd(logPath, "")}},
			},
			scriptedTurn{text: "all done"},
		)
		sessionID := newHookSession(t, env)

		_, err := runHookTurn(t, sa, sessionID, "do the thing")
		require.NoError(t, err)

		payloads := readHookPayloads(t, logPath)
		require.Len(t, payloads, 1, "Stop must fire once when the top-level agent finishes a turn")
		require.Equal(t, hooks.EventStop, payloads[0]["event"])
		require.Equal(t, sessionID, payloads[0]["session_id"])
		require.NotContains(t, payloads[0], "prompt", "Stop carries only the common fields")
	})

	t.Run("does not fire when the turn errors", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		env := testEnv(t)
		agent, err := coderAgent(nil, env, &alwaysFailModel{}, retryNotifyTitleModel{})
		require.NoError(t, err)
		sa := agent.(*sessionAgent)
		sa.hooks = newTestRegistry(t, map[string][]config.HookConfig{
			hooks.EventStop: {{Command: captureHookCmd(logPath, "")}},
		})
		sessionID := newHookSession(t, env)

		_, runErr := runHookTurn(t, sa, sessionID, "go")
		require.Error(t, runErr)

		require.Empty(t, readHookPayloads(t, logPath),
			"Stop fires when the agent finishes a turn, not when it errors")
	})

	t.Run("does not fire for sub-agents", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		env := testEnv(t)
		sa := NewSessionAgent(SessionAgentOptions{
			LargeModel:           Model{Model: textModel("done"), CatalogCfg: catalog.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}},
			SmallModel:           Model{Model: textModel("A Session"), CatalogCfg: catalog.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}},
			SystemPrompt:         "fake system prompt",
			IsSubAgent:           true,
			DisableAutoSummarize: true,
			Sessions:             env.sessions,
			Messages:             env.messages,
			Hooks: newTestRegistry(t, map[string][]config.HookConfig{
				hooks.EventStop:             {{Command: captureHookCmd(logPath, "")}},
				hooks.EventSessionStart:     {{Command: captureHookCmd(logPath, "")}},
				hooks.EventUserPromptSubmit: {{Command: captureHookCmd(logPath, "")}},
			}),
		}).(*sessionAgent)
		sessionID := newHookSession(t, env)

		_, err := runHookTurn(t, sa, sessionID, "child task")
		require.NoError(t, err)

		require.Empty(t, readHookPayloads(t, logPath),
			"sub-agents must not fire the top-level turn and prompt events")
	})
}

func TestNotificationHook(t *testing.T) {
	t.Parallel()

	newBroker := func(t *testing.T) *pubsub.Broker[notify.Notification] {
		t.Helper()
		broker := pubsub.NewBroker[notify.Notification]()
		t.Cleanup(broker.Shutdown)
		return broker
	}

	t.Run("fires with agent_finished when the turn completes", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		sa, env, _ := hookedCoderAgent(t,
			map[string][]config.HookConfig{
				hooks.EventNotification: {{Command: captureHookCmd(logPath, "")}},
			},
			scriptedTurn{text: "ok"},
		)
		sa.notify = newBroker(t)
		sessionID := newHookSession(t, env)

		_, err := runHookTurn(t, sa, sessionID, "hello")
		require.NoError(t, err)

		payloads := readHookPayloads(t, logPath)
		require.Len(t, payloads, 1)
		require.Equal(t, hooks.EventNotification, payloads[0]["event"])
		require.Equal(t, "agent_finished", payloads[0]["notification_type"])
		require.Equal(t, sessionID, payloads[0]["session_id"])
		require.NotContains(t, payloads[0], "message", "agent_finished carries no message")
	})

	t.Run("fires with agent_retrying on a provider retry", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		env := testEnv(t)
		agent, err := coderAgent(nil, env, &flakyStreamModel{}, retryNotifyTitleModel{})
		require.NoError(t, err)
		sa := agent.(*sessionAgent)
		sa.hooks = newTestRegistry(t, map[string][]config.HookConfig{
			hooks.EventNotification: {{Command: captureHookCmd(logPath, "")}},
		})
		sa.notify = newBroker(t)
		one := 1
		sa.maxRetries = &one
		sessionID := newHookSession(t, env)

		_, err = runHookTurn(t, sa, sessionID, "hello")
		require.NoError(t, err)

		payloads := readHookPayloads(t, logPath)
		require.Len(t, payloads, 2,
			"the retry notice while the turn is in flight, then agent_finished once it completes")
		require.Equal(t, "agent_retrying", payloads[0]["notification_type"])
		require.Contains(t, payloads[0]["message"], "overloaded")
		require.Contains(t, payloads[0]["message"], "attempt 1")
		require.Equal(t, "agent_finished", payloads[1]["notification_type"])
	})

	t.Run("fires retrying then error when retries run out", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		env := testEnv(t)
		agent, err := coderAgent(nil, env, &alwaysFailModel{}, retryNotifyTitleModel{})
		require.NoError(t, err)
		sa := agent.(*sessionAgent)
		sa.hooks = newTestRegistry(t, map[string][]config.HookConfig{
			hooks.EventNotification: {{Command: captureHookCmd(logPath, "")}},
		})
		sa.notify = newBroker(t)
		one := 1
		sa.maxRetries = &one
		sessionID := newHookSession(t, env)

		_, err = runHookTurn(t, sa, sessionID, "hello")
		require.Error(t, err)

		payloads := readHookPayloads(t, logPath)
		require.Len(t, payloads, 2, "one per-attempt notice followed by one terminal error notice")
		require.Equal(t, "agent_retrying", payloads[0]["notification_type"])
		require.Equal(t, "error", payloads[1]["notification_type"])
		require.Contains(t, payloads[1]["message"], "failed after 1 retry")
	})
}

func TestCompactHooks(t *testing.T) {
	t.Parallel()

	t.Run("fire around a manual summarize carrying the focus instructions", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		sa, env, large := hookedCoderAgent(t,
			map[string][]config.HookConfig{
				hooks.EventPreCompact:  {{Command: captureHookCmd(logPath, "")}},
				hooks.EventPostCompact: {{Command: captureHookCmd(logPath, "")}},
			},
			scriptedTurn{text: "some work"},
		)
		sessionID := newHookSession(t, env)
		// Summaries are the small model's job; give it a scripted one so
		// the summary request can be inspected.
		small := textModel("the summary")
		sa.smallModel.Set(Model{Model: small})

		_, err := runHookTurn(t, sa, sessionID, "do some work")
		require.NoError(t, err)
		require.Empty(t, readHookPayloads(t, logPath), "compaction hooks must not fire on a plain turn")

		require.NoError(t, sa.Summarize(t.Context(), sessionID, nil, nil, "Focus on the API changes"))

		payloads := readHookPayloads(t, logPath)
		require.Len(t, payloads, 2, "PreCompact and PostCompact must each fire exactly once")
		require.Equal(t, hooks.EventPreCompact, payloads[0]["event"], "PreCompact fires first")
		require.Equal(t, "manual", payloads[0]["trigger"])
		require.Equal(t, sessionID, payloads[0]["session_id"])
		require.Equal(t, hooks.EventPostCompact, payloads[1]["event"])
		require.Equal(t, "manual", payloads[1]["trigger"])

		require.Len(t, large.sentCalls(), 1, "the large model only answered the turn")
		// The small model also titles the session, so the summary is its
		// latest call.
		calls := small.sentCalls()
		require.NotEmpty(t, calls)
		summaryCall := calls[len(calls)-1]
		require.Contains(t, lastUserText(t, summaryCall), "## Focus")
		require.Contains(t, lastUserText(t, summaryCall), "Focus on the API changes",
			"the /compact focus instructions must steer the summary prompt")
	})

	t.Run("fire with trigger auto when the context window threshold is hit", func(t *testing.T) {
		t.Parallel()

		logPath := filepath.Join(t.TempDir(), "hooks.log")
		env := testEnv(t)

		// The model reports enough usage that the session crosses the
		// auto-summarize threshold of its small context window after the
		// first step: threshold is a fifth of 1200 = 240, and the step
		// reports 1010 tokens, leaving 190.
		large := newScriptedModel(scriptedTurn{text: "hello"})
		large.usage = fantasy.Usage{InputTokens: 950, OutputTokens: 60, TotalTokens: 1010}
		sa := NewSessionAgent(SessionAgentOptions{
			LargeModel:   Model{Model: large, CatalogCfg: catalog.Model{ContextWindow: 1200, DefaultMaxTokens: 4096}},
			SmallModel:   Model{Model: textModel("summary"), CatalogCfg: catalog.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}},
			SystemPrompt: "fake system prompt",
			Sessions:     env.sessions,
			Messages:     env.messages,
			Hooks: newTestRegistry(t, map[string][]config.HookConfig{
				hooks.EventPreCompact:  {{Command: captureHookCmd(logPath, "")}},
				hooks.EventPostCompact: {{Command: captureHookCmd(logPath, "")}},
			}),
		}).(*sessionAgent)
		sessionID := newHookSession(t, env)

		_, err := runHookTurn(t, sa, sessionID, "hello")
		require.NoError(t, err)

		payloads := readHookPayloads(t, logPath)
		require.Len(t, payloads, 2, "the threshold hit must summarize, firing both compaction hooks")
		require.Equal(t, hooks.EventPreCompact, payloads[0]["event"])
		require.Equal(t, "auto", payloads[0]["trigger"])
		require.Equal(t, hooks.EventPostCompact, payloads[1]["event"])
		require.Equal(t, "auto", payloads[1]["trigger"])
	})
}
