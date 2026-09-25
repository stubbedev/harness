package model

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/subagents"
	"github.com/stubbedev/harness/internal/ui/chat"
	"github.com/stubbedev/harness/internal/workspace"
)

// shiftUp and shiftDown build the shifted arrows the terminal delivers.
func shiftUp() tea.KeyPressMsg   { return tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift} }
func shiftDown() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift} }

// TestShiftArrowsChainTranscriptTasksEditor pins the shift-arrow focus
// chain: transcript -> background tasks -> editor going down, editor
// -> tasks -> transcript going up. Each region hands focus to the next
// at its edge; without subagents the strip is skipped.
func TestShiftArrowsChainTranscriptTasksEditor(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.focus = uiFocusMain
	u.chat.SelectLast()
	require.Equal(t, u.chat.Len()-1, u.chat.Selected())

	// Down off the newest item without subagents: straight to the editor.
	_, _ = u.Update(shiftDown())
	require.Equal(t, uiFocusEditor, u.focus)

	// Up from the editor without subagents: back to the transcript.
	_, _ = u.Update(shiftUp())
	require.Equal(t, uiFocusMain, u.focus)
	require.Equal(t, u.chat.Len()-1, u.chat.Selected(), "returning to the transcript lands on the newest item")

	// A running subagent inserts the strip into the chain, both ways.
	_ = u.upsertAgentTask(&message.Message{ID: "m1", Role: message.Assistant}, agentToolCall("a1"))

	_, _ = u.Update(shiftDown())
	require.Equal(t, uiFocusTasks, u.focus, "down off the newest item selects the strip")

	_, _ = u.Update(shiftDown())
	require.Equal(t, uiFocusEditor, u.focus, "down off the strip's last row focuses the editor")

	_, _ = u.Update(shiftUp())
	require.Equal(t, uiFocusTasks, u.focus, "up from the editor selects the strip")

	_, _ = u.Update(shiftUp())
	require.Equal(t, uiFocusMain, u.focus, "up off the strip's first row returns to the transcript")
}

// TestTranscriptEntryGestures pins the landing rule for every way
// focus enters the transcript: the tab cycle restores the kept
// selection, an upward arrow arriving from below (from the editor or
// off the strip's top) always lands on the newest entry.
func TestTranscriptEntryGestures(t *testing.T) {
	t.Parallel()

	// An older item, kept on screen, that focus could return to.
	pinKeptSelection := func(t *testing.T) *UI {
		t.Helper()
		u := newFrameTestUI(t)
		u.chat.SetSelected(10)
		_ = u.chat.ScrollItemIntoView(10)
		require.True(t, u.chat.HasManualSelection())
		return u
	}
	toEditor := func(u *UI) {
		u.focus = uiFocusEditor
		u.textarea.Focus()
		u.chat.Blur()
	}
	withStrip := func(u *UI) {
		_ = u.upsertAgentTask(&message.Message{ID: "m1", Role: message.Assistant}, agentToolCall("a1"))
		u.focusTasks()
	}

	t.Run("tab from editor restores", func(t *testing.T) {
		t.Parallel()
		u := pinKeptSelection(t)
		toEditor(u)
		_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		require.Equal(t, uiFocusMain, u.focus)
		require.Equal(t, 10, u.chat.Selected())
	})

	t.Run("shift+tab from editor restores", func(t *testing.T) {
		t.Parallel()
		u := pinKeptSelection(t)
		toEditor(u)
		_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		require.Equal(t, uiFocusMain, u.focus)
		require.Equal(t, 10, u.chat.Selected())
	})

	t.Run("shift+up from editor lands on newest", func(t *testing.T) {
		t.Parallel()
		u := pinKeptSelection(t)
		toEditor(u)
		_, _ = u.Update(shiftUp())
		require.Equal(t, uiFocusMain, u.focus)
		require.Equal(t, u.chat.Len()-1, u.chat.Selected())
		require.False(t, u.chat.HasManualSelection())
	})

	t.Run("shift+tab from strip restores", func(t *testing.T) {
		t.Parallel()
		u := pinKeptSelection(t)
		withStrip(u)
		_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		require.Equal(t, uiFocusMain, u.focus)
		require.Equal(t, 10, u.chat.Selected())
	})

	t.Run("shift+up off strip top lands on newest", func(t *testing.T) {
		t.Parallel()
		u := pinKeptSelection(t)
		withStrip(u)
		_, _ = u.Update(shiftUp())
		require.Equal(t, uiFocusMain, u.focus)
		require.Equal(t, u.chat.Len()-1, u.chat.Selected())
	})
}

