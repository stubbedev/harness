package agent

import (
	"fmt"
	"slices"
	"testing"

	"charm.land/fantasy"
)

// benchmarkFantasyHistory is about 1.9 MB of request history in the shape
// a long tool-heavy turn has: a prompt, then assistant tool calls each
// answered by a 12 KB result, with short text in between.
func benchmarkFantasyHistory() []fantasy.Message {
	msgs := []fantasy.Message{fantasy.NewSystemMessage("You are a coding agent."), fantasy.NewUserMessage("Fix the failing tests.")}
	for i := range 160 {
		id := fmt.Sprintf("call-%d", i)
		msgs = append(msgs,
			fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
				fantasy.TextPart{Text: "Let me look at the next package."},
				fantasy.ToolCallPart{ToolCallID: id, ToolName: "shell", Input: fmt.Sprintf(`{"command":"go test ./pkg%d/..."}`, i)},
			}},
			fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
				fantasy.ToolResultPart{ToolCallID: id, Output: fantasy.ToolResultOutputContentText{Text: benchmarkToolOutput(12<<10, i)}},
			}},
		)
	}
	return msgs
}

// BenchmarkEstimateMessageTokens is the full estimate over a history
// whose strings are all in the token count cache already, which is the
// state every step after the first finds it in.
func BenchmarkEstimateMessageTokens(b *testing.B) {
	msgs := benchmarkFantasyHistory()
	estimateMessageTokens(msgs)
	b.ReportAllocs()
	for b.Loop() {
		estimateMessageTokens(msgs)
	}
}

// BenchmarkHistoryTokenEstimatorStep is the per-step estimate a turn
// pays: the same history as BenchmarkEstimateMessageTokens, one step
// on, with a new tool call and result behind it.
func BenchmarkHistoryTokenEstimatorStep(b *testing.B) {
	msgs := benchmarkFantasyHistory()
	estimator := newHistoryTokenEstimator()
	estimator.Messages(msgs)
	next := append(slices.Clip(msgs),
		fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.ToolCallPart{ToolCallID: "next", ToolName: "view", Input: `{"file_path":"main.go"}`}}},
		fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "next", Output: fantasy.ToolResultOutputContentText{Text: benchmarkToolOutput(12<<10, -1)}}}},
	)
	b.ReportAllocs()
	for b.Loop() {
		estimator.Messages(next)
	}
}
