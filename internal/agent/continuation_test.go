package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/message"
)

// An answer cut off at max_tokens is resumed in the same assistant
// message: the model is nudged once, invisibly, and the transcript ends
// up with one user message and one finished answer.
func TestTruncatedAnswerIsContinuedInPlace(t *testing.T) {
	agent, env, large := scriptedAgent(t, nil,
		scriptedTurn{text: "The answer begins here and", finish: fantasy.FinishReasonLength},
		scriptedTurn{text: " ends here."},
	)

	msgs := runScript(t, agent, env, "explain")

	var users, assistants []message.Message
	for _, m := range msgs {
		switch m.Role {
		case message.User:
			users = append(users, m)
		case message.Assistant:
			assistants = append(assistants, m)
		}
	}
	require.Len(t, users, 1, "the continuation nudge is never stored")
	require.Len(t, assistants, 1, "the continuation lands in the same assistant message")
	assert.Equal(t, "The answer begins here and ends here.", assistants[0].Content().Text)
	assert.Equal(t, message.FinishReasonEndTurn, assistants[0].FinishReason(), "the max_tokens finish is replaced by the real one")

	calls := large.sentCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, continuePrompt, lastUserText(t, calls[1]), "the second call carries the nudge")
	// The partial answer went back to the model so it knows where it was.
	var sawPartial bool
	for _, msg := range calls[1].Prompt {
		if msg.Role == fantasy.MessageRoleAssistant {
			for _, part := range msg.Content {
				if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && text.Text == "The answer begins here and" {
					sawPartial = true
				}
			}
		}
	}
	assert.True(t, sawPartial, "the cut-off text is in the continuation request")
}

// A model that keeps running out of tokens is resumed a bounded number of
// times, then left with the max_tokens finish it earned.
func TestTruncatedAnswerContinuationIsBounded(t *testing.T) {
	turns := make([]scriptedTurn, 0, maxAnswerContinuations+3)
	for range maxAnswerContinuations + 3 {
		turns = append(turns, scriptedTurn{text: "more ", finish: fantasy.FinishReasonLength})
	}
	agent, env, large := scriptedAgent(t, nil, turns...)

	msgs := runScript(t, agent, env, "go on forever")

	require.Len(t, large.sentCalls(), 1+maxAnswerContinuations, "one answer plus the allowed continuations")
	var assistants []message.Message
	for _, m := range msgs {
		if m.Role == message.Assistant {
			assistants = append(assistants, m)
		}
	}
	require.Len(t, assistants, 1)
	assert.Equal(t, message.FinishReasonMaxTokens, assistants[0].FinishReason())
}

// A length finish on a tool call is not an answer to resume: the
// arguments were cut short and fantasy never dispatches them. The turn
// ends as it did before.
func TestTruncatedToolCallIsNotContinued(t *testing.T) {
	agent, env, large := scriptedAgent(t, nil,
		scriptedTurn{calls: []scriptedCall{{name: "view", input: map[string]any{"file_path": "main.go"}}}, finish: fantasy.FinishReasonLength},
	)

	_ = runScript(t, agent, env, "read it")

	require.Len(t, large.sentCalls(), 1, "no continuation for a cut-off tool call")
}