// TestPlainArrowsStopAtStripEdges pins that the unshifted arrows keep
// their meaning inside the strip: they move the cursor and stop at the
// edges instead of handing focus off.
func TestPlainArrowsStopAtStripEdges(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	_ = u.upsertAgentTask(&message.Message{ID: "m1", Role: message.Assistant}, agentToolCall("a1"))
	u.focusTasks()

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Equal(t, uiFocusTasks, u.focus, "plain up at the top of the strip stays in the strip")
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	require.Equal(t, uiFocusTasks, u.focus, "plain down at the bottom of the strip stays in the strip")
}

// TestShiftUpLetterAliasStaysTypeable pins that only the arrow form of
// the up-one-item binding leaves the editor: its letter alias must
// insert text instead of moving focus.
func TestShiftUpLetterAliasStaysTypeable(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.focus = uiFocusEditor
	u.textarea.Focus()
	u.chat.Blur()

	_, _ = u.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModShift, Text: "K"})
	require.Equal(t, uiFocusEditor, u.focus, "the K alias must not move focus from the editor")
	require.Equal(t, "K", u.textarea.Value(), "the K alias must stay typeable")
}

// agentToolCall builds the tool call a parent-session agent dispatch
// produces. Blocking keeps the inline-result semantics these strip tests
// were written against: the tool result is the sub-agent's final output.
func agentToolCall(id string) message.ToolCall {
	return message.ToolCall{
		ID:       id,
		Name:     "agent",
		Input:    `{"subagent_type":"researcher","prompt":"dig into the git history","blocking":true}`,
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

	// Expanding via the recorded row shows the task's detail but not the
	// dispatch prompt — that is context for the model, not the
	// transcript; clicking again collapses it.
	assert.True(t, u.handleTaskClick(2, 0))
	assert.Equal(t, "a1", u.expandedTaskID)
	u.tasksAreaHeight()
	assert.NotContains(t, ansi.Strip(u.tasksView), "dig into the git history")
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

// TestSubagentNoteBumpsTaskCount pins the report-back path: a
// background sub-agent's send_message content never renders in the
// transcript; it counts up on its task's strip row instead.
func TestSubagentNoteBumpsTaskCount(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.width = 100

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	bg := agentToolCall("a1")
	bg.Input = `{"subagent_type":"researcher","prompt":"dig"}`
	_ = u.upsertAgentTask(msg, bg)
	task := u.agentTaskByToolCall("a1")
	require.NotNil(t, task)
	task.childSessionID = "child-1"

	before := u.chat.Len()
	note := message.Message{ID: "n1", SessionID: "s1", Role: message.User, Parts: []message.ContentPart{
		message.SubagentNote{AgentName: "researcher", Handle: "bg-1", ChildSessionID: "child-1", Text: "halfway there"},
	}}
	_ = u.appendSessionMessage(note)
	assert.Equal(t, 1, task.messages, "the report-back counts on its task")
	assert.Equal(t, before, u.chat.Len(), "the note renders nowhere in the transcript")

	// A second note for another child bumps only that child's task.
	otherCall := agentToolCall("a2")
	otherCall.Input = `{"prompt":"more"}`
	_ = u.upsertAgentTask(msg, otherCall)
	other := u.agentTaskByToolCall("a2")
	require.NotNil(t, other)
	other.childSessionID = "child-2"
	_ = u.appendSessionMessage(message.Message{ID: "n2", SessionID: "s1", Role: message.User, Parts: []message.ContentPart{
		message.SubagentNote{AgentName: "researcher", Handle: "bg-2", ChildSessionID: "child-2", Text: "done"},
	}})
	assert.Equal(t, 1, other.messages)
	assert.Equal(t, 1, task.messages)

	u.tasksAreaHeight()
	assert.Contains(t, ansi.Strip(u.tasksView), "1 msg")
}

// TestLoadAgentTasksCountsNotes pins the reload path: report-backs
// persisted before the reload count onto the rebuilt tasks.
func TestLoadAgentTasksCountsNotes(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}

	child := u.childSessionIDFor("m1", "a1")
	require.NotEmpty(t, child)
	msgs := []*message.Message{
		{ID: "m1", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "a1", Name: "agent", Input: `{"prompt":"dig"}`, Finished: true},
		}},
		{ID: "n1", Role: message.User, Parts: []message.ContentPart{
			message.SubagentNote{AgentName: "researcher", Handle: "bg-1", ChildSessionID: child, Text: "halfway there"},
		}},
		{ID: "n2", Role: message.User, Parts: []message.ContentPart{
			message.SubagentNote{AgentName: "researcher", Handle: "bg-1", ChildSessionID: child, Text: "done digging"},
		}},
	}
	u.loadAgentTasks(msgs, nil)

	task := u.agentTaskByToolCall("a1")
	require.NotNil(t, task)
	assert.Equal(t, 2, task.messages)
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
		chat.NewToolMessageItem(u.com.Styles, "c1", message.ToolCall{ID: "n1", Name: "Bash", Input: `{"command":"ls"}`, Finished: true}, nil, false),
		chat.NewToolMessageItem(u.com.Styles, "c1", message.ToolCall{ID: "n2", Name: "View", Input: `{"file_path":"a.go"}`, Finished: true}, nil, false),
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

	// Escape climbs out: the call's full view collapses to its one-liner,
	// the next escape collapses the task, and with nothing left to leave,
	// focus returns to the editor.
	u.ascendTaskAtCursor()
	assert.True(t, task.nested[0].(interface{ IsCompact() bool }).IsCompact(), "escape collapses the call to its one-liner")
	u.ascendTaskAtCursor()
	require.Empty(t, u.expandedTaskID)
	require.Equal(t, -1, u.taskSubCursor)
	u.ascendTaskAtCursor()
	require.Equal(t, uiFocusEditor, u.focus)

	// A selection that is already a one-liner collapses the task in a
	// single escape: clearing the selection marker alone would waste a
	// keypress.
	u.focusTasks()
	u.enterTaskAtCursor()
	require.Equal(t, 0, u.taskSubCursor)
	u.ascendTaskAtCursor()
	require.Empty(t, u.expandedTaskID)
	require.Equal(t, -1, u.taskSubCursor)

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

