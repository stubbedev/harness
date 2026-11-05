package agent

import (
	"strings"

	"charm.land/fantasy"
)

// An answer that stops because the model ran out of output tokens is
// not finished; it is paused. Rather than hand the user half an answer
// and make them type "continue", the run resumes it: the same request
// with the partial answer appended and a nudge to go on, written into
// the same assistant message. Only plain answers are resumed - a length
// finish on a tool call means the arguments were cut short, and those
// are never executed or repaired (see fantasy's CHARM-2020 handling).
const (
	// maxAnswerContinuations bounds how many times one turn resumes, so a
	// model that never stops cannot spend the whole window on one answer.
	maxAnswerContinuations = 3
	// continuePrompt is the nudge. It is sent, never stored: the transcript
	// shows the answer, not the plumbing that kept it going.
	continuePrompt = "Continue exactly where you stopped; do not repeat anything."
)

// answerTruncated reports whether the run's last step ended with the
// output budget exhausted in the middle of a plain text answer.
func answerTruncated(result *fantasy.AgentResult) bool {
	if result == nil || result.Response.FinishReason != fantasy.FinishReasonLength {
		return false
	}
	if len(result.Response.Content.ToolCalls()) > 0 {
		return false
	}
	return strings.TrimSpace(result.Response.Content.Text()) != ""
}

// continuationHistory is the request the first resumed step sends: the
// history the turn started from, the prompt that started it with its
// attachments (which the first call carried as its Prompt and Files),
// and everything the model has produced since - the steps' assistant
// and tool messages, the cut-off answer last. The nudge goes in as the
// new prompt.
func continuationHistory(history []fantasy.Message, prompt string, files []fantasy.FilePart, result *fantasy.AgentResult) []fantasy.Message {
	out := make([]fantasy.Message, 0, len(history)+1+len(result.Steps)*2)
	out = append(out, history...)
	if prompt != "" || len(files) > 0 {
		out = append(out, fantasy.NewUserMessage(prompt, files...))
	}
	return appendStepMessages(out, result)
}

// nextContinuationHistory is the request a later resumed step sends: the
// previous continuation's request exactly as it went out, its nudge, and
// what that continuation produced. Extending the request rather than
// rebuilding it keeps every earlier nudge in place, so the prefix the
// provider cached stays the prefix, and the positions turnInjections
// recorded during that continuation still point where they did.
func nextContinuationHistory(previous []fantasy.Message, cont *fantasy.AgentResult) []fantasy.Message {
	out := make([]fantasy.Message, 0, len(previous)+1+len(cont.Steps)*2)
	out = append(out, previous...)
	out = append(out, fantasy.NewUserMessage(continuePrompt))
	return appendStepMessages(out, cont)
}

// appendStepMessages appends the assistant and tool messages of every
// step of result to out.
func appendStepMessages(out []fantasy.Message, result *fantasy.AgentResult) []fantasy.Message {
	for _, step := range result.Steps {
		out = append(out, step.Messages...)
	}
	return out
}

// mergeContinuation folds a continuation's result into the turn's: the
// steps accumulate, usage adds up, and the response is the latest one,
// whose finish reason decides whether to continue again.
func mergeContinuation(first, cont *fantasy.AgentResult) *fantasy.AgentResult {
	merged := *first
	merged.Steps = append(append([]fantasy.StepResult(nil), first.Steps...), cont.Steps...)
	merged.Response = cont.Response
	merged.TotalUsage = fantasy.Usage{
		InputTokens:         first.TotalUsage.InputTokens + cont.TotalUsage.InputTokens,
		OutputTokens:        first.TotalUsage.OutputTokens + cont.TotalUsage.OutputTokens,
		TotalTokens:         first.TotalUsage.TotalTokens + cont.TotalUsage.TotalTokens,
		ReasoningTokens:     first.TotalUsage.ReasoningTokens + cont.TotalUsage.ReasoningTokens,
		CacheCreationTokens: first.TotalUsage.CacheCreationTokens + cont.TotalUsage.CacheCreationTokens,
		CacheReadTokens:     first.TotalUsage.CacheReadTokens + cont.TotalUsage.CacheReadTokens,
	}
	return &merged
}
