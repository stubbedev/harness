package agent

import (
	"context"
	"fmt"

	"github.com/stubbedev/harness/internal/message"
)

// interruptedTurnTitle and interruptedTurnDetails are the banner a turn
// recovery closed carries.
const (
	interruptedTurnTitle   = "Interrupted"
	interruptedTurnDetails = "Harness stopped before this turn finished."
)

// RecoverInterruptedTurns closes every turn a process that died left
// unfinished: an assistant message without a finish, and tool calls
// without a result. It must run once per database before any turn does;
// the data directory lock keeps every other process off the database, so
// nothing it finds unfinished is still running.
//
// A message whose tool calls all returned only missed its finish: the
// process died between the last result and the end of the step, so it is
// closed as the tool round it was. Anything else is closed as interrupted:
// each call without a result gets one telling the model whether it is safe
// to call again, and the message gets the interrupted banner.
func RecoverInterruptedTurns(ctx context.Context, messages message.Service) error {
	unfinished, err := messages.ListUnfinishedAssistants(ctx)
	if err != nil {
		return fmt.Errorf("recover interrupted turns: %w", err)
	}
	for start := 0; start < len(unfinished); {
		end := start + 1
		for end < len(unfinished) && unfinished[end].SessionID == unfinished[start].SessionID {
			end++
		}
		if err := recoverSessionTurns(ctx, messages, unfinished[start:end]); err != nil {
			return fmt.Errorf("recover interrupted turns of session %s: %w", unfinished[start].SessionID, err)
		}
		start = end
	}
	return nil
}

// recoverSessionTurns closes the unfinished assistant messages of one
// session, given oldest first.
func recoverSessionTurns(ctx context.Context, messages message.Service, unfinished []message.Message) error {
	later, err := messages.ListFrom(ctx, unfinished[0].SessionID, unfinished[0].ID)
	if err != nil {
		return err
	}
	answered := answeredToolCalls(later)
	for i := range unfinished {
		if err := closeInterruptedTurn(ctx, messages, &unfinished[i], answered); err != nil {
			return fmt.Errorf("close message %s: %w", unfinished[i].ID, err)
		}
	}
	return nil
}

// closeInterruptedTurn finishes one assistant message a dead process left
// unfinished.
func closeInterruptedTurn(ctx context.Context, messages message.Service, assistant *message.Message, answered map[string]bool) error {
	if toolRoundComplete(assistant, answered) {
		assistant.AddFinish(message.FinishReasonToolUse, "", "")
		return messages.Update(ctx, *assistant)
	}
	assistant.FinishThinking()
	stripOrphanThinking(assistant)
	err := closeToolCalls(ctx, messages, assistant, answered, func(call message.ToolCall, unfinished bool) message.ToolResult {
		content := interruptedToolResult(call)
		if unfinished {
			content = "Error: the tool was not run: the turn was interrupted before its arguments finished streaming."
		}
		return message.ToolResult{ToolCallID: call.ID, Name: call.Name, Content: content, IsError: true}
	})
	if err != nil {
		return err
	}
	assistant.AddFinish(message.FinishReasonInterrupted, interruptedTurnTitle, interruptedTurnDetails)
	return messages.Update(ctx, *assistant)
}

// toolRoundComplete reports whether assistant made tool calls and every
// one of them was fully streamed and returned a result.
func toolRoundComplete(assistant *message.Message, answered map[string]bool) bool {
	calls := assistant.ToolCalls()
	if len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		if !call.Finished || !answered[call.ID] {
			return false
		}
	}
	return true
}

// answeredToolCalls returns the IDs of the tool calls msgs hold a result
// for.
func answeredToolCalls(msgs []message.Message) map[string]bool {
	answered := make(map[string]bool)
	for _, msg := range msgs {
		if msg.Role != message.Tool {
			continue
		}
		for _, result := range msg.ToolResults() {
			answered[result.ToolCallID] = true
		}
	}
	return answered
}

// closeToolCalls stores a result, built by result, for every tool call of
// assistant that answered holds none for. A call whose arguments never
// finished streaming is first marked finished with empty arguments, so
// the stored call is valid JSON; result is told it was unfinished.
func closeToolCalls(
	ctx context.Context,
	messages message.Service,
	assistant *message.Message,
	answered map[string]bool,
	result func(call message.ToolCall, unfinished bool) message.ToolResult,
) error {
	for _, call := range assistant.ToolCalls() {
		unfinished := !call.Finished
		if unfinished {
			call.Finished = true
			call.Input = "{}"
			assistant.AddToolCall(call)
			// Buffered: the result row created below writes it first.
			if err := messages.UpdateBuffered(ctx, *assistant); err != nil {
				return err
			}
		}
		if answered[call.ID] {
			continue
		}
		_, err := messages.Create(ctx, assistant.SessionID, message.CreateMessageParams{
			Role:  message.Tool,
			Parts: []message.ContentPart{result(call, unfinished)},
		})
		if err != nil {
			return fmt.Errorf("store result of tool call %s: %w", call.ID, err)
		}
	}
	return nil
}
