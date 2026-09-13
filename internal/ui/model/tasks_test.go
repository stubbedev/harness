package model

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/subagents"
	"github.com/stubbedev/harness/internal/ui/chat"
)

// agentToolCall builds the tool call a parent-session agent dispatch
// produces.
func agentToolCall(id string) message.ToolCall {
	return message.ToolCall{
		ID:       id,
		Name:     "agent",
		Input:    `{"subagent_type":"researcher","prompt":"dig into the git history"}`,
		Finished: false,
	}
}

func TestBackgroundTasksStrip(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.width = 100

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))

	task := u.agentTaskByToolCall("a1")
	require.NotNil(t, task)
	assert.Equal(t, "researcher", task.name)
	assert.True(t, u.tasksSpinning(), "a fresh dispatch with no result is running")

	// The strip renders one row per task and names it.
	u.tasksAreaHeight()
	require.NotEmpty(t, u.tasksView)
	assert.Contains(t, ansi.Strip(u.tasksView), "researcher")
	require.Len(t, u.taskRows, 1)

	// Expanding via the recorded row shows the prompt; clicking again
	// collapses it.
	assert.True(t, u.handleTaskClick(2, 0))
	assert.Equal(t, "a1", u.expandedTaskID)
	u.tasksAreaHeight()
	assert.Contains(t, ansi.Strip(u.tasksView), "dig into the git history")
	assert.True(t, u.handleTaskClick(2, 0))
	assert.Empty(t, u.expandedTaskID)

	// The final result completes the task — and reaps it: the strip is a
	// viewer for ongoing work only.
	assert.True(t, u.resolveAgentTaskResult(message.ToolResult{
		ToolCallID: "a1", Name: "agent", Content: "found the culprit",
	}))
	assert.Equal(t, subagents.StatusCompleted, task.status)
	assert.False(t, u.tasksSpinning())
	assert.Empty(t, u.agentTasks, "a finished task leaves the strip")

	// Session switches drop the strip.
	u.resetAgentTasks()
	assert.Empty(t, u.agentTasks)
	assert.Equal(t, 0, u.tasksAreaHeight())
}

func TestBackgroundTasksWindowScrolls(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.width = 100

	// Four running tasks: only three rows render, and the window
	// follows the cursor.
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		_ = u.upsertAgentTask(&message.Message{ID: "m", Role: message.Assistant}, agentToolCall(id))
	}
	require.Len(t, u.agentTasks, 4)

	u.tasksAreaHeight()
	out := ansi.Strip(u.tasksView)
	// The window renders at most three task rows.
	assert.LessOrEqual(t, strings.Count(out, "researcher"), 3)
	assert.LessOrEqual(t, strings.Count(out, "\n")+1, 3)

	// Move the cursor past the window end: the window scrolls with it.
	u.taskCursor = 3
	window, start := u.taskWindow()
	require.Len(t, window, 3)
	assert.Equal(t, 1, start, "the window follows the cursor onto the last task")
	assert.Equal(t, "a4", window[len(window)-1].toolCallID)

	// Reaping the task under the cursor clamps the cursor back in range.
	u.reapAgentTask("a4")
	u.clampTaskCursor()
	assert.Equal(t, 2, u.taskCursor)
}

func TestBackgroundTasksKeyboardNavigation(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.width = 100

	// One finished task with two nested calls, like a completed run.
	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	task := u.agentTaskByToolCall("a1")
	task.status = subagents.StatusCompleted
	task.nested = []chat.ToolMessageItem{
		chat.NewToolMessageItem(u.com.Styles, "c1", message.ToolCall{ID: "n1", Name: "Bash", Input: `{"command":"ls"}`, Finished: true}, nil, false, "/tmp"),
		chat.NewToolMessageItem(u.com.Styles, "c1", message.ToolCall{ID: "n2", Name: "View", Input: `{"file_path":"a.go"}`, Finished: true}, nil, false, "/tmp"),
	}
	for _, nested := range task.nested {
		nested.(chat.Compactable).SetCompact(true)
	}

	// ctrl+b-style focus parks the cursor on the task row.
	u.focusTasks()
	require.Equal(t, uiFocusTasks, u.focus)
	require.Equal(t, 0, u.taskCursor)

	// Enter expands the task and descends onto its first call; enter
	// again opens that call's full view.
	u.enterTaskAtCursor()
	require.Equal(t, "a1", u.expandedTaskID)
	require.Equal(t, 0, u.taskSubCursor)
	u.enterTaskAtCursor()
	assert.False(t, task.nested[0].(interface{ IsCompact() bool }).IsCompact(), "the cursor's call renders its full view")
	assert.True(t, task.nested[1].(interface{ IsCompact() bool }).IsCompact())

	// Escape climbs out one level at a time: the call's full view, the
	// call cursor, the task, then focus back to the editor.
	u.ascendTaskAtCursor()
	assert.True(t, task.nested[0].(interface{ IsCompact() bool }).IsCompact(), "escape collapses the call to its one-liner")
	u.ascendTaskAtCursor()
	require.Equal(t, -1, u.taskSubCursor)
	u.ascendTaskAtCursor()
	require.Empty(t, u.expandedTaskID)

	// Re-enter and walk the calls: down to the last (stays put past it),
	// up climbs back to the task row.
	u.enterTaskAtCursor()
	require.Equal(t, 0, u.taskSubCursor)
	u.taskCursorDown()
	require.Equal(t, 1, u.taskSubCursor)
	u.taskCursorDown()
	require.Equal(t, 1, u.taskSubCursor, "no further down at the last entry")
	u.taskCursorUp()
	u.taskCursorUp()
	require.Equal(t, -1, u.taskSubCursor)

	// Esc-style leave returns the editor.
	u.ascendTaskAtCursor()
	u.ascendTaskAtCursor()
	require.Equal(t, uiFocusEditor, u.focus)
}