// TestRunningDispatchesLiveOnlyInTheStrip pins that a dispatch shows up
// as a strip row and nowhere else: no companion entry is appended to the
// transcript, so the only thing to look at while subagents run is the
// list of the subagents themselves, each expandable to its tool calls.
func TestRunningDispatchesLiveOnlyInTheStrip(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.width = 100

	before := u.chat.Len()
	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	_ = u.upsertAgentTask(msg, agentToolCall("a2"))
	require.Len(t, u.agentTasks, 2, "each dispatch is a strip row")
	require.True(t, u.tasksSpinning())
	assert.Equal(t, before, u.chat.Len(), "dispatches add nothing to the transcript")

	// The emitted flag flipping must not reap the running subagent.
	finished := agentToolCall("a1")
	finished.Finished = true
	_ = u.upsertAgentTask(msg, finished)
	require.NotNil(t, u.agentTaskByToolCall("a1"), "an emitted-but-unresolved dispatch keeps running")

	_ = u.resolveAgentTaskResult(message.ToolResult{ToolCallID: "a1", Name: "agent", Content: "ok"})
	_ = u.resolveAgentTaskResult(message.ToolResult{ToolCallID: "a2", Name: "agent", Content: "ok"})
	assert.Empty(t, u.agentTasks)
	assert.False(t, u.tasksSpinning())
	assert.Equal(t, before, u.chat.Len())
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

	msg.Parts = append(msg.Parts, message.ToolCall{ID: "a1", Name: "agent", Input: `{"prompt":"dig","blocking":true}`})
	_ = u.updateSessionMessage(msg)
	require.Len(t, u.agentTasks, 1)

	// Results for the plain tools must not reap the dispatch's task.
	toolMsg := message.Message{ID: "tm1", SessionID: "s1", Role: message.Tool, Parts: []message.ContentPart{
		message.ToolResult{ToolCallID: "b1", Name: "Bash", Content: "ok"},
		message.ToolResult{ToolCallID: "v1", Name: "View", Content: "ok"},
	}}
	_ = u.appendSessionMessage(toolMsg)
	require.Len(t, u.agentTasks, 1, "plain tool results never reap subagent tasks")
}

