package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/verification"
)

// requestUserTexts returns the text of every user message in a model request.
func requestUserTexts(call fantasy.Call) []string {
	var texts []string
	for _, msg := range call.Prompt {
		if msg.Role == fantasy.MessageRoleUser {
			texts = append(texts, messageText(msg))
		}
	}
	return texts
}

// TestOverflowAfterWorkResumesTheTypedPrompt pins the recovery from a
// provider rejecting a turn that had already called tools: the prompt
// is shown once, exactly as typed, and the model is told by a hidden
// note to pick the work up from the summary.
func TestOverflowAfterWorkResumesTheTypedPrompt(t *testing.T) {
	t.Parallel()

	tool := fantasy.NewAgentTool("probe", "probe", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("probed"), nil
	})
	env := testEnv(t)
	large := newScriptedModel(
		scriptedTurn{calls: []scriptedCall{{name: "probe", input: map[string]any{}}}},
		scriptedTurn{err: errors.New("prompt is too long: context length exceeded")},
		scriptedTurn{text: "Finished after the summary."},
	)
	agent := testSessionAgent(env, large, textModel("summary"), "system", tool)
	history := runScriptHistory(t, agent, env, "Fix the parser")

	var typed []string
	for _, msg := range conversation(history) {
		if msg.Role == message.User {
			typed = append(typed, msg.Content().Text)
		}
	}
	assert.Equal(t, []string{"Fix the parser"}, typed, "the transcript shows the prompt once, as typed")
	assert.Equal(t, []string{resumeNote}, notesOfKind(history, message.ContextNoteResume))

	calls := large.sentCalls()
	require.Len(t, calls, 3)
	resumed := requestUserTexts(calls[2])
	require.NotEmpty(t, resumed)
	assert.True(t, strings.HasSuffix(resumed[len(resumed)-1], "Fix the parser\n\n"+resumeNote),
		"the resumed request ends with the prompt followed by the note, as stored: %q", resumed[len(resumed)-1])
}

// TestOverflowBeforeWorkReplaysThePromptPlainly pins the other branch:
// a turn rejected before it did anything is simply sent again.
func TestOverflowBeforeWorkReplaysThePromptPlainly(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	large := newScriptedModel(
		scriptedTurn{err: errors.New("context_length_exceeded")},
		scriptedTurn{text: "Done."},
	)
	agent := testSessionAgent(env, large, textModel("summary"), "system")
	history := runScriptHistory(t, agent, env, "Fix the parser")

	assert.Empty(t, notesOfKind(history, message.ContextNoteResume))
	calls := large.sentCalls()
	require.Len(t, calls, 2)
	texts := requestUserTexts(calls[1])
	assert.True(t, strings.HasSuffix(texts[len(texts)-1], "Fix the parser"), "the prompt is sent again as typed: %q", texts[len(texts)-1])
	assert.NotContains(t, texts[len(texts)-1], "system_reminder")
}

// TestVerificationRepairSendsEachMessageOnce pins the repair request:
// rebuilt from the stored session, it carries what the turn injected
// (here a follow-up folded in mid-turn) once, and its own nudge is a
// stored note rather than a prompt nobody can see later.
func TestVerificationRepairSendsEachMessageOnce(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	file := filepath.Join(env.workingDir, "main.go")
	require.NoError(t, os.WriteFile(file, []byte("package main\n"), 0o600))
	sess, err := env.sessions.Create(t.Context(), "verification")
	require.NoError(t, err)
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
	var sa *sessionAgent
	const followUp = "Also check the docs."
	queuer := fantasy.NewAgentTool("queuer", "queues a follow-up", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		sa.appendQueued(sess.ID, SessionAgentCall{SessionID: sess.ID, Prompt: followUp})
		return fantasy.NewTextResponse("queued"), nil
	})
	large := newScriptedModel(
		scriptedTurn{calls: []scriptedCall{{name: "queuer", input: map[string]any{}}}},
		scriptedTurn{text: "done"},
		scriptedTurn{text: "still done"},
	)
	sa = testSessionAgent(env, large, textModel("title"), "system", verifier, queuer).(*sessionAgent)
	sa.cfg = config.NewTestStoreWithWorkingDir(&config.Config{Options: &config.Options{}, Verification: cfg}, env.workingDir)
	sa.files = env.history

	_, err = sa.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "finish"})
	require.ErrorContains(t, err, "completion blocked")

	calls := large.sentCalls()
	require.Len(t, calls, 3)
	repair := strings.Join(requestUserTexts(calls[2]), "\n")
	assert.Equal(t, 1, strings.Count(repair, followUp), "the folded follow-up is sent once")
	assert.Equal(t, 1, strings.Count(repair, verificationRepairPrompt), "the nudge is sent once")

	stored, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{verificationRepairPrompt}, notesOfKind(stored, message.ContextNoteVerification))
}
