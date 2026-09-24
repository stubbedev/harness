package agent

import (
	"errors"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/session"
)

func TestUsageIsZero(t *testing.T) {
	t.Parallel()

	require.True(t, usageIsZero(fantasy.Usage{}))
	require.False(t, usageIsZero(fantasy.Usage{InputTokens: 1}))
	require.False(t, usageIsZero(fantasy.Usage{OutputTokens: 1}))
	require.False(t, usageIsZero(fantasy.Usage{TotalTokens: 1}))
	require.False(t, usageIsZero(fantasy.Usage{ReasoningTokens: 1}))
	require.False(t, usageIsZero(fantasy.Usage{CacheCreationTokens: 1}))
	require.False(t, usageIsZero(fantasy.Usage{CacheReadTokens: 1}))
}

func TestFallbackStepUsageKeepsProviderUsage(t *testing.T) {
	t.Parallel()

	usage := fantasy.Usage{
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
	}
	step := fantasy.StepResult{
		Usage: usage,
	}

	fallbackUsage, estimated := fallbackStepUsage(nil, step)
	require.False(t, estimated)
	require.Equal(t, usage, fallbackUsage)
}

func TestFallbackStepUsageEstimatesPromptAndAssistantText(t *testing.T) {
	t.Parallel()

	messages := []fantasy.Message{
		fantasy.NewUserMessage("please explain the implementation details"),
	}
	step := fantasy.StepResult{
		Content: fantasy.ResponseContent{
			fantasy.TextContent{Text: "the implementation stores state safely"},
		},
	}

	usage, estimated := fallbackStepUsage(messages, step)
	require.True(t, estimated)
	require.Positive(t, usage.InputTokens)
	require.Positive(t, usage.OutputTokens)
	require.Equal(t, usage.InputTokens+usage.OutputTokens, usage.TotalTokens)
}

func TestFallbackStepUsageEstimatesReasoning(t *testing.T) {
	t.Parallel()

	messages := []fantasy.Message{
		{
			Role: fantasy.MessageRoleAssistant,
			Content: []fantasy.MessagePart{
				fantasy.ReasoningPart{Text: "first reason about the request"},
			},
		},
	}
	step := fantasy.StepResult{
		Content: fantasy.ResponseContent{
			fantasy.ReasoningContent{Text: "second reason about the answer"},
		},
	}

	usage, estimated := fallbackStepUsage(messages, step)
	require.True(t, estimated)
	require.Positive(t, usage.InputTokens)
	require.Positive(t, usage.OutputTokens)
}

func TestFallbackStepUsageEstimatesToolCalls(t *testing.T) {
	t.Parallel()

	step := fantasy.StepResult{
		Content: fantasy.ResponseContent{
			fantasy.ToolCallContent{
				ToolCallID: "tool-call-1",
				ToolName:   "view",
				Input:      `{"file_path":"/tmp/example.go"}`,
			},
		},
	}

	usage, estimated := fallbackStepUsage(nil, step)
	require.True(t, estimated)
	require.Zero(t, usage.InputTokens)
	require.Positive(t, usage.OutputTokens)
	require.Equal(t, usage.OutputTokens, usage.TotalTokens)
}

func TestFallbackStepUsageEstimatesToolResults(t *testing.T) {
	t.Parallel()

	messages := []fantasy.Message{
		{
			Role: fantasy.MessageRoleTool,
			Content: []fantasy.MessagePart{
				fantasy.ToolResultPart{
					ToolCallID: "tool-call-1",
					Output: fantasy.ToolResultOutputContentText{
						Text: "file contents returned by the tool",
					},
				},
				fantasy.ToolResultPart{
					ToolCallID: "tool-call-2",
					Output: fantasy.ToolResultOutputContentError{
						Error: errors.New("permission denied"),
					},
				},
				fantasy.ToolResultPart{
					ToolCallID: "tool-call-3",
					Output: fantasy.ToolResultOutputContentMedia{
						MediaType: "image/png",
						Text:      "screenshot",
						Data:      "abc123",
					},
				},
			},
		},
	}

	usage, estimated := fallbackStepUsage(messages, fantasy.StepResult{})
	require.True(t, estimated)
	require.Positive(t, usage.InputTokens)
	require.Zero(t, usage.OutputTokens)
	require.Equal(t, usage.InputTokens, usage.TotalTokens)
}

