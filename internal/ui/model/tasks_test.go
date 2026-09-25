package model

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/subagents"
	"github.com/stubbedev/harness/internal/ui/chat"
	"github.com/stubbedev/harness/internal/ui/util"
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
	assert.Equal(t, "dig into the git history", task.description, "the prompt excerpt is the fallback description")
	assert.True(t, u.tasksSpinning(), "a fresh dispatch with no result is running")

	// The strip renders one row per task: the Agent word plus the
	// description, no type and no spinner glyph.
	u.tasksAreaHeight()
	require.NotEmpty(t, u.tasksView)
	out := ansi.Strip(u.tasksView)
	assert.Contains(t, out, "Agent")
	assert.Contains(t, out, "dig into the git history")
	assert.NotContains(t, out, "researcher", "the subagent type is not shown")
	require.Len(t, u.taskRows, 1)

	// The generated title replaces the excerpt once the child session's
	// title lands.
	assert.True(t, u.applyTaskTitle(task.childSessionID, "Dig into git history for the culprit"))
	u.tasksAreaHeight()
	assert.Contains(t, ansi.Strip(u.tasksView), "Dig into git history for the culprit")
	assert.False(t, u.applyTaskTitle(task.childSessionID, "New Agent Session"), "the placeholder title is ignored")

	// Clicking a row activates it: the transcript switches to the
	// agent's session once the fetch lands.
	clicked, clickCmd := u.handleTaskClick(2, 0)
	assert.True(t, clicked)
	assert.NotNil(t, clickCmd)
	assert.Equal(t, task.childSessionID, u.agentView.requested)

	// The final result completes the task — and reaps it: the strip is
	// a viewer for ongoing work only.
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

// TestSubagentNoteNeverRenders pins that a background sub-agent's
// send_message content is LLM-to-LLM mail: it never renders in the
// transcript, no matter which session is being viewed.
func TestSubagentNoteNeverRenders(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.width = 100

	note := message.Message{ID: "n1", SessionID: "s1", Role: message.User, Parts: []message.ContentPart{
		message.SubagentNote{AgentName: "researcher", Handle: "bg-1", ChildSessionID: "child-1", Text: "halfway there"},
	}}
	before := u.chat.Len()
	_ = u.appendSessionMessage(note)
	assert.Equal(t, before, u.chat.Len(), "the note renders nowhere in the transcript")
}

// TestLoadAgentTasksUsesPromptExcerpt pins the reload path: a rebuilt
// task's description falls back to the dispatch-prompt excerpt until a
// generated title arrives.
func TestLoadAgentTasksUsesPromptExcerpt(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}

	child := u.childSessionIDFor("m1", "a1")
	require.NotEmpty(t, child)
	msgs := []*message.Message{
		{ID: "m1", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "a1", Name: "agent", Input: `{"prompt":"dig into the parser"}`, Finished: true},
		}},
	}
	u.loadAgentTasks(msgs, nil)

	task := u.agentTaskByToolCall("a1")
	require.NotNil(t, task)
	assert.Equal(t, "dig into the parser", task.description)
}

func TestBackgroundTasksWindowScrolls(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.width = 100

	// Four running tasks: only three rows render, and the window
	// follows the cursor.
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		bg := agentToolCall(id)
		bg.Input = fmt.Sprintf(`{"prompt":"dig %s"}`, id)
		_ = u.upsertAgentTask(&message.Message{ID: "m", Role: message.Assistant}, bg)
	}
	require.Len(t, u.agentTasks, 4)

	u.tasksAreaHeight()
	out := ansi.Strip(u.tasksView)
	// The window renders at most three task rows.
	assert.LessOrEqual(t, strings.Count(out, "Agent"), 3)
	assert.LessOrEqual(t, strings.Count(out, "\n")+1, 3)

	// Move the cursor past the window end: the window scrolls with it.
	u.taskCursor = 3
	u.tasksAreaHeight()
	assert.Contains(t, ansi.Strip(u.tasksView), "dig a4", "the window follows the cursor onto the last task")
	assert.NotContains(t, ansi.Strip(u.tasksView), "dig a1")

	// Reaping the task under the cursor clamps the cursor back in range.
	u.reapAgentTask("a4")
	u.clampTaskCursor()
	assert.Equal(t, 2, u.taskCursor)
}

