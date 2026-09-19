package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/hooks"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/verification"
)

func TestCompletionVerificationRunsWithoutModelRoundTrip(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	file := filepath.Join(env.workingDir, "main.go")
	require.NoError(t, os.WriteFile(file, []byte("package main\n"), 0o600))
	sess, err := env.sessions.Create(t.Context(), "verification")
	require.NoError(t, err)
	createMessage(t, env, sess.ID, message.User, message.TextContent{Text: "existing task"})
	_, err = env.history.Create(t.Context(), sess.ID, file, "")
	require.NoError(t, err)
	cfg := verification.VerificationConfig{RequireOnCompletion: true, Rules: []verification.Rule{{Name: "check", Paths: []string{"**/*.go"}, Command: []string{"unused"}}}}
	runner, err := verification.New(env.workingDir, cfg)
	require.NoError(t, err)
	called := 0
	verifier := fantasy.NewAgentTool(VerificationToolName, "test verification", func(ctx context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		called++
		result := runner.Run(ctx, []string{file})
		result.Status = verification.Passed
		return fantasy.ToolResponse{Content: "passed", Metadata: result.Metadata()}, nil
	})
	large := newScriptedModel(scriptedTurn{text: "done"})
	sa := testSessionAgent(env, large, textModel("title"), "system", verifier).(*sessionAgent)
	sa.cfg = config.NewTestStoreWithWorkingDir(&config.Config{Options: &config.Options{}, Verification: cfg}, env.workingDir)
	sa.files = env.history
	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "finish"})
	require.NoError(t, err)
	require.Equal(t, 1, called)
	require.Len(t, large.sentCalls(), 1)
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.NotNil(t, latestVerification(msgs))
}

func TestCompletionVerificationRepairIsBounded(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	file := filepath.Join(env.workingDir, "main.go")
	require.NoError(t, os.WriteFile(file, []byte("package main\n"), 0o600))
	sess, err := env.sessions.Create(t.Context(), "verification")
	require.NoError(t, err)
	createMessage(t, env, sess.ID, message.User, message.TextContent{Text: "existing task"})
	_, err = env.history.Create(t.Context(), sess.ID, file, "")
	require.NoError(t, err)
	cfg := verification.VerificationConfig{RequireOnCompletion: true, MaxRepairAttempts: 1, Rules: []verification.Rule{{Name: "check", Paths: []string{"**/*.go"}, Command: []string{"unused"}}}}
	runner, err := verification.New(env.workingDir, cfg)
	require.NoError(t, err)
	verifier := fantasy.NewAgentTool(VerificationToolName, "test verification", func(ctx context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		result := runner.Run(ctx, []string{file})
		result.Status = verification.Failed
		return fantasy.ToolResponse{Content: "failed", Metadata: result.Metadata(), IsError: true}, nil
	})
	large := newScriptedModel(scriptedTurn{text: "done"}, scriptedTurn{text: "still done"})
	sa := testSessionAgent(env, large, textModel("title"), "system", verifier).(*sessionAgent)
	sa.cfg = config.NewTestStoreWithWorkingDir(&config.Config{Options: &config.Options{}, Verification: cfg}, env.workingDir)
	sa.files = env.history
	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "finish"})
	require.ErrorContains(t, err, "completion blocked")
	require.Len(t, large.sentCalls(), 2)
}

func TestCompletionVerificationUsesHookBoundary(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "blocked verification")
	require.NoError(t, err)
	inner := &fakeTool{name: VerificationToolName, resp: fantasy.NewTextResponse("must not run")}
	registry := newTestRegistry(t, map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: `printf '%s' '{"decision":"deny","reason":"verification denied"}'`}},
	})
	sa := testSessionAgent(env, textModel("done"), textModel("title"), "system", newHookedTool(inner, registry, nil)).(*sessionAgent)
	err = sa.runCompletionVerification(t.Context(), sess.ID)
	require.ErrorContains(t, err, "verification blocked")
	require.False(t, inner.called)
}

func TestVerificationToolRespectsAllowlist(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	coord := newTestCoordinator(t, env, "p", config.ProviderConfig{ID: "p"})
	coord.cfg.Config().Verification = verification.VerificationConfig{Rules: []verification.Rule{{Name: "check", Paths: []string{"**/*.go"}, Command: []string{"unused"}}}}
	for _, allowed := range [][]string{{VerificationToolName}, {"view"}} {
		built, err := coord.buildTools(t.Context(), config.Agent{ID: config.AgentCoder, AllowedTools: allowed}, false)
		require.NoError(t, err)
		found := false
		for _, tool := range built {
			found = found || tool.Info().Name == VerificationToolName
		}
		require.Equal(t, allowed[0] == VerificationToolName, found)
	}
}

func TestLatestVerificationIgnoresProseClaims(t *testing.T) {
	t.Parallel()
	require.Nil(t, latestVerification([]message.Message{{Parts: []message.ContentPart{message.ToolResult{Name: "shell", Content: "all checks passed"}}}}))
}
