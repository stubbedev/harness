package agent

import (
	"log/slog"
	"regexp"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/message"
)

// Inherent goals: a turn that ends by declaring what it will do next is
// continued by harness instead of ending and waiting for the user to
// type "continue". The detection is mechanical — first-person
// future-intent statements in the tail of the final assistant message —
// and the continuation is a queued turn harness starts on its own, so
// every guard the queue path already has (cancel marks, user prompts
// taking precedence, terminal events) applies unchanged. Its prompt is
// a ContextNote: the model reads it, the transcript does not show it.

// nextStepsPrompt is the prompt the continuation turn runs with. The
// model has its own last message in context, so a short nudge suffices;
// it says where it comes from so the model acts on it instead of
// answering it.
const nextStepsPrompt = "<system_reminder>\nYour last message ended by stating what you would do next, " +
	"and the turn stopped there. Do it now without restating the plan. " +
	"This reminder is automatic, not from the user; do not mention it.\n</system_reminder>"

// maxNextStepsContinuations bounds the chain: consecutive continuations
// since the last user prompt. It keeps a model that signs every message
// off with "next I will..." from looping forever.
const maxNextStepsContinuations = 3

// goalScanWindow is how much of the final assistant message the
// detector reads. Declarations of future work close a wrap-up, so
// reading only the tail keeps plan mentions in the middle of a report
// from triggering a continuation.
const goalScanWindow = 500

var (
	// goalIntentRe matches first-person future-intent openers.
	goalIntentRe = regexp.MustCompile(`(?i)\b(?:i'?ll|i will|i'?m going to|i am going to|i plan to|i intend to|i'?m about to|i am about to|next,? i(?:'ll| will|'?m going to| am going to)|then i(?:'ll| will)|my next steps? (?:are|is|will be))\b`)

	// goalNextStepsRe matches a "Next steps:" style heading line.
	goalNextStepsRe = regexp.MustCompile(`(?i)^\W*(?:\*\*)?\s*next steps?\s*(?:\*\*)?\s*:?.*$`)

	// goalExclusionRe marks sentences that only look like intent:
	// negations, offers and questions to the user, advice, and
	// contrastive framing.
	goalExclusionRe = regexp.MustCompile(`(?i)\b(?:will not|won't|wouldn't|shan't|don't|do not|cannot|can't|didn't|did not|let me know|instead of|rather than|if you|should i|shall i|do you want|want me to|would you like|i'?d recommend|i would recommend|i recommend|you should|you may want|leave it to|leave the rest|leave that to|unless)\b`)
)

// goalSentenceRe splits a line into sentence-sized pieces, keeping the
// terminator so a question can be recognized by its "?".
var goalSentenceRe = regexp.MustCompile(`[^.!?\n]+[.!?]?`)

// declaresNextSteps reports whether the final assistant text declares
// work it intends to do next. Only the tail of the message is read;
// questions, negations, and offers aimed at the user do not count.
func declaresNextSteps(text string) bool {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return false
	}
	if len(runes) > goalScanWindow {
		runes = runes[len(runes)-goalScanWindow:]
	}
	for line := range strings.SplitSeq(string(runes), "\n") {
		trimmed := strings.TrimSpace(line)
		if goalNextStepsRe.MatchString(trimmed) {
			return true
		}
		for _, sentence := range goalSentenceRe.FindAllString(line, -1) {
			sentence = strings.TrimSpace(sentence)
			if sentence == "" || strings.HasSuffix(sentence, "?") {
				continue
			}
			if goalExclusionRe.MatchString(sentence) {
				continue
			}
			if goalIntentRe.MatchString(sentence) {
				return true
			}
		}
	}
	return false
}

// nextStepsContinuation returns the turn that continues a finished turn
// which ended by declaring next steps, or nil when it should end. Only
// interactive turns qualify: subagent and non-interactive runs end when
// their work ends. A turn the user already has follow-up input queued
// for continues through that input instead, and the chain is capped so
// a model that ends every message with a promise cannot run away.
func (a *sessionAgent) nextStepsContinuation(call SessionAgentCall, assistant *message.Message, result *fantasy.AgentResult) *SessionAgentCall {
	if !a.inherentGoals || call.NonInteractive {
		return nil
	}
	if call.nextStepsContinuations >= maxNextStepsContinuations {
		return nil
	}
	if a.QueuedPrompts(call.SessionID) > 0 {
		return nil
	}
	if assistant == nil || len(assistant.ToolCalls()) > 0 {
		return nil
	}
	if result == nil || result.Response.FinishReason != fantasy.FinishReasonStop {
		return nil
	}
	if !declaresNextSteps(assistant.Content().Text) {
		return nil
	}
	next := call.harnessTurn(message.ContextNoteNextSteps, nextStepsPrompt)
	next.nextStepsContinuations++
	slog.Info("Turn ended by declaring next steps; continuing",
		"session_id", call.SessionID, "continuation", next.nextStepsContinuations)
	return &next
}