// activateAgentView is the test-side act of entering an agent's
// transcript: focus the strip, press enter, run the fetch the switch
// returns, and apply it.
func activateAgentView(t *testing.T, u *UI) {
	t.Helper()
	u.focusTasks()
	cmd := u.activateTaskAtCursor()
	require.NotNil(t, cmd, "activating a row returns the transcript fetch")
	applyAgentTranscript(t, u, cmd())
}

// applyAgentTranscript runs a fetched transcript through the handler,
// as Update would, and follows the switch's single retrace (which folds
// in events that raced the first snapshot and must not retrace again).
func applyAgentTranscript(t *testing.T, u *UI, msg tea.Msg) {
	t.Helper()
	tr, ok := msg.(agentTranscriptMsg)
	require.True(t, ok, "the view switch fetches the transcript")
	retrace := u.handleAgentTranscriptMsg(tr)
	if retrace == nil {
		return
	}
	tr2, ok := retrace().(agentTranscriptMsg)
	require.True(t, ok)
	require.True(t, tr2.retraced, "the retrace is marked so it stops after one round")
	assert.Nil(t, u.handleAgentTranscriptMsg(tr2))
}

// TestEnterSwitchesToAgentAndMainBack pins the strip's core gesture:
// enter on an agent row shows that agent's session in the transcript
// with a Main Agent row pinned above, and enter (or escape) on Main
// returns to the main session.
func TestEnterSwitchesToAgentAndMainBack(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}
	u.session = &session.Session{ID: "s1"}

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	task := u.agentTaskByToolCall("a1")
	require.Equal(t, "agent-tool-m1-a1", task.childSessionID)

	// Enter on the agent row: the transcript shows its session, focus
	// moves to the transcript, and Main Agent appears in the strip.
	activateAgentView(t, u)
	require.Equal(t, "agent-tool-m1-a1", u.agentView.shown)
	require.Equal(t, uiFocusMain, u.focus, "activating a row moves focus to the transcript")
	require.Equal(t, 2, u.taskRowCount(), "the Main row is pinned above the agent row")
	u.tasksAreaHeight()
	out := ansi.Strip(u.tasksView)
	assert.Contains(t, out, "Main Agent")
	assert.Contains(t, out, "Agent")

	// Enter on the Main row (row 0) returns to the main session and
	// drops the pinned row. The main reload also rebuilds the strip from
	// the main session's history; the stub returns none, so the strip
	// empties here.
	u.focusTasks()
	u.taskCursor = 0
	cmd := u.activateTaskAtCursor()
	require.NotNil(t, cmd)
	require.Empty(t, u.agentView.requested, "main is requested")
	applyAgentTranscript(t, u, cmd())
	require.Empty(t, u.agentView.shown)
	require.Equal(t, 0, u.taskRowCount(), "the reload rebuilds the strip from the main session's history")
}

// TestEscapeFromAgentViewReturnsToMain pins escape as the fast way out
// of an agent's transcript.
func TestEscapeFromAgentViewReturnsToMain(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}
	u.session = &session.Session{ID: "s1"}
	u.keyMap = DefaultKeyMap()

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	activateAgentView(t, u)
	require.Equal(t, "agent-tool-m1-a1", u.agentView.shown)

	u.focusTasks()
	consumed, cmd := u.handleTaskKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.True(t, consumed)
	require.NotNil(t, cmd)
	applyAgentTranscript(t, u, cmd())
	assert.Empty(t, u.agentView.shown, "escape returns to the main session")
}

// TestActivateUnstartedAgentReports pins the edge: a dispatch that is
// still streaming has no child session yet, so activating its row
// reports instead of switching to an empty transcript.
func TestActivateUnstartedAgentReports(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	task := u.agentTaskByToolCall("a1")
	require.NotNil(t, task)
	task.childSessionID = ""

	u.focusTasks()
	cmd := u.activateTaskAtCursor()
	require.NotNil(t, cmd, "activating an unstarted agent reports instead of switching")
	report := cmd()
	info, ok := report.(util.InfoMsg)
	require.True(t, ok, "the report is an info message")
	assert.Contains(t, info.Msg, "not started")
	assert.Empty(t, u.agentView.shown, "nothing to switch to")
}