// TestBackgroundDispatchStaysVisible pins that a background dispatch is
// not settled by its immediate handle tool result: the subagent keeps
// running, and only the runtime's terminal event (or reconciliation
// against the running list) removes it from the strip.
func TestBackgroundDispatchStaysVisible(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.width = 100
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	bg := agentToolCall("a1")
	bg.Input = `{"subagent_type":"researcher","prompt":"dig"}`
	_ = u.upsertAgentTask(msg, bg)

	task := u.agentTaskByToolCall("a1")
	require.NotNil(t, task)
	require.True(t, task.background)

	// The handle result must not complete the task.
	assert.True(t, u.resolveAgentTaskResult(message.ToolResult{
		ToolCallID: "a1", Name: "agent", Content: "Started background agent",
	}))
	require.NotNil(t, u.agentTaskByToolCall("a1"), "a background dispatch outlives its handle result")
	assert.True(t, u.tasksSpinning())

	// The terminal runtime event reaps it.
	u.applyRunningSubagentInfo(childSessionInfo{
		ChildSessionID: task.childSessionID,
		Status:         subagents.StatusCompleted,
	})
	assert.Empty(t, u.agentTasks, "a finished background task leaves the strip")
}

// TestBackgroundDispatchReconcileAgainstRunningList pins the
// reconciliation path: a running background task whose child session is
// absent from the authoritative running list is settled, while one still
// present stays.
func TestBackgroundDispatchReconcileAgainstRunningList(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	for _, id := range []string{"a1", "a2"} {
		bg := agentToolCall(id)
		bg.Input = `{"prompt":"dig"}`
		_ = u.upsertAgentTask(msg, bg)
	}
	t1 := u.agentTaskByToolCall("a1")
	t2 := u.agentTaskByToolCall("a2")
	require.NotNil(t, t1)
	require.NotNil(t, t2)
	t1.childSessionID = "child-1"
	t2.childSessionID = "child-2"
	// Aged past the reconcile grace: a fetch that raced the runtime
	// Register of a just-dispatched task must not settle it, so only a
	// task that has been spinning a while may be reaped this way.
	t2.startedAt = time.Now().Add(-2 * tasksReconcileGrace)

	// child-1 is still running; child-2 is not.
	u.reconcileBackgroundTasks([]workspace.RunningSubagentInfo{
		{ChildSessionID: "child-1"},
	})
	assert.Nil(t, u.agentTaskByToolCall("a2"), "a background task missing from the running list is reaped")
	assert.NotNil(t, u.agentTaskByToolCall("a1"), "a background task still running stays")

	// A task younger than the grace is left alone even when absent from
	// the list: its Register may simply not have landed yet.
	bg := agentToolCall("a3")
	bg.Input = `{"prompt":"dig"}`
	_ = u.upsertAgentTask(msg, bg)
	t3 := u.agentTaskByToolCall("a3")
	require.NotNil(t, t3)
	t3.childSessionID = "child-3"
	u.reconcileBackgroundTasks([]workspace.RunningSubagentInfo{
		{ChildSessionID: "child-1"},
	})
	assert.NotNil(t, u.agentTaskByToolCall("a3"), "a task inside the reconcile grace is not reaped")
}

