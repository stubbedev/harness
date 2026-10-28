package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

// waitReport is a settled wait's result: the outcome sentence, then one
// item per piece of work it watched.
const waitReport = "All waited work finished.\n\n" +
	"background job job-1 (go test ./...): exited 0\n" +
	"  Output:\n  ok\n"

func waitCall(id, input string, finished bool) message.ToolCall {
	return message.ToolCall{ID: id, Name: "wait", Input: input, Finished: finished}
}

// TestRunningWaitNamesItsTarget pins a running wait's header: the
// progressive label, what it watches, and no redundant "Waiting for
// tool response" line under it.
func TestRunningWaitNamesItsTarget(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{"everything", `{}`, "Waiting everything outstanding"},
		{"named ids", `{"wait_for":["job-1","timer-2"]}`, "Waiting job-1, timer-2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			item := NewToolMessageItem(sty, "msg", waitCall("t1", tt.input, false), nil, false)
			require.Equal(t, ToolStatusRunning, item.EffectiveStatus())
			require.Equal(t, "Waiting", item.DisplayName())

			plain := ansi.Strip(item.Render(80))
			require.Contains(t, plain, tt.want)
			require.NotContains(t, plain, "Waiting for tool response")
		})
	}
}

// TestSettledWaitShowsTheReport pins a settled wait's header and body:
// the past-tense label, the same target summary, and the collected
// report as its body.
func TestSettledWaitShowsTheReport(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{"everything", `{}`, "Waited everything outstanding"},
		{"named ids", `{"wait_for":["job-1","timer-2"]}`, "Waited job-1, timer-2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			item := NewToolMessageItem(sty, "msg", waitCall("t1", tt.input, true), &message.ToolResult{
				ToolCallID: "t1",
				Name:       "wait",
				Content:    waitReport,
			}, false)
			require.Equal(t, "Waited", item.DisplayName())

			plain := ansi.Strip(item.Render(80))
			require.Contains(t, plain, tt.want)
			require.Contains(t, plain, "All waited work finished.")
			require.Contains(t, plain, "background job job-1")
			require.Contains(t, plain, "ok")
		})
	}
}

// TestSettledWaitReportExpands covers a report longer than the
// collapsed body: the full report only shows on expansion.
func TestSettledWaitReportExpands(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	report := "All waited work finished.\n"
	for range 20 {
		report += "\nbackground job job-1: exited 0\n  Output:\n  ok\n"
	}
	item := NewToolMessageItem(sty, "msg", waitCall("t1", `{}`, true), &message.ToolResult{
		ToolCallID: "t1",
		Name:       "wait",
		Content:    report,
	}, false)

	require.Contains(t, ansi.Strip(item.Render(80)), "lines hidden")
	item.SetExpansionLevel(1)
	require.NotContains(t, ansi.Strip(item.Render(80)), "lines hidden")
}

// TestRunningWaitCountsAsWorkInProgress pins that a wait call is a
// first-class member of the run: a group names it by its progressive
// label and carries it in the running count.
func TestRunningWaitCountsAsWorkInProgress(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	g := NewToolGroupMessageItem(sty, bashTool("t1", "ls", false))
	g.AddTool(NewToolMessageItem(sty, "msg", waitCall("t2", `{}`, false), nil, false))

	header := ansi.Strip(strings.Split(g.Render(120), "\n")[0])
	require.Contains(t, header, "Running (2 calls")
	require.Contains(t, header, "Waiting")
	require.NotContains(t, header, "Wait,")
}

// TestTimerShowsItsArmedLength pins a timer call's header and result:
// the armed length is humanised and the tool's acknowledgement is the
// body.
func TestTimerShowsItsArmedLength(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	item := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "timer", Input: `{"seconds":300}`, Finished: true,
	}, &message.ToolResult{
		ToolCallID: "t1",
		Name:       "timer",
		Content: "[timer timer-1 armed for 5m0s; it fires at 2026-10-28T12:00:00Z]\n" +
			"Its result arrives through the wait tool, with no polling needed.",
	}, false)

	require.Equal(t, "Timer", item.DisplayName())

	plain := ansi.Strip(item.Render(80))
	require.Contains(t, plain, "Timer 5m 0s")
	require.Contains(t, plain, "timer-1 armed")
	require.Contains(t, plain, "wait tool")
}

// TestPendingTimerHasNoLengthYet pins that a timer whose input is still
// streaming shows its label without inventing a duration.
func TestPendingTimerHasNoLengthYet(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	item := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "timer", Input: `{"seconds":`,
	}, nil, false)

	plain := ansi.Strip(item.Render(80))
	firstLine, _, _ := strings.Cut(plain, "\n")
	require.Contains(t, firstLine, "Timer")
	require.NotContains(t, firstLine, "0s", "the armed length is unknown while the input streams")
}
