package chat

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/home"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// settledTool builds a finished call with a successful result.
func settledTool(sty *styles.Styles, id, name, input string) ToolMessageItem {
	return NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: id, Name: name, Input: input, Finished: true,
	}, &message.ToolResult{ToolCallID: id, Name: name, Content: "ok"}, false)
}

// took sets the call's measured duration by rewriting the timestamps the
// item derives it from.
func took(t ToolMessageItem, d time.Duration) ToolMessageItem {
	item, ok := t.(*baseToolMessageItem)
	if !ok {
		return t
	}
	item.startedAt = time.Now().Add(-d)
	item.finishedAt = time.Now()
	return t
}

func shellRunInput(command string) string {
	return `{"command":` + quoteJSON(command) + `}`
}

func TestGroupHeaderSummarizesTheRun(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	g := NewToolGroupMessageItem(sty, settledTool(sty, "t1", "shell", shellRunInput("ls")))
	g.AddTool(settledTool(sty, "t2", "shell", shellRunInput("pwd")))
	g.AddTool(settledTool(sty, "t3", "view", `{"file_path":"cmd/main.go"}`))

	header := ansi.Strip(strings.Split(g.Render(120), "\n")[0])
	require.Contains(t, header, "3 tool calls")
	assert.Contains(t, header, "Shell ×2")
	assert.Contains(t, header, "View")
	assert.Contains(t, header, "3 tool calls · Shell ×2, View")
}

func TestGroupHeaderNamesFailuresBeforeKinds(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	g := NewToolGroupMessageItem(sty, settledTool(sty, "t1", "shell", shellRunInput("ls")))
	g.AddTool(NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t2", Name: "shell", Input: shellRunInput("false"), Finished: true,
	}, &message.ToolResult{ToolCallID: "t2", Name: "shell", Content: "boom", IsError: true}, false))

	header := ansi.Strip(strings.Split(g.Render(120), "\n")[0])
	assert.Contains(t, header, "2 tool calls, 1 failed")
	assert.Less(t, strings.Index(header, "1 failed"), strings.Index(header, "Shell"))
}

func TestGroupHeaderSumsSettledDurations(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	g := NewToolGroupMessageItem(sty, took(settledTool(sty, "t1", "shell", shellRunInput("ls")), 2*time.Second))
	g.AddTool(took(settledTool(sty, "t2", "shell", shellRunInput("pwd")), 3*time.Second))

	header := ansi.Strip(strings.Split(g.Render(120), "\n")[0])
	assert.Contains(t, header, "5s")
}

func TestGroupHeaderHidesUnknownDurations(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	first := took(settledTool(sty, "t1", "shell", shellRunInput("ls")), 2*time.Second)
	restored := settledTool(sty, "t2", "shell", shellRunInput("pwd"))
	restored.(*baseToolMessageItem).markRestored()
	g := NewToolGroupMessageItem(sty, first)
	g.AddTool(restored)

	header := ansi.Strip(strings.Split(g.Render(120), "\n")[0])
	assert.NotContains(t, header, "2s", "a partial sum must not stand in for the whole run")
	assert.Contains(t, header, "Shell ×2")
}

func TestGroupHeaderFitsItsWidth(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	g := NewToolGroupMessageItem(sty, settledTool(sty, "t1", "shell", shellRunInput("ls")))
	g.AddTool(settledTool(sty, "t2", "view", `{"file_path":"cmd/main.go"}`))

	for _, width := range []int{6, 12, 20, 40, 80} {
		for line := range strings.SplitSeq(ansi.Strip(g.Render(width)), "\n") {
			assert.LessOrEqual(t, ansi.StringWidth(line), width, "width %d: %q", width, line)
		}
	}
}

