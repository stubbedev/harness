package agent

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"charm.land/fantasy"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/hooks"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

// Compaction keeps a session inside its model's context window without
// anything showing in the transcript. Every message row stays where it
// is; what changes is the request assembled from them, in three layers
// that each cost less than the next:
//
//  1. Aging. Tool results are the bulk of a long session and the least
//     worth re-reading. Once the request passes compactionAgeRatio of the
//     usable window, every tool result before the most recent user turn
//     is sent as a one-line stub instead. The watermark this leaves on
//     the session (CompactionAgedID) moves only when the threshold is
//     crossed again, so the request prefix stays stable in between and
//     prompt caching keeps working.
//  2. Folding. Past compactionSummarizeRatio, the oldest history is
//     summarized into the session's hidden summary and the request
//     restarts from the boundary this leaves (CompactionBoundaryID),
//     keeping the most recent compactionKeepRatio of the window
//     verbatim. The summary is carried into the next fold, so it is
//     rolling: one summary covers everything before the boundary.
//  3. Prediction. Both run between turns, on the request the next turn
//     is projected to send, so the user never waits on a compaction in
//     the middle of an answer. The hard checks inside a turn (the stop
//     condition and PrepareStep's projection) remain as the safety net
//     and fold everything when they fire.
//
// The summary is written by the small model when it fits that model's
// window, by the large one otherwise.
const (
	compactionAgeRatio       = 0.5
	compactionSummarizeRatio = 0.75
	compactionKeepRatio      = 0.25

	// compactionStubMaxChars is the size below which a tool result is
	// not worth stubbing: the stub would be about as long.
	compactionStubMaxChars = 240
)

// sessionHistory returns what the model is sent for a session: the
// messages after the compaction boundary, verbatim, plus the hidden
// summary standing in for everything up to it. Sessions compacted before
// the boundary existed carry their summary as a message the transcript
// hides; those resume from that message, as they always did.
func (a *sessionAgent) sessionHistory(ctx context.Context, s session.Session) (msgs []message.Message, summary string, err error) {
	all, err := a.messages.List(ctx, s.ID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to list messages: %w", err)
	}
	tail, agedIdx := compactedTail(s, all)
	return stubAgedResults(tail, agedIdx), s.CompactionSummary, nil
}

// compactedTail slices the session's messages down to the ones sent
// verbatim and reports the index up to which tool results are aged (-1
// when none are). The boundary and the watermark are message IDs, so a
// message deleted underneath them just means nothing is cut there.
func compactedTail(s session.Session, all []message.Message) (tail []message.Message, agedIdx int) {
	tail = all
	if s.CompactionBoundaryID != "" {
		if i := indexOfMessage(all, s.CompactionBoundaryID); i >= 0 {
			tail = all[i+1:]
		}
	} else if s.SummaryMessageID != "" {
		// A session compacted the old way: its summary is a message and
		// the conversation restarts from it, re-rooted as the user's.
		if i := indexOfMessage(all, s.SummaryMessageID); i >= 0 {
			tail = append([]message.Message(nil), all[i:]...)
			tail[0].Role = message.User
		}
	}
	agedIdx = -1
	if s.CompactionAgedID != "" {
		agedIdx = indexOfMessage(tail, s.CompactionAgedID)
	}
	return tail, agedIdx
}

func indexOfMessage(msgs []message.Message, id string) int {
	for i, m := range msgs {
		if m.ID == id {
			return i
		}
	}
	return -1
}

// stubAgedResults replaces the text of every tool result up to and
// including index agedIdx with a stub naming what was there. The
// messages are copied; the stored rows are untouched. Media results and
// results already short enough to be their own stub are left alone.
func stubAgedResults(msgs []message.Message, agedIdx int) []message.Message {
	if agedIdx < 0 {
		return msgs
	}
	out := make([]message.Message, len(msgs))
	copy(out, msgs)
	for i := 0; i <= agedIdx && i < len(out); i++ {
		if out[i].Role != message.Tool {
			continue
		}
		parts := make([]message.ContentPart, len(out[i].Parts))
		for j, part := range out[i].Parts {
			tr, ok := part.(message.ToolResult)
			if !ok || tr.Data != "" || len(tr.Content) <= compactionStubMaxChars {
				parts[j] = part
				continue
			}
			tr.Content = toolResultStub(tr)
			parts[j] = tr
		}
		out[i].Parts = parts
	}
	return out
}

// toolResultStub is what an aged tool result is sent as: enough to know
// there was output and how to get it back, and nothing of the output.
func toolResultStub(tr message.ToolResult) string {
	name := tr.Name
	if name == "" {
		name = "tool"
	}
	lines := strings.Count(tr.Content, "\n") + 1
	return fmt.Sprintf("[%s output from earlier in the session elided to save context: %d lines, %d characters; run it again if you need it]",
		name, lines, len(tr.Content))
}