// TestSessionEventTitlesTaskRow pins the live path: the child session's
// generated title arrives as a session event and becomes the row's
// description, replacing the prompt excerpt.
func TestSessionEventTitlesTaskRow(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}
	u.session = &session.Session{ID: "s1"}

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	task := u.agentTaskByToolCall("a1")
	require.NotNil(t, task)
	require.False(t, task.titled)

	_, _ = u.Update(pubsub.Event[session.Session]{
		Type:    pubsub.UpdatedEvent,
		Payload: session.Session{ID: task.childSessionID, Title: "Investigating parser drift"},
	})
	assert.Equal(t, "Investigating parser drift", task.description)
	assert.True(t, task.titled)

	// A later dispatch-prompt update (the step-finish message update)
	// must not overwrite the title with the excerpt again.
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	assert.Equal(t, "Investigating parser drift", task.description)
}

// TestViewedAgentIgnoresMainTraffic pins the one-session invariant of
// the chat: while an agent's session is viewed, main-session message
// events do not paint into the transcript (the main reload on
// switch-back picks them up instead).
func TestViewedAgentIgnoresMainTraffic(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.session = &session.Session{ID: "s1"}

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	activateAgentView(t, u)
	require.Equal(t, "agent-tool-m1-a1", u.agentView.shown)

	before := u.chat.Len()
	_, _ = u.Update(pubsub.Event[message.Message]{
		Type: pubsub.CreatedEvent,
		Payload: message.Message{ID: "main-1", SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "main session reply"},
		}},
	})
	assert.Equal(t, before, u.chat.Len(), "main traffic does not paint into the agent's transcript")

	// The viewed agent's own traffic does render.
	_, _ = u.Update(pubsub.Event[message.Message]{
		Type: pubsub.CreatedEvent,
		Payload: message.Message{ID: "child-1", SessionID: "agent-tool-m1-a1", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "agent reply"},
		}},
	})
	assert.Equal(t, before+1, u.chat.Len(), "the viewed agent's messages render")
}

// TestSteerWhileViewingSendsToAgent pins the editor's routing: with an
// agent's session viewed, a submitted prompt goes to that agent, not to
// the main session.
func TestSteerWhileViewingSendsToAgent(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	ws := &testWorkspace{cfg: &config.Config{}}
	u.com.Workspace = ws
	u.session = &session.Session{ID: "s1"}

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	activateAgentView(t, u)
	require.Equal(t, "agent-tool-m1-a1", u.agentView.shown)

	cmd := u.steerAgent("focus on the parser")
	require.NotNil(t, cmd)
	assert.Nil(t, cmd(), "a successful steer reports nothing")
	assert.Equal(t, "agent-tool-m1-a1", ws.steeredSession)
	assert.Equal(t, "focus on the parser", ws.steeredText)
}

// TestViewSwitchIsAtomicUntilPopulated pins the no-blank-swap: while the
// agent transcript is still being fetched, the old view keeps rendering
// and keeps receiving its own session's traffic; only the completed
// fetch swaps the view over.
func TestViewSwitchIsAtomicUntilPopulated(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.session = &session.Session{ID: "s1"}

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	u.focusTasks()
	cmd := u.activateTaskAtCursor()
	require.NotNil(t, cmd)

	// Mid-switch: main is still shown and still paints; the agent's
	// traffic waits for the swap.
	require.Empty(t, u.agentView.shown)
	require.Equal(t, "agent-tool-m1-a1", u.agentView.requested)
	_, _ = u.Update(pubsub.Event[message.Message]{
		Type: pubsub.CreatedEvent,
		Payload: message.Message{ID: "main-2", SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "still main"},
		}},
	})
	mainItem := u.chat.MessageItem("main-2")
	require.NotNil(t, mainItem, "the old view keeps painting until the swap")

	// The fetch lands: the agent's transcript takes over, and the
	// retrace folds in anything that raced the first snapshot.
	tr := cmd().(agentTranscriptMsg)
	retrace := u.handleAgentTranscriptMsg(tr)
	require.Equal(t, "agent-tool-m1-a1", u.agentView.shown)
	require.NotNil(t, retrace, "a child swap retraces once")
	_, _ = u.Update(pubsub.Event[message.Message]{
		Type: pubsub.CreatedEvent,
		Payload: message.Message{ID: "child-race", SessionID: "agent-tool-m1-a1", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "steer"},
		}},
	})
	tr2 := retrace().(agentTranscriptMsg)
	require.True(t, tr2.retraced)
	assert.Nil(t, u.handleAgentTranscriptMsg(tr2), "the retrace does not retrace again")
}