func TestFallbackStepUsageSkipsClientToolResultsAsOutput(t *testing.T) {
	t.Parallel()

	step := fantasy.StepResult{
		Content: fantasy.ResponseContent{
			fantasy.ToolResultContent{
				ToolCallID: "tool-call-1",
				ToolName:   "shell",
				Result: fantasy.ToolResultOutputContentText{
					Text: "large client-executed payload that should not count as model output tokens",
				},
			},
		},
	}

	usage, estimated := fallbackStepUsage(nil, step)
	require.False(t, estimated)
	require.Zero(t, usage.OutputTokens)
}

func TestFallbackStepUsageCountsProviderToolResultsAsOutput(t *testing.T) {
	t.Parallel()

	step := fantasy.StepResult{
		Content: fantasy.ResponseContent{
			fantasy.ToolResultContent{
				ToolCallID:       "tool-call-1",
				ToolName:         "web_search",
				ProviderExecuted: true,
				ClientMetadata:   "provider metadata",
				Result:           fantasy.ToolResultOutputContentText{Text: "provider-executed result"},
			},
		},
	}

	usage, estimated := fallbackStepUsage(nil, step)
	require.True(t, estimated)
	require.Positive(t, usage.OutputTokens)
	require.Equal(t, usage.OutputTokens, usage.TotalTokens)
}

func TestFallbackStepUsageReturnsZeroWithoutContent(t *testing.T) {
	t.Parallel()

	usage, estimated := fallbackStepUsage(nil, fantasy.StepResult{})
	require.False(t, estimated)
	require.True(t, usageIsZero(usage))
}