// summaryMessage is how the hidden summary enters the request: a user
// message ahead of the verbatim history, in a tag the model reads as
// context rather than as something the user just said.
func summaryMessage(summary string) fantasy.Message {
	return fantasy.NewUserMessage(
		"<session_memory>\nEarlier parts of this conversation were compacted into the summary below. " +
			"The messages after it are the most recent ones, verbatim.\n\n" +
			summary + "\n</session_memory>")
}

// withSummary prepends the hidden summary to an assembled history.
func withSummary(summary string, history []fantasy.Message) []fantasy.Message {
	if summary == "" {
		return history
	}
	return mergeConsecutiveUserMessages(append([]fantasy.Message{summaryMessage(summary)}, history...))
}

// projectRequest estimates the tokens the next turn's request would
// carry for this history: system prompt, summary, and the messages.
func (a *sessionAgent) projectRequest(summary string, msgs []message.Message, supportsImages bool) int64 {
	history, _ := a.preparePrompt(msgs, supportsImages)
	history = withSummary(summary, history)
	return approxTokenCount(a.systemPrompt.Get()) + estimateMessageTokens(history)
}

// lastUserTurnStart is the index of the most recent message the user
// typed, or 0 when there is none: everything before it belongs to
// earlier turns.
func lastUserTurnStart(msgs []message.Message) int {
	for i, m := range slices.Backward(msgs) {
		if isUserText(m) {
			return i
		}
	}
	return 0
}

// isUserText reports whether m is a message the user typed (as opposed
// to a tool result or an injected reminder with no text).
func isUserText(m message.Message) bool {
	if m.Role != message.User {
		return false
	}
	for _, part := range m.Parts {
		if tc, ok := part.(message.TextContent); ok && strings.TrimSpace(tc.Text) != "" {
			return true
		}
	}
	return false
}

// foldCut chooses where the verbatim tail begins after a fold: the most
// recent stretch of messages worth about keepTokens, extended backwards
// to the user message that started that turn so the tail never opens on
// a tool result or an answer to a question the model can no longer see.
// It returns the number of leading messages to fold; 0 means the tail
// is already small enough.
func (a *sessionAgent) foldCut(msgs []message.Message, keepTokens int64, supportsImages bool) int {
	var kept int64
	cut := len(msgs)
	for cut > 0 {
		aiMsgs := msgs[cut-1].ToAIMessage()
		if !supportsImages {
			for i := range aiMsgs {
				aiMsgs[i].Content = filterFileParts(aiMsgs[i].Content)
			}
		}
		size := estimateMessageTokens(aiMsgs)
		if kept+size > keepTokens {
			break
		}
		kept += size
		cut--
	}
	if cut >= len(msgs) {
		// Not even the last message fits the budget on its own: keep the
		// last user turn whole anyway, a tail has to start somewhere.
		return lastUserTurnStart(msgs)
	}
	for cut > 0 && !isUserText(msgs[cut]) {
		cut--
	}
	return cut
}

// maintainContext is the one entry point for compaction: it takes the
// session's history as it stands and applies whichever layers the
// projected request calls for. With force set - a context-window
// overflow, the mid-turn stop condition, or a manual /compact - the
// whole verbatim history is folded regardless of size. It reports
// whether the session changed. trigger and instructions go to the
// compaction hooks and the summary prompt.
func (a *sessionAgent) maintainContext(
	ctx context.Context,
	sessionID string,
	opts fantasy.ProviderOptions,
	onAuthRefresh func(context.Context, *fantasy.ProviderError) error,
	trigger, instructions string,
	force bool,
) (bool, error) {
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("failed to get session: %w", err)
	}
	all, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("failed to list messages: %w", err)
	}
	if len(all) == 0 {
		return false, nil
	}
	large := a.largeModel.Get()
	cw := usableContextWindow(large)
	if cw <= 0 && !force {
		// An unknown window is nothing to plan against; only an explicit
		// request compacts here.
		return false, nil
	}
	supportsImages := large.CatalogCfg.SupportsImages
	tail, agedIdx := compactedTail(sess, all)
	if len(tail) == 0 {
		return false, nil
	}
	projected := a.projectRequest(sess.CompactionSummary, stubAgedResults(tail, agedIdx), supportsImages)
	over := func(ratio float64) bool { return cw > 0 && float64(projected) > ratio*float64(cw) }
	changed := false

	// Layer 1: age the tool results of every turn but the current one.
	if force || over(compactionAgeRatio) {
		if start := lastUserTurnStart(tail); start > 0 && tail[start-1].ID != sess.CompactionAgedID {
			sess.CompactionAgedID = tail[start-1].ID
			agedIdx = start - 1
			projected = a.projectRequest(sess.CompactionSummary, stubAgedResults(tail, agedIdx), supportsImages)
			changed = true
		}
	}

	// Layer 2: fold the oldest history into the summary.
	if force || over(compactionSummarizeRatio) {
		cut := len(tail)
		if !force {
			cut = a.foldCut(tail, int64(compactionKeepRatio*float64(cw)), supportsImages)
		}
		if cut > 0 {
			if !a.isSubAgent && a.hooks.Has(hooks.EventPreCompact) {
				if _, hookErr := a.hooks.Run(ctx, hooks.EventContext{
					Event: hooks.EventPreCompact, SessionID: sessionID, Trigger: trigger,
				}); hookErr != nil {
					slog.Warn("PreCompact hook error", "error", hookErr)
				}
			}
			summary, err := a.summarizeMessages(ctx, &sess, sess.CompactionSummary, tail[:cut], opts, onAuthRefresh, instructions)
			if err != nil {
				return changed, err
			}
			sess.CompactionSummary = summary
			sess.CompactionBoundaryID = tail[cut-1].ID
			sess.CompactionAgedID = ""
			sess.SummaryMessageID = ""
			tail = tail[cut:]
			projected = a.projectRequest(summary, tail, supportsImages)
			changed = true
			// The transcript those diagnostics were reported into is
			// gone. Start the record over, or a standing error stays
			// suppressed as already-reported against a context that no
			// longer mentions it.
			tools.ForgetReportedDiagnostics(a.lspManager, sessionID)
			if !a.isSubAgent && a.hooks.Has(hooks.EventPostCompact) {
				if _, hookErr := a.hooks.Run(ctx, hooks.EventContext{
					Event: hooks.EventPostCompact, SessionID: sessionID, Trigger: trigger,
				}); hookErr != nil {
					slog.Warn("PostCompact hook error", "error", hookErr)
				}
			}
		}
	}

	if !changed {
		return false, nil
	}
	// The counters describe the request the session now stands at, which
	// nothing has measured; the next turn's usage replaces the estimate.
	sess.PromptTokens = projected
	sess.CompletionTokens = 0
	sess.EstimatedUsage = true
	if _, err := a.sessions.Save(ctx, sess); err != nil {
		return true, fmt.Errorf("failed to save compacted session: %w", err)
	}
	slog.Info("Session compacted", "session_id", sessionID, "trigger", trigger,
		"projected_tokens", projected, "usable_window", cw,
		"folded", sess.CompactionBoundaryID != "", "aged", sess.CompactionAgedID != "")
	return true, nil
}