// TestWaitingCallRendersAgentsPins pins the wait form's transcript
// rendering: the agent tool's no-prompt call renders as "Waiting for N
// agents" with a live count, and its result carries the collected
// reports.
func TestWaitingCallRendersAgentsPins(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}
	u.session = &session.Session{ID: "s1"}

	for _, id := range []string{"a1", "a2"} {
		_ = u.upsertAgentTask(&message.Message{ID: "m1", Role: message.Assistant}, agentToolCall(id))
	}
	require.Equal(t, 2, u.runningAgentCount())

	wait := message.Message{ID: "m2", SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "w1", Name: "agent", Input: `{}`},
	}}
	_ = u.updateSessionMessage(wait)

	item := u.chat.MessageItem("w1")
	require.NotNil(t, item, "the wait call renders in the transcript")
	out := ansi.Strip(waitCallRender(t, item, 80))
	assert.Contains(t, out, "Waiting for 2 agents")

	// The collected reports land as the call's result; the assistant
	// update also flips the call's Finished flag, exactly as the real
	// step-finish update does.
	_ = u.appendSessionMessage(message.Message{ID: "tm2", SessionID: "s1", Role: message.Tool, Parts: []message.ContentPart{
		message.ToolResult{ToolCallID: "w1", Name: "agent", Content: "All waited agents finished."},
	}})
	wait.Parts[0] = message.ToolCall{ID: "w1", Name: "agent", Input: `{}`, Finished: true}
	_ = u.updateSessionMessage(wait)
	out = ansi.Strip(waitCallRender(t, u.chat.MessageItem("w1"), 80))
	assert.Contains(t, out, "Waited for agents")
	assert.Contains(t, out, "All waited agents finished.")
}

// waitCallRender renders the tool call inside whatever container holds
// it (a singleton group once folded), at the body width.
func waitCallRender(t *testing.T, item chat.MessageItem, width int) string {
	t.Helper()
	group, ok := item.(interface {
		ChildTool(string) chat.ToolMessageItem
	})
	require.True(t, ok, "tool calls fold into groups")
	tool := group.ChildTool("w1")
	require.NotNil(t, tool)
	return tool.RawRender(width)
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
// reconciliation path: a dispatched background task whose child session
// is absent from the authoritative running list is settled, while one
// still present stays. A dispatch that has not returned its handle yet
// is never settled this way: during a wide fan-out the strip rows
// register while the model is still streaming the message, minutes
// before fantasy executes the calls and the runtime registers the
// children, so absence from the list is expected, not terminal.
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
	// The handle results mark both dispatches as started.
	assert.True(t, u.resolveAgentTaskResult(message.ToolResult{
		ToolCallID: "a1", Name: "agent", Content: "Started background agent",
	}))
	assert.True(t, u.resolveAgentTaskResult(message.ToolResult{
		ToolCallID: "a2", Name: "agent", Content: "Started background agent",
	}))

	// child-1 is still running; child-2 is not.
	u.reconcileBackgroundTasks([]workspace.RunningSubagentInfo{
		{ChildSessionID: "child-1"},
	})
	assert.Nil(t, u.agentTaskByToolCall("a2"), "a dispatched background task missing from the running list is reaped")
	assert.NotNil(t, u.agentTaskByToolCall("a1"), "a background task still running stays")

	// A dispatch still streaming its input (no handle result yet) is
	// left alone even when absent from the list and long past the
	// grace: its child session does not exist until fantasy executes
	// the call, which for a fan-out is only after the whole message has
	// finished streaming.
	bg := agentToolCall("a3")
	bg.Input = `{"prompt":"dig"}`
	_ = u.upsertAgentTask(msg, bg)
	t3 := u.agentTaskByToolCall("a3")
	require.NotNil(t, t3)
	t3.childSessionID = "child-3"
	t3.startedAt = time.Now().Add(-2 * tasksReconcileGrace)
	u.reconcileBackgroundTasks([]workspace.RunningSubagentInfo{
		{ChildSessionID: "child-1"},
	})
	assert.NotNil(t, u.agentTaskByToolCall("a3"), "a streaming dispatch must not be settled for absence from the running list")

	// Once its handle result lands, the same absence settles it.
	assert.True(t, u.resolveAgentTaskResult(message.ToolResult{
		ToolCallID: "a3", Name: "agent", Content: "Started background agent",
	}))
	u.reconcileBackgroundTasks(nil)
	assert.Nil(t, u.agentTaskByToolCall("a3"), "a dispatched task absent from the running list is reaped")
}