func TestUpdateSessionUsageSkipsEstimatedCost(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{ID: "session-id", Cost: 1.25}
	model := Model{CatalogCfg: catalog.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{InputTokens: 1000, OutputTokens: 2000}

	agent.updateSessionUsage(model, currentSession, usage, nil, true)

	require.Equal(t, 1.25, currentSession.Cost)
	require.Equal(t, int64(1000), currentSession.PromptTokens)
	require.Equal(t, int64(2000), currentSession.CompletionTokens)
	require.True(t, currentSession.EstimatedUsage)
}

func TestUpdateSessionUsageCountsEveryPromptBucket(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{ID: "session-id"}
	model := Model{CatalogCfg: catalog.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{
		InputTokens:         50,
		CacheCreationTokens: 30000,
		CacheReadTokens:     1200,
		OutputTokens:        400,
	}

	agent.updateSessionUsage(model, currentSession, usage, nil, false)

	require.Equal(t, int64(31250), currentSession.PromptTokens)
	require.Equal(t, int64(400), currentSession.CompletionTokens)
}

func TestUpdateSessionUsageKeepsCountersForZeroUsage(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{
		ID:               "session-id",
		PromptTokens:     123,
		CompletionTokens: 456,
		Cost:             1.25,
	}
	model := Model{CatalogCfg: catalog.Model{CostPer1MIn: 10, CostPer1MOut: 20}}

	agent.updateSessionUsage(model, currentSession, fantasy.Usage{}, nil, false)

	require.Equal(t, 1.25, currentSession.Cost)
	require.Equal(t, int64(123), currentSession.PromptTokens)
	require.Equal(t, int64(456), currentSession.CompletionTokens)
}

func TestUpdateSessionUsagePreservesOmittedCountersForPartialUsage(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{
		ID:               "session-id",
		PromptTokens:     123,
		CompletionTokens: 456,
	}
	model := Model{CatalogCfg: catalog.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{InputTokens: 789}

	agent.updateSessionUsage(model, currentSession, usage, nil, false)

	require.Equal(t, int64(789), currentSession.PromptTokens)
	require.Equal(t, int64(456), currentSession.CompletionTokens)
}

func TestUpdateSessionUsagePreservesCountersForTotalOnlyUsage(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{
		ID:               "session-id",
		PromptTokens:     123,
		CompletionTokens: 456,
	}
	model := Model{CatalogCfg: catalog.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{TotalTokens: 100}

	agent.updateSessionUsage(model, currentSession, usage, nil, false)

	require.Equal(t, int64(123), currentSession.PromptTokens)
	require.Equal(t, int64(456), currentSession.CompletionTokens)
}

func TestUpdateSessionUsagePreservesPromptForOutputOnlyUsage(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{
		ID:               "session-id",
		PromptTokens:     123,
		CompletionTokens: 456,
	}
	model := Model{CatalogCfg: catalog.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{OutputTokens: 50}

	agent.updateSessionUsage(model, currentSession, usage, nil, false)

	require.Equal(t, int64(123), currentSession.PromptTokens)
	require.Equal(t, int64(50), currentSession.CompletionTokens)
}

func TestUpdateSessionUsageKeepsCountersForEstimatedZeroUsage(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{
		ID:               "session-id",
		PromptTokens:     123,
		CompletionTokens: 456,
		Cost:             1.25,
	}
	model := Model{CatalogCfg: catalog.Model{CostPer1MIn: 10, CostPer1MOut: 20}}

	agent.updateSessionUsage(model, currentSession, fantasy.Usage{}, nil, true)

	require.Equal(t, 1.25, currentSession.Cost)
	require.Equal(t, int64(123), currentSession.PromptTokens)
	require.Equal(t, int64(456), currentSession.CompletionTokens)
}

func TestUpdateSessionUsageAddsProviderCost(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{ID: "session-id", Cost: 1.25}
	model := Model{CatalogCfg: catalog.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{InputTokens: 1000, OutputTokens: 2000}

	agent.updateSessionUsage(model, currentSession, usage, nil, false)

	require.Equal(t, 1.3, currentSession.Cost)
	require.Equal(t, int64(1000), currentSession.PromptTokens)
	require.Equal(t, int64(2000), currentSession.CompletionTokens)
	require.False(t, currentSession.EstimatedUsage)
}

func TestHistoryTokenEstimatorMatchesFullEstimate(t *testing.T) {
	t.Parallel()

	history := []fantasy.Message{
		fantasy.NewSystemMessage("You are a coding agent."),
		fantasy.NewUserMessage(strings.Repeat("please fix the failing test in the parser package ", 20)),
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
			fantasy.ToolCallPart{ToolCallID: "call-1", ToolName: "shell", Input: `{"command":"go test ./parser/..."}`},
		}},
		{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
			fantasy.ToolResultPart{ToolCallID: "call-1", Output: fantasy.ToolResultOutputContentText{Text: strings.Repeat("--- FAIL: TestParse (0.00s)\n", 40)}},
		}},
	}
	estimator := newHistoryTokenEstimator()
	require.Equal(t, estimateMessageTokens(history), estimator.Messages(history))

	// The next step appends to the same history.
	history = append(history, fantasy.NewUserMessage(strings.Repeat("and then run the whole suite again ", 10)))
	require.Equal(t, estimateMessageTokens(history), estimator.Messages(history))

	// A rewrite in place (aging, a merge, compaction) holds new strings
	// of the same length, which must be counted rather than recognized.
	rewritten := append([]fantasy.Message(nil), history...)
	result := rewritten[3].Content[0].(fantasy.ToolResultPart)
	body := result.Output.(fantasy.ToolResultOutputContentText).Text
	result.Output = fantasy.ToolResultOutputContentText{Text: strings.Repeat("x", len(body))}
	rewritten[3].Content = []fantasy.MessagePart{result}
	require.NotEqual(t, estimateMessageTokens(history), estimateMessageTokens(rewritten))
	require.Equal(t, estimateMessageTokens(rewritten), estimator.Messages(rewritten))
}
