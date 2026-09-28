package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

// Session goals: the user sets a completion condition and harness keeps
// the agent working until a judge finds it met. The goal lives on the
// session row, never in the conversation, so a compaction, a restart or
// a resumed session cannot drop it, and every continuation restates it
// for a model whose history may have been folded into a summary.
//
// The judge runs once per turn, when the model has stopped calling tools
// and handed back a final answer - never per step. It reads the goal,
// the final message and the names of the tools the turn used, not the
// transcript, so a judgement costs one small-model call of a few hundred
// tokens. A turn that ends any other way - an error, a cancel, a
// compaction requeue, a user prompt already waiting - is not judged: the
// turn that follows it is.

//go:embed templates/goal_judge.md
var goalJudgePrompt []byte

const (
	// goalFinalMessageWindow is how much of the turn's final message
	// the judge reads, from its end, where the report of what was done
	// sits.
	goalFinalMessageWindow = 6000
	// maxGoalStalledTurns pauses the loop when this many continuations
	// in a row used no tools: the agent is talking, not working, and
	// another nudge will not change that. The goal stays set, and the
	// next user prompt resumes it.
	maxGoalStalledTurns = 3
	// goalJudgeMaxOutput bounds a non-reasoning judge's answer, which is
	// one short JSON object.
	goalJudgeMaxOutput = 400
)

// goalVerdictKind is the judge's answer.
type goalVerdictKind string

const (
	goalVerdictMet        goalVerdictKind = "met"
	goalVerdictNotMet     goalVerdictKind = "not_met"
	goalVerdictBlocked    goalVerdictKind = "blocked"
	goalVerdictImpossible goalVerdictKind = "impossible"
)

type goalVerdict struct {
	Verdict goalVerdictKind `json:"verdict"`
	Reason  string          `json:"reason"`
}

// GoalPrompt is the prompt the turn that sets a goal runs with.
func GoalPrompt(condition string) string {
	return "Work toward this goal until it is fully met. Keep going without " +
		"waiting for confirmation; after each turn a judge checks whether " +
		"the goal is met, and you are asked to continue while it is not.\n\n" +
		"<goal>\n" + condition + "\n</goal>"
}

// goalContinuePrompt is the prompt a continuation turn runs with. It
// restates the goal in full: after a compaction the goal may survive
// only here.
func goalContinuePrompt(goal *session.Goal) string {
	var sb strings.Builder
	sb.WriteString("The goal is not met yet.")
	if goal.Reason != "" {
		sb.WriteString(" ")
		sb.WriteString(goal.Reason)
	}
	sb.WriteString("\n\nKeep working toward the goal:\n\n<goal>\n")
	sb.WriteString(goal.Condition)
	sb.WriteString("\n</goal>")
	return sb.String()
}

// advanceGoal judges a finished turn against the session's goal. It
// returns the continuation turn to queue while the goal is not met, and
// nil when the loop stops: no active goal, a turn that is not judged,
// or a verdict that ends or pauses it.
func (a *sessionAgent) advanceGoal(ctx context.Context, call SessionAgentCall, assistant *message.Message, result *fantasy.AgentResult) *SessionAgentCall {
	if a.isSubAgent {
		return nil
	}
	if assistant == nil || len(assistant.ToolCalls()) > 0 {
		return nil
	}
	if result == nil || result.Response.FinishReason != fantasy.FinishReasonStop {
		return nil
	}
	if a.QueuedPrompts(call.SessionID) > 0 {
		return nil
	}
	sess, err := a.sessions.Get(ctx, call.SessionID)
	if err != nil {
		slog.Warn("Failed to load session for goal", "session_id", call.SessionID, "error", err)
		return nil
	}
	goal := sess.Goal
	if !goal.Active() {
		return nil
	}

	used := turnToolNames(result)
	stalled := 0
	if call.GoalContinuation && len(used) == 0 {
		stalled = call.GoalStalledTurns + 1
	}
	if stalled >= maxGoalStalledTurns {
		slog.Info("Goal loop paused; continuations stopped using tools",
			"session_id", call.SessionID, "turns", stalled)
		goal.Reason = fmt.Sprintf("Paused: %d turns in a row made no progress. Send a prompt to resume.", stalled)
		goal.UpdatedAt = time.Now().Unix()
		a.saveGoal(ctx, call.SessionID, goal)
		return nil
	}

	verdict, err := a.judgeGoal(ctx, call.SessionID, goal, assistant.Content().Text, used)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("Goal judge failed; pausing the goal loop", "session_id", call.SessionID, "error", err)
		}
		return nil
	}
	goal.Turns++
	goal.Reason = verdict.Reason
	goal.UpdatedAt = time.Now().Unix()
	slog.Info("Goal judged", "session_id", call.SessionID, "verdict", verdict.Verdict, "turns", goal.Turns)
	switch verdict.Verdict {
	case goalVerdictMet:
		goal.Status = session.GoalMet
	case goalVerdictImpossible:
		goal.Status = session.GoalImpossible
	}
	a.saveGoal(ctx, call.SessionID, goal)
	if verdict.Verdict != goalVerdictNotMet {
		return nil
	}

	next := call
	next.Prompt = goalContinuePrompt(goal)
	next.Attachments = nil
	next.GoalContinuation = true
	next.GoalStalledTurns = stalled
	next.GoalContinuations = 0
	next.OverflowRecovered = false
	return &next
}

func (a *sessionAgent) saveGoal(ctx context.Context, sessionID string, goal *session.Goal) {
	if err := a.sessions.SetGoal(ctx, sessionID, goal); err != nil {
		slog.Warn("Failed to save session goal", "session_id", sessionID, "error", err)
	}
}

