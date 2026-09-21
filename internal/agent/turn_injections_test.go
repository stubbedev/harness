package agent

import (
	"runtime"
	"strconv"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/message"
)

func textsOf(msgs []fantasy.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, messageText(msg))
	}
	return out
}

// A message appended in one step goes back in the same place on every
// later step, ahead of what those steps produced, so the request only
// ever grows at its end.
func TestTurnInjectionsReplayAtTheIndexTheyWereAppendedAt(t *testing.T) {
	t.Parallel()
	var inj turnInjections
	step1 := []fantasy.Message{fantasy.NewSystemMessage("sys"), fantasy.NewUserMessage("task")}
	require.Equal(t, step1, inj.apply(step1), "nothing recorded returns the input")

	inj.add(len(step1), fantasy.NewUserMessage("note-1"))
	step2 := append(append([]fantasy.Message{}, step1...), assistantText("call"), fantasy.NewUserMessage("result"))
	require.Equal(t, []string{"sys", "task", "note-1", "call", "result"}, textsOf(inj.apply(step2)))

	inj.add(len(step2), fantasy.NewUserMessage("note-2"), fantasy.NewUserMessage("note-3"))
	step3 := append(append([]fantasy.Message{}, step2...), assistantText("call-2"))
	require.Equal(t, []string{"sys", "task", "note-1", "call", "result", "note-2", "note-3", "call-2"}, textsOf(inj.apply(step3)))
	require.Len(t, step3, 5, "the input is not modified")
}

func TestLastTaggedUserText(t *testing.T) {
	t.Parallel()
	msgs := []fantasy.Message{
		fantasy.NewUserMessage("<execution_state>\nold\n</execution_state>"),
		assistantText("ok"),
		fantasy.NewUserMessage("prompt\n\n<execution_state>\nmerged\n</execution_state>"),
		fantasy.NewUserMessage("unrelated"),
	}
	require.Equal(t, "<execution_state>\nmerged\n</execution_state>", lastTaggedUserText(msgs, "<execution_state>"))
	require.Empty(t, lastTaggedUserText(msgs, "<harness_runtime>"))
	require.True(t, userTextContains(msgs, "unrelated"))
	require.False(t, userTextContains([]fantasy.Message{assistantText("unrelated")}, "unrelated"))
}

func assistantText(text string) fantasy.Message {
	return fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: text}}}
}

// promptShape reduces a request to what a prefix comparison needs: the
// role and text of every message, with tool results counted by part.
func promptShape(msgs []fantasy.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, string(msg.Role)+":"+messageText(msg)+":"+strconv.Itoa(len(msg.Content)))
	}
	return out
}

func countRuntimeBlocks(msgs []fantasy.Message) int {
	n := 0
	for _, msg := range msgs {
		n += strings.Count(messageText(msg), "<harness_runtime>")
	}
	return n
}

// Across the steps of a turn, and into the next turn, every request the
// agent sends starts with the previous one: harness context is written
// once, at the place it first appeared, never re-appended behind the
// step's new tool results. This is what keeps the provider's prompt
// cache warm.
func TestRequestsGrowOnlyAtTheEnd(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows for now")
	}

	env := testEnv(t)
	createSimpleGoProject(t, env.workingDir)
	large := newScriptedModel(
		scriptedTurn{calls: []scriptedCall{{name: tools.ViewToolName, input: map[string]any{"file_path": "go.mod"}}}},
		scriptedTurn{text: "read it"},
		scriptedTurn{text: "still here"},
	)
	agent, err := coderAgent(nil, env, large, textModel("A Session"))
	require.NoError(t, err)
	sess, err := env.sessions.Create(t.Context(), "prefix")
	require.NoError(t, err)

	for _, prompt := range []string{"read the go mod", "anything else?"} {
		res, runErr := agent.Run(t.Context(), SessionAgentCall{Prompt: prompt, SessionID: sess.ID, MaxOutputTokens: 10000})
		require.NoError(t, runErr)
		require.NotNil(t, res)
	}

	sent := large.sentCalls()
	require.Len(t, sent, 3, "two steps in the first turn, one in the second")
	for i := 1; i < len(sent); i++ {
		prev, next := promptShape(sent[i-1].Prompt), promptShape(sent[i].Prompt)
		require.Greater(t, len(next), len(prev))
		require.Equal(t, prev, next[:len(prev)], "request %d does not start with request %d", i+1, i)
	}
	for i, call := range sent {
		require.Equal(t, 1, countRuntimeBlocks(call.Prompt), "request %d carries the runtime block once", i+1)
	}

	stored, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var notes []message.ContextNote
	for _, m := range stored {
		notes = append(notes, m.ContextNotes()...)
	}
	require.Len(t, notes, 1, "the runtime block is stored once, as a context note")
	require.Equal(t, message.ContextNoteRuntime, notes[0].Kind)
	require.Contains(t, notes[0].Text, "<harness_runtime>")
}