func TestToolOneLinerMarksFailures(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	failed := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "shell", Input: shellRunInput("false"), Finished: true,
	}, &message.ToolResult{ToolCallID: "t1", Name: "shell", Content: "boom", IsError: true}, false)
	canceled := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t2", Name: "shell", Input: shellRunInput("sleep 1"), Finished: true,
	}, nil, true)

	assert.True(t, strings.HasPrefix(ansi.Strip(ToolOneLiner(sty, failed, 80, false)), styles.ToolFailure+" "))
	assert.True(t, strings.HasPrefix(ansi.Strip(ToolOneLiner(sty, canceled, 80, false)), styles.ToolCancelled+" "))
}

func TestToolOneLinerShowsDiffStat(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	meta, err := json.Marshal(map[string]any{"additions": 12, "removals": 3})
	require.NoError(t, err)
	item := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "edit", Input: `{"file_path":"cmd/main.go"}`, Finished: true,
	}, &message.ToolResult{ToolCallID: "t1", Name: "edit", Content: "ok", Metadata: string(meta)}, false)

	line := ansi.Strip(ToolOneLiner(sty, item, 80, false))
	assert.Contains(t, line, "+12 -3")
}

func TestToolOneLinerRightAlignsSettledDuration(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	item := took(settledTool(sty, "t1", "shell", shellRunInput("go test ./...")), 2*time.Second)

	line := ansi.Strip(ToolOneLiner(sty, item, 60, false))
	assert.Equal(t, 60, ansi.StringWidth(line), "the duration sits on the right edge")
	assert.True(t, strings.HasSuffix(line, "2s"))
}

func TestToolOneLinerHidesDurationWhileRunning(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	running := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "shell", Input: shellRunInput("sleep 10"), Finished: false,
	}, nil, false)
	running.(*baseToolMessageItem).startedAt = time.Now().Add(-5 * time.Second)

	line := ansi.Strip(ToolOneLiner(sty, running, 60, false))
	assert.NotContains(t, line, "5s")
	assert.Equal(t, strings.TrimRight(line, " "), line, "a running call has no duration to align")
}

func TestToolCallSummaryShortensHomePaths(t *testing.T) {
	t.Parallel()

	if home.Dir() == "" {
		t.Skip("no home directory to shorten")
	}
	path := filepath.Join(home.Dir(), "proj", "main.go")
	input, err := json.Marshal(map[string]string{"file_path": path})
	require.NoError(t, err)

	got := ToolCallSummary(message.ToolCall{Name: "view", Input: string(input)})
	assert.Equal(t, "~/proj/main.go", got)
}

func TestTerminalOutputHighlightsDiffs(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	diff := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-package main\n+package chat\n"

	assert.Equal(t,
		toolOutputDiffContentFromUnified(sty, diff, 80, false),
		toolOutputTerminalContent(sty, diff, 80, false),
	)
	assert.Equal(t,
		sty.Tool.Body.Render(toolOutputPlainContent(sty, "plain log line", 80, false)),
		toolOutputTerminalContent(sty, "plain log line", 80, false),
	)
}

func TestShellCallHighlightsDiffOutput(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	diff := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-package main\n+package chat\n"
	item := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "shell", Input: shellRunInput("git diff"), Finished: true,
	}, &message.ToolResult{ToolCallID: "t1", Name: "shell", Content: diff}, false)

	body := item.BodyRender(ToolBodyWidth(80, 0))
	assert.Contains(t, ansi.Strip(body), "package chat")
	assert.True(t, strings.HasSuffix(body, toolOutputDiffContentFromUnified(sty, diff, ToolBodyWidth(80, 0), false)),
		"the shell body must carry the highlighted diff, not the plain output")
}

func TestDiffStatIgnoresNonFileTools(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	meta := `{"additions":1,"removals":1}`
	item := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "shell", Input: shellRunInput("ls"), Finished: true,
	}, &message.ToolResult{ToolCallID: "t1", Name: "shell", Content: "ok", Metadata: meta}, false)

	assert.NotContains(t, ansi.Strip(ToolOneLiner(sty, item, 80, false)), "+1 -1")
}