// summarizeMessages writes the rolling summary: the previous summary,
// if any, followed by the messages being folded, condensed by the small
// model into one briefing. The small model takes it when the fold fits
// its window; otherwise the large model does, since it is the one that
// has to read the result anyway. Usage is charged to the session.
func (a *sessionAgent) summarizeMessages(
	ctx context.Context,
	sess *session.Session,
	previous string,
	folded []message.Message,
	opts fantasy.ProviderOptions,
	onAuthRefresh func(context.Context, *fantasy.ProviderError) error,
	instructions string,
) (string, error) {
	large := a.largeModel.Get()
	model := a.smallModel.Get()
	if model.Model == nil {
		model = large
	}
	history, _ := a.preparePrompt(folded, model.CatalogCfg.SupportsImages)
	history = withSummary(previous, history)
	if cw := usableContextWindow(model); model.Model != large.Model && cw > 0 &&
		approxTokenCount(string(summaryPrompt))+estimateMessageTokens(history) > cw {
		model = large
		history, _ = a.preparePrompt(folded, model.CatalogCfg.SupportsImages)
		history = withSummary(previous, history)
	}
	systemPromptPrefix := a.systemPromptPrefix.Get()

	agent := fantasy.NewAgent(
		model.Model,
		fantasy.WithSystemPrompt(string(summaryPrompt)),
		a.retryOption(),
		fantasy.WithUserAgent(userAgent),
	)
	resp, err := agent.Generate(ctx, fantasy.AgentCall{
		Prompt:          buildSummaryPrompt(instructions),
		Messages:        history,
		Headers:         sessionHeaders(sess.ID),
		ProviderOptions: opts,
		OnAuthRefresh:   onAuthRefresh,
		PrepareStep: func(callContext context.Context, options fantasy.PrepareStepFunctionOptions) (context.Context, fantasy.PrepareStepResult, error) {
			prepared := fantasy.PrepareStepResult{Messages: options.Messages}
			if systemPromptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{fantasy.NewSystemMessage(systemPromptPrefix)}, prepared.Messages...)
			}
			return callContext, prepared, nil
		},
	})
	if err != nil {
		return "", fmt.Errorf("summarize session: %w", err)
	}
	text := strings.TrimSpace(resp.Response.Content.Text())
	if text == "" {
		return "", fmt.Errorf("summarize session: the model returned no summary")
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
	a.updateSessionUsage(model, sess, resp.TotalUsage, cost, false)
	return text, nil
}

// buildSummaryPrompt is the user turn of the summary request; the
// instructions, when given, steer what the summary keeps.
func buildSummaryPrompt(instructions string) string {
	var sb strings.Builder
	sb.WriteString("Provide a detailed summary of our conversation above.")
	if instructions = strings.TrimSpace(instructions); instructions != "" {
		sb.WriteString("\n\n## Focus\n\n")
		sb.WriteString(instructions)
		sb.WriteString("\n")
	}
	return sb.String()
}