// TestStripDoesNotResurrectFinishedDispatch pins the ordering that ghosts
// the strip: fantasy delivers a step's tool results before the
// step-finish update of the assistant message, so the dispatch's task is
// reaped and then upsertAgentTask sees the same tool call again. The
// second update must not re-register the finished dispatch.
func TestStripDoesNotResurrectFinishedDispatch(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}

	msg := message.Message{ID: "m1", SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "a1", Name: "agent", Input: `{"prompt":"dig","blocking":true}`, Finished: true},
	}}
	_ = u.updateSessionMessage(msg)
	require.Len(t, u.agentTasks, 1)

	resultMsg := message.Message{ID: "tm1", SessionID: "s1", Role: message.Tool, Parts: []message.ContentPart{
		message.ToolResult{ToolCallID: "a1", Name: "agent", Content: "found it"},
	}}
	_ = u.appendSessionMessage(resultMsg)
	require.Empty(t, u.agentTasks, "the dispatch's result reaps its task")

	// The step-finish update re-delivers the same assistant message,
	// finish part included, after the result landed.
	msg.Parts = append(msg.Parts, message.Finish{Reason: message.FinishReasonToolUse})
	_ = u.updateSessionMessage(msg)
	assert.Empty(t, u.agentTasks, "a post-result update must not resurrect the task")
	assert.False(t, u.tasksSpinning())

	// The same guard holds when the reap came from a terminal runtime
	// status instead of the tool result.
	_ = u.updateSessionMessage(message.Message{ID: "m2", SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "a2", Name: "agent", Input: `{"prompt":"more"}`, Finished: true},
	}})
	require.Len(t, u.agentTasks, 1)
	u.reapAgentTask("a2")
	require.Empty(t, u.agentTasks)
	_ = u.updateSessionMessage(message.Message{ID: "m2", SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "a2", Name: "agent", Input: `{"prompt":"more"}`, Finished: true},
		message.Finish{Reason: message.FinishReasonToolUse},
	}})
	assert.Empty(t, u.agentTasks, "a runtime-reaped dispatch stays gone too")

	// A fresh dispatch in a later step still registers normally.
	_ = u.updateSessionMessage(message.Message{ID: "m3", SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "a3", Name: "agent", Input: `{"prompt":"next"}`, Finished: true},
	}})
	assert.Len(t, u.agentTasks, 1, "unrelated later dispatches still register")
}

// TestAgentWaitCallGetsNoStripRow pins that the waiting form of the
// agent tool -- a call with no prompt, which dispatches nothing and
// blocks until the background subagents already running report back --
// never gets a strip row of its own. It used to raise a second spinner
// titled "subagent" (the fallback title, since a wait names no
// subagent_type) alongside the row for the agent it was waiting on.
func TestAgentWaitCallGetsNoStripRow(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.width = 100

	msg := &message.Message{ID: "m1", Role: message.Assistant}

	// A background dispatch takes its row.
	_ = u.upsertAgentTask(msg, message.ToolCall{
		ID: "a1", Name: "agent",
		Input: `{"subagent_type":"fast","prompt":"read the diff"}`,
	})
	// The wait that collects it does not.
	_ = u.upsertAgentTask(msg, message.ToolCall{
		ID: "a2", Name: "agent", Input: `{}`, Finished: true,
	})

	require.Len(t, u.agentTasks, 1)
	assert.Equal(t, "fast", u.agentTasks[0].name)
	assert.Nil(t, u.agentTaskByToolCall("a2"))

	u.tasksAreaHeight()
	assert.NotContains(t, ansi.Strip(u.tasksView), subagentDisplayName)
}

// TestLoadAgentTasksSkipsWaitCalls pins the same policy on the reload
// path: a session restored from history must not resurrect the ghost row
// for a wait call that was in flight.
func TestLoadAgentTasksSkipsWaitCalls(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}

	msgs := []*message.Message{{
		ID:   "m1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "a1", Name: "agent", Input: `{"subagent_type":"fast","prompt":"read the diff"}`},
			message.ToolCall{ID: "a2", Name: "agent", Input: `{"timeout_seconds":60}`},
		},
	}}
	u.loadAgentTasks(msgs, map[string]message.ToolResult{})

	require.Len(t, u.agentTasks, 1)
	assert.Equal(t, "a1", u.agentTasks[0].toolCallID)
}
