package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

// judgeModel is the small model for goal tests. Titles stream and goal
// verdicts are generated, so it answers each by the call it gets: the
// title goroutine races the first judgement, and one script shared by
// both would hand either the other's answer.
type judgeModel struct {
	title *scriptedModel

	mu       sync.Mutex
	verdicts []string
	prompts  []string
}

func newJudgeModel(verdicts ...string) *judgeModel {
	return &judgeModel{title: textModel("A Session"), verdicts: verdicts}
}

func (m *judgeModel) Provider() string { return "scripted" }
func (m *judgeModel) Model() string    { return "scripted-judge" }

func (m *judgeModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	return m.title.Stream(ctx, call)
}

func (m *judgeModel) Generate(_ context.Context, call fantasy.Call) (*fantasy.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var prompt string
	if n := len(call.Prompt); n > 0 {
		for _, part := range call.Prompt[n-1].Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				prompt += text.Text
			}
		}
	}
	m.prompts = append(m.prompts, prompt)
	verdict := `{"verdict": "met", "reason": "out of script"}`
	if len(m.verdicts) > 0 {
		verdict, m.verdicts = m.verdicts[0], m.verdicts[1:]
	}
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: verdict}},
		FinishReason: fantasy.FinishReasonStop,
	}, nil
}

func (m *judgeModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, nil
}

func (m *judgeModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}

func (m *judgeModel) judged() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

// runGoal sets condition as a fresh session's goal, runs prompt, and
// returns the conversation and the goal it left.
func runGoal(t *testing.T, judge *judgeModel, condition string, turns ...scriptedTurn) ([]message.Message, *session.Goal) {
	t.Helper()

	env := testEnv(t)
	createSimpleGoProject(t, env.workingDir)
	agent, err := coderAgent(nil, env, newScriptedModel(turns...), judge)
	require.NoError(t, err)

	sess, err := env.sessions.Create(t.Context(), "New Session")
	require.NoError(t, err)
	if condition != "" {
		goal, err := session.NewGoal(condition, time.Now())
		require.NoError(t, err)
		require.NoError(t, env.sessions.SetGoal(t.Context(), sess.ID, goal))
	}
	_, err = agent.Run(t.Context(), SessionAgentCall{
		Prompt:          GoalPrompt(condition),
		SessionID:       sess.ID,
		MaxOutputTokens: 10000,
	})
	require.NoError(t, err)

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var convo []message.Message
	for _, m := range msgs {
		if !m.ContextNotesOnly() {
			convo = append(convo, m)
		}
	}
	after, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	return convo, after.Goal
}

func TestSessionGoalLoop(t *testing.T) {
	t.Parallel()

	t.Run("continues until the judge finds it met", func(t *testing.T) {
		t.Parallel()

		judge := newJudgeModel(
			`{"verdict": "not_met", "reason": "The README still lacks an install section."}`,
			"```json\n{\"verdict\": \"met\", \"reason\": \"Both sections are written.\"}\n```",
		)
		msgs, goal := runGoal(t, judge, "README has usage and install sections",
			scriptedTurn{text: "Wrote the usage section."},
			scriptedTurn{text: "Wrote the install section."},
		)

		require.Len(t, msgs, 4)
		cont := msgs[2]
		require.Equal(t, message.User, cont.Role)
		// The continuation restates the goal, which after a compaction
		// may be the only place the model still sees it.
		assert.Contains(t, cont.Content().Text, "README has usage and install sections")
		assert.Contains(t, cont.Content().Text, "lacks an install section")

		require.NotNil(t, goal)
		assert.Equal(t, session.GoalMet, goal.Status)
		assert.Equal(t, 2, goal.Turns)
		assert.Equal(t, "Both sections are written.", goal.Reason)

		judged := judge.judged()
		require.Len(t, judged, 2)
		assert.Contains(t, judged[0], "Wrote the usage section.")
		assert.Contains(t, judged[1], "lacks an install section", "the judge sees its previous verdict")
	})

	t.Run("an impossible goal ends the loop", func(t *testing.T) {
		t.Parallel()

		judge := newJudgeModel(`{"verdict": "impossible", "reason": "There is no such service."}`)
		msgs, goal := runGoal(t, judge, "Deploy to the staging service",
			scriptedTurn{text: "There is no staging service in this repo."},
		)
		require.Len(t, msgs, 2)
		assert.Equal(t, session.GoalImpossible, goal.Status)
	})

	t.Run("a blocked goal pauses and stays active", func(t *testing.T) {
		t.Parallel()

		judge := newJudgeModel(`{"verdict": "blocked", "reason": "Needs the API key."}`)
		msgs, goal := runGoal(t, judge, "Call the API",
			scriptedTurn{text: "Which API key should I use?"},
		)
		require.Len(t, msgs, 2)
		assert.True(t, goal.Active())
		assert.Equal(t, "Needs the API key.", goal.Reason)
	})

	t.Run("continuations that use no tools pause the loop", func(t *testing.T) {
		t.Parallel()

		notMet := `{"verdict": "not_met", "reason": "Nothing was done."}`
		judge := newJudgeModel(notMet, notMet, notMet, notMet, notMet)
		turns := make([]scriptedTurn, maxGoalStalledTurns+1)
		for i := range turns {
			turns[i] = scriptedTurn{text: "Thinking about it."}
		}
		msgs, goal := runGoal(t, judge, "Ship the feature", turns...)

		// The prompt turn and maxGoalStalledTurns continuations; the last
		// one is not judged.
		require.Len(t, msgs, 2*(maxGoalStalledTurns+1))
		assert.Len(t, judge.judged(), maxGoalStalledTurns)
		assert.True(t, goal.Active(), "a paused goal stays set")
		assert.True(t, strings.HasPrefix(goal.Reason, "Paused:"), goal.Reason)
	})

	t.Run("no goal, no judge", func(t *testing.T) {
		t.Parallel()

		judge := newJudgeModel()
		msgs, goal := runGoal(t, judge, "", scriptedTurn{text: "Hi."})
		require.Len(t, msgs, 2)
		assert.Nil(t, goal)
		assert.Empty(t, judge.judged())
	})
}

func TestParseGoalVerdict(t *testing.T) {
	t.Parallel()

	v, err := parseGoalVerdict(`Sure. {"verdict": "not_met", "reason": " tests fail "} done`)
	require.NoError(t, err)
	assert.Equal(t, goalVerdictNotMet, v.Verdict)
	assert.Equal(t, "tests fail", v.Reason)

	for _, bad := range []string{"", "met", `{"verdict": "maybe"}`, `{"verdict": }`} {
		_, err := parseGoalVerdict(bad)
		assert.Error(t, err, bad)
	}
}

func TestGoalJudgeInputIsBounded(t *testing.T) {
	t.Parallel()

	goal := &session.Goal{Condition: "done", Turns: 3, CreatedAt: 0}
	long := strings.Repeat("x", 3*goalFinalMessageWindow) + "THE END"
	in := goalJudgeInput(goal, long, []string{"bash x2", "edit"}, time.Unix(0, 0))
	assert.Contains(t, in, "THE END", "the tail of the final message is kept")
	assert.Less(t, len(in), goalFinalMessageWindow+1000)
	assert.Contains(t, in, "bash x2, edit")
}
