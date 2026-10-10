package agent

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// projectionSeed keeps every generated text new to the process-wide count
// cache, so a test sees strings nothing has counted yet.
var projectionSeed atomic.Int64

func uncountedText(size int) string {
	return benchmarkToolOutput(size, int(projectionSeed.Add(1))+1_000_000)
}

func projectionHistory(results ...string) []fantasy.Message {
	msgs := []fantasy.Message{fantasy.NewSystemMessage("You are a coding agent."), fantasy.NewUserMessage("Fix the failing tests.")}
	for i, text := range results {
		id := fmt.Sprintf("call-%d", i)
		msgs = append(msgs,
			fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.ToolCallPart{ToolCallID: id, ToolName: "view", Input: `{"file_path":"main.go"}`}}},
			fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: id, Output: fantasy.ToolResultOutputContentText{Text: text}}}},
		)
	}
	return msgs
}

// uncachedTokens is the full count of msgs, taken without filling the
// process-wide count cache, so the strings stay uncounted for the
// estimator under test.
func uncachedTokens(t *testing.T, msgs []fantasy.Message) int64 {
	t.Helper()
	codec, err := tokenCodec()
	require.NoError(t, err)
	return estimateMessageTokensWith(func(s string) int64 { return countTokens(codec, s) }, msgs)
}

// Whatever the limit, the projection reaches it exactly when the full
// count does: the auto-summarize abort and stop condition compare it
// with that limit and nothing else.
func TestProjectDecidesAsTheFullCountDoes(t *testing.T) {
	t.Parallel()

	shape := func() []fantasy.Message {
		return projectionHistory(uncountedText(8<<10), uncountedText(30<<10), uncountedText(500), strings.Repeat("\xff\xfe", 600)+uncountedText(200))
	}
	full := uncachedTokens(t, shape())
	for _, limit := range []int64{1, full / 2, full - 1, full, full + 1, full * 2, full * 10, 1 << 40} {
		msgs := shape()
		want := uncachedTokens(t, msgs)
		got := newHistoryTokenEstimator().Project(msgs, limit)
		require.Equal(t, want >= limit, got >= limit, "limit %d: full count %d, projection %d", limit, want, got)
		if want >= limit {
			require.Equal(t, want, got, "a request at or over its limit is counted in full")
		}
	}
}

// Far from the limit a new string is not tokenized at all, and the next
// step looks at it again: once its count is known, it is used.
func TestProjectLeavesNewTextUncountedFarFromTheLimit(t *testing.T) {
	t.Parallel()

	result := uncountedText(80 << 10)
	msgs := projectionHistory(result)
	estimator := newHistoryTokenEstimator()

	rough := estimator.Project(msgs, 1_000_000)
	_, counted := cachedTokenCount(result)
	require.False(t, counted, "a request far under its limit tokenized its new text")
	require.Less(t, rough, int64(1_000_000))

	warmTokenCount(result)
	require.Equal(t, uncachedTokens(t, msgs), estimator.Project(msgs, 1_000_000),
		"a count that became known is not picked up")
}

// Near the limit the projection counts as much as the decision needs and
// no more.
func TestProjectCountsOnlyWhatTheDecisionNeeds(t *testing.T) {
	t.Parallel()

	small, large := uncountedText(4<<10), uncountedText(64<<10)
	msgs := projectionHistory(small, large)
	full := uncachedTokens(t, msgs)

	// The uncounted bytes could reach this limit, the true count cannot,
	// and counting the larger string alone shows it.
	limit := full + 8<<10
	got := newHistoryTokenEstimator().Project(msgs, limit)
	require.Less(t, got, limit)
	_, largeCounted := cachedTokenCount(large)
	require.True(t, largeCounted, "the string the decision hinged on was not counted")
	_, smallCounted := cachedTokenCount(small)
	require.False(t, smallCounted, "a string the decision did not need was counted")
}

// A string the request holds twice counts twice, settled or not.
func TestProjectCountsEveryOccurrence(t *testing.T) {
	t.Parallel()

	repeated := uncountedText(16 << 10)
	msgs := append(projectionHistory(repeated), projectionHistory(repeated)[2:]...)
	full := uncachedTokens(t, msgs)
	require.Equal(t, full, newHistoryTokenEstimator().Project(msgs, full))
	require.Equal(t, full, newHistoryTokenEstimator().Messages(msgs))
}

// tokenUpperBound must hold for any text, invalid UTF-8 included, or a
// request could be judged under its limit when it is not.
func TestTokenUpperBoundHolds(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		uncountedText(4 << 10),
		strings.Repeat("\xff", 512),
		strings.Repeat("é中🙂", 200),
		strings.Repeat(" ", 1000),
		strings.Repeat("a\x00b\x80", 300),
	} {
		require.LessOrEqual(t, approxTokenCount(s), tokenUpperBound(s), "%q", s[:16])
		require.LessOrEqual(t, roughTokenCount(s), tokenUpperBound(s), "%q", s[:16])
	}
}