// TestStreamingFanOutKeepsAllRows pins the wide fan-out: seven
// dispatches stream in one message and register rows as their inputs
// complete, but the child sessions do not exist until the message
// finishes. Reconcile fetches during that window used to settle every
// row older than the grace, leaving one spinner for a seven-agent
// fan-out; none of them may be reaped before their handles return.
func TestStreamingFanOutKeepsAllRows(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}

	msg := message.Message{ID: "m1", SessionID: "s1", Role: message.Assistant}
	for i, id := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"} {
		msg.Parts = append(msg.Parts, message.ToolCall{
			ID: id, Name: "agent",
			Input: fmt.Sprintf(`{"subagent_type":"task","prompt":"survey %d"}`, i),
		})
		_ = u.updateSessionMessage(msg)
	}
	require.Len(t, u.agentTasks, 7, "each streaming dispatch registers its row")

	// Reconcile runs while the model is still streaming: the running
	// list is empty and the first rows are far past the grace.
	for _, task := range u.agentTasks {
		task.startedAt = time.Now().Add(-2 * tasksReconcileGrace)
	}
	u.reconcileBackgroundTasks(nil)
	assert.Len(t, u.agentTasks, 7, "streaming dispatches survive reconcile before their handles return")

	// The message finishes; all seven start and their handles return.
	for _, id := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"} {
		assert.True(t, u.resolveAgentTaskResult(message.ToolResult{
			ToolCallID: id, Name: "agent", Content: "Started background agent",
		}))
	}
	require.Len(t, u.agentTasks, 7, "a handle keeps its task spinning")

	// Terminal runtime events then settle them one by one.
	for i, task := range append([]*agentTask(nil), u.agentTasks...) {
		u.applyRunningSubagentInfo(childSessionInfo{
			ChildSessionID: task.childSessionID,
			Status:         subagents.StatusCompleted,
		})
		assert.Len(t, u.agentTasks, 6-i)
	}
}

// TestFailedBackgroundDispatchSettles pins that an error handle result
// settles a background dispatch: the run never started (unknown type,
// build or session failure), so it never registers with the runtime and
// no terminal event would ever arrive.
func TestFailedBackgroundDispatchSettles(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	bg := agentToolCall("a1")
	bg.Input = `{"subagent_type":"nope","prompt":"dig"}`
	_ = u.upsertAgentTask(msg, bg)
	require.NotNil(t, u.agentTaskByToolCall("a1"))

	assert.True(t, u.resolveAgentTaskResult(message.ToolResult{
		ToolCallID: "a1", Name: "agent", IsError: true, Content: "unknown subagent type",
	}))
	assert.Empty(t, u.agentTasks, "an error handle settles the background dispatch")
	assert.False(t, u.tasksSpinning())
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
// never gets a strip row of its own. It would double every row it
// waits on with a descriptionless Agent row.
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
	assert.Equal(t, "read the diff", u.agentTasks[0].description)
	assert.Nil(t, u.agentTaskByToolCall("a2"))

	u.tasksAreaHeight()
	assert.NotContains(t, ansi.Strip(u.tasksView), "subagent")
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