// TestSubagentWaitEntry covers the transcript's live wait entry: it
// appears with the running count while subagents are in flight, survives
// the assistant message flipping its tool calls to Finished (that only
// means the model finished emitting them), updates the count, and leaves
// once the last result lands.
func TestSubagentWaitEntry(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.width = 100

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	require.NotNil(t, u.chat.MessageItem(chat.SubagentWaitID), "a running dispatch adds the wait entry")
	wait := u.chat.MessageItem(chat.SubagentWaitID).(*chat.SubagentWaitItem)
	assert.Contains(t, ansi.Strip(wait.Render(80)), "Waiting for 1 subagent")

	// The emitted flag flipping must not reap the running subagent.
	finished := agentToolCall("a1")
	finished.Finished = true
	_ = u.upsertAgentTask(msg, finished)
	require.NotNil(t, u.agentTaskByToolCall("a1"), "an emitted-but-unresolved dispatch keeps running")
	require.NotNil(t, u.chat.MessageItem(chat.SubagentWaitID))

	// A second dispatch updates the count in place.
	_ = u.upsertAgentTask(msg, agentToolCall("a2"))
	assert.Contains(t, ansi.Strip(wait.Render(80)), "Waiting for 2 subagents")

	// Results reap both; the wait entry leaves with the last one.
	_ = u.resolveAgentTaskResult(message.ToolResult{ToolCallID: "a1", Name: "agent", Content: "ok"})
	require.NotNil(t, u.chat.MessageItem(chat.SubagentWaitID), "one subagent still running")
	assert.Contains(t, ansi.Strip(wait.Render(80)), "Waiting for 1 subagent")
	_ = u.resolveAgentTaskResult(message.ToolResult{ToolCallID: "a2", Name: "agent", Content: "ok"})
	assert.Nil(t, u.chat.MessageItem(chat.SubagentWaitID), "the wait entry leaves when all settle")
}

// TestStripCountsDispatchesOnly pins that the strip and the wait entry
// count only agent/research dispatches: the main agent's own tool calls
// (bash, view, ...) never create tasks, and child-session traffic (the
// subagent's own work, including its own dispatches) attaches as nested
// detail rather than new top-level rows.
func TestStripCountsDispatchesOnly(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}

	// One turn: two plain tool calls and one dispatch.
	msg := message.Message{ID: "m1", SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "b1", Name: "Bash", Input: `{"command":"ls"}`, Finished: true},
		message.ToolCall{ID: "v1", Name: "View", Input: `{"file_path":"a.go"}`, Finished: true},
	}}
	_ = u.updateSessionMessage(msg)
	require.Len(t, u.agentTasks, 0, "plain tool calls never count as subagents")
	require.Nil(t, u.chat.MessageItem(chat.SubagentWaitID))

	msg.Parts = append(msg.Parts, message.ToolCall{ID: "a1", Name: "agent", Input: `{"prompt":"dig"}`})
	_ = u.updateSessionMessage(msg)
	require.Len(t, u.agentTasks, 1)
	wait := u.chat.MessageItem(chat.SubagentWaitID).(*chat.SubagentWaitItem)
	assert.Contains(t, ansi.Strip(wait.Render(80)), "Waiting for 1 subagent")

	// Results for the plain tools must not reap the dispatch's task.
	toolMsg := message.Message{ID: "tm1", SessionID: "s1", Role: message.Tool, Parts: []message.ContentPart{
		message.ToolResult{ToolCallID: "b1", Name: "Bash", Content: "ok"},
		message.ToolResult{ToolCallID: "v1", Name: "View", Content: "ok"},
	}}
	_ = u.appendSessionMessage(toolMsg)
	require.Len(t, u.agentTasks, 1, "plain tool results never reap subagent tasks")
}