// judgeGoal asks the small model for a verdict on the goal. The large
// model stands in when no small one is configured.
func (a *sessionAgent) judgeGoal(ctx context.Context, sessionID string, goal *session.Goal, finalText string, used []string) (goalVerdict, error) {
	model := a.smallModel.Get()
	if model.Model == nil {
		model = a.largeModel.Get()
	}
	if model.Model == nil {
		return goalVerdict{}, errors.New("no model to judge the goal")
	}
	maxOutput := int64(goalJudgeMaxOutput)
	if model.CatalogCfg.CanReason && model.CatalogCfg.DefaultMaxTokens > 0 {
		maxOutput = model.CatalogCfg.DefaultMaxTokens
	}
	var opts fantasy.ProviderOptions
	if a.cfg != nil {
		if providerCfg, ok := a.cfg.Config().Providers.Get(model.ModelCfg.Provider); ok {
			opts = withPromptCacheKey(sessionID, auxiliaryProviderOptions(model, providerCfg))
		}
	}
	systemPromptPrefix := a.systemPromptPrefix.Get()
	judge := fantasy.NewAgent(
		model.Model,
		fantasy.WithSystemPrompt(string(goalJudgePrompt)),
		fantasy.WithMaxOutputTokens(maxOutput),
		a.retryOption(),
		fantasy.WithUserAgent(userAgent),
	)
	resp, err := judge.Generate(ctx, fantasy.AgentCall{
		Prompt:          goalJudgeInput(goal, finalText, used, time.Now()),
		Headers:         sessionHeaders(sessionID),
		ProviderOptions: opts,
		PrepareStep: func(callCtx context.Context, options fantasy.PrepareStepFunctionOptions) (context.Context, fantasy.PrepareStepResult, error) {
			prepared := fantasy.PrepareStepResult{Messages: options.Messages}
			if systemPromptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{fantasy.NewSystemMessage(systemPromptPrefix)}, prepared.Messages...)
			}
			return callCtx, prepared, nil
		},
	})
	if err != nil {
		return goalVerdict{}, fmt.Errorf("judge goal: %w", err)
	}
	var cost *float64
	for _, step := range resp.Steps {
		if stepCost := a.openrouterCost(step.ProviderMetadata); stepCost != nil {
			total := *stepCost
			if cost != nil {
				total += *cost
			}
			cost = &total
		}
	}
	// Only the cost is charged: the token counters describe the coding
	// request the session stands at, which the judge did not send.
	if charged := sessionUsage(model, resp.TotalUsage, cost, false); charged.CostDelta > 0 {
		if err := a.sessions.AddCost(ctx, sessionID, charged.CostDelta); err != nil {
			slog.Warn("Failed to charge the goal judge", "session_id", sessionID, "error", err)
		}
	}
	return parseGoalVerdict(resp.Response.Content.Text())
}

// goalJudgeInput is the judge's prompt: the goal and the evidence the
// latest turn left.
func goalJudgeInput(goal *session.Goal, finalText string, used []string, now time.Time) string {
	var sb strings.Builder
	sb.WriteString("<goal>\n")
	sb.WriteString(goal.Condition)
	sb.WriteString("\n</goal>\n\n<progress>\n")
	fmt.Fprintf(&sb, "Turns judged so far: %d.", goal.Turns)
	if goal.CreatedAt > 0 {
		elapsed := now.Sub(time.Unix(goal.CreatedAt, 0)).Round(time.Minute)
		fmt.Fprintf(&sb, " Time since the goal was set: %s.", elapsed)
	}
	sb.WriteString("\n</progress>\n\n")
	if goal.Reason != "" {
		sb.WriteString("<previous_verdict>\n")
		sb.WriteString(goal.Reason)
		sb.WriteString("\n</previous_verdict>\n\n")
	}
	sb.WriteString("<tools_used_this_turn>\n")
	if len(used) == 0 {
		sb.WriteString("none")
	} else {
		sb.WriteString(strings.Join(used, ", "))
	}
	sb.WriteString("\n</tools_used_this_turn>\n\n<final_message>\n")
	sb.WriteString(tailRunes(strings.TrimSpace(finalText), goalFinalMessageWindow))
	sb.WriteString("\n</final_message>")
	return sb.String()
}

// turnToolNames lists the tools a turn called, each with its count.
func turnToolNames(result *fantasy.AgentResult) []string {
	counts := map[string]int{}
	for _, step := range result.Steps {
		for _, tc := range step.Content.ToolCalls() {
			counts[tc.ToolName]++
		}
	}
	names := slices.Sorted(maps.Keys(counts))
	for i, name := range names {
		if n := counts[name]; n > 1 {
			names[i] = fmt.Sprintf("%s x%d", name, n)
		}
	}
	return names
}

// parseGoalVerdict reads the judge's JSON answer, tolerating prose or a
// code fence around it.
func parseGoalVerdict(text string) (goalVerdict, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return goalVerdict{}, fmt.Errorf("judge goal: no verdict in %q", text)
	}
	var v goalVerdict
	if err := json.Unmarshal([]byte(text[start:end+1]), &v); err != nil {
		return goalVerdict{}, fmt.Errorf("judge goal: %w", err)
	}
	v.Reason = strings.TrimSpace(v.Reason)
	switch v.Verdict {
	case goalVerdictMet, goalVerdictNotMet, goalVerdictBlocked, goalVerdictImpossible:
		return v, nil
	}
	return goalVerdict{}, fmt.Errorf("judge goal: unknown verdict %q", v.Verdict)
}

// tailRunes returns the last n runes of s.
func tailRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[len(runes)-n:])
}
