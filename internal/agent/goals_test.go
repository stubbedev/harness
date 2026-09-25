package agent

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

// goalsAgent is a scriptedAgent with inherent goals enabled (the tests
// drive the coder agent with the config default off, since config.Init
// leaves the option unset and the resolved default is applied by the
// coordinator, not by NewSessionAgent).
func goalsAgent(t *testing.T, client *http.Client, turns ...scriptedTurn) (SessionAgent, fakeEnv, *scriptedModel) {
	t.Helper()

	agent, env, model := scriptedAgent(t, client, turns...)
	if sa, ok := agent.(*sessionAgent); ok {
		sa.inherentGoals = true
	}
	return agent, env, model
}

func TestDeclaresNextSteps(t *testing.T) {
	t.Parallel()

	declarations := []string{
		"Fixed the parser bug. Next I will run the full test suite.",
		"All checks pass. I'll now update the changelog.",
		"Done with the refactor. I'm going to extract the shared helper next.",
		"The upgrade is in. My next steps are the migration and a smoke test.",
		"I plan to benchmark the result before reporting back.",
		"Summary of changes above. Then I'll wire up the new config.",
		"First pass complete.\n\nNext steps:\n- add regression tests\n- update docs",
		"**Next steps:** re-run the benchmarks",
	}
	for _, text := range declarations {
		assert.True(t, declaresNextSteps(text), "expected a declaration: %q", text)
	}

	nonDeclarations := []string{
		"All done. Nothing left to do.",
		"Fixed. Let me know if you need anything else.",
		"I won't touch the vendored dependencies.",
		"Shall I proceed with the refactor?",
		"I can add tests if you want them.",
		"I would recommend pinning the dependency versions.",
		"The CI will fail without the generated files.",
		"I did not run the benchmarks; the machine is too loaded.",
		"",
	}
	for _, text := range nonDeclarations {
		assert.False(t, declaresNextSteps(text), "expected no declaration: %q", text)
	}

	// Only the tail is scanned: a plan mentioned mid-report does not
	// trigger, one at the end does.
	long := "I will check the headers. " + strings.Repeat("Progress notes. ", 60) + "All green."
	assert.False(t, declaresNextSteps(long), "a mid-report plan is outside the scan window")
	tail := strings.Repeat("Progress notes. ", 60) + "I will check the headers next."
	assert.True(t, declaresNextSteps(tail), "a closing plan is inside the scan window")
}

func TestGoalContinuationEndsOfTurn(t *testing.T) {
	t.Parallel()

	t.Run("a declaring turn is continued", func(t *testing.T) {
		t.Parallel()

		agent, env, _ := goalsAgent(t, nil,
			scriptedTurn{text: "Fixed the parser. Next I will run the test suite."},
			scriptedTurn{text: "Tests pass. Done."},
		)
		msgs := runScript(t, agent, env, "Fix the parser")

		require.Len(t, msgs, 4)
		require.Equal(t, message.User, msgs[0].Role)
		require.Equal(t, message.Assistant, msgs[1].Role)
		require.Contains(t, msgs[1].Content().Text, "Next I will run")
		// The continuation is a real turn: its prompt is persisted as
		// the user message that starts it.
		require.Equal(t, message.User, msgs[2].Role)
		require.Equal(t, goalContinuationPrompt, msgs[2].Content().Text)
		require.Equal(t, message.Assistant, msgs[3].Role)
		require.Contains(t, msgs[3].Content().Text, "Tests pass")
	})

	t.Run("a plain answer is not continued", func(t *testing.T) {
		t.Parallel()

		agent, env, _ := goalsAgent(t, nil, scriptedTurn{text: "All done."})
		msgs := runScript(t, agent, env, "Status?")

		require.Len(t, msgs, 2, "a turn that declares nothing ends the chain")
	})

	t.Run("the chain is capped", func(t *testing.T) {
		t.Parallel()

		turns := make([]scriptedTurn, 0, maxGoalContinuations+1)
		for range maxGoalContinuations + 1 {
			turns = append(turns, scriptedTurn{text: "Progress. Next I will keep going."})
		}
		agent, env, _ := goalsAgent(t, nil, turns...)
		msgs := runScript(t, agent, env, "Long task")

		// One user prompt, cap+1 declaring assistant turns, and a
		// continuation prompt before every one of them but the last.
		require.Len(t, msgs, 2*maxGoalContinuations+2)
		continuations := 0
		for _, msg := range msgs {
			if msg.Role == message.User && msg.Content().Text == goalContinuationPrompt {
				continuations++
			}
		}
		assert.Equal(t, maxGoalContinuations, continuations, "the chain stops at the cap")
	})
}
