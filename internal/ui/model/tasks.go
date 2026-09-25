package model

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/subagents"
	"github.com/stubbedev/harness/internal/ui/chat"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/workspace"
)

// agentTask tracks one subagent run for the background tasks strip.
// Subagents do not render in the transcript; this is their holder.
type agentTask struct {
	toolCallID       string
	name             string
	color            string
	model            string
	status           string
	prompt           string
	result           *message.ToolResult
	nested           []chat.ToolMessageItem
	startedAt        time.Time
	promptTokens     int64
	completionTokens int64
	// messages counts report-backs the sub-agent sent mid-run via
	// send_message; the row shows the count instead of the messages,
	// which are LLM-to-LLM context.
	messages int

	// background marks a dispatch whose tool call already returned a
	// handle: the child session keeps running past that result, so the
	// result must not settle the task.
	background bool
	// childSessionID is the sub-session behind the dispatch, learned
	// from runtime events or derived at load, used to reconcile the
	// strip against the authoritative running list.
	childSessionID string
}

// taskRow maps a rendered strip row to its task for click handling.
type taskRow struct {
	yStart, yEnd int
	toolCallID   string
}

// agentTaskByToolCall returns the task for a parent-session agent tool
// call, or nil.
func (m *UI) agentTaskByToolCall(toolCallID string) *agentTask {
	for _, t := range m.agentTasks {
		if t.toolCallID == toolCallID {
			return t
		}
	}
	return nil
}

// upsertAgentTask registers or updates the task for an agent tool call
// seen in a parent-session assistant message. A call whose Finished
// flag flips only means the model finished emitting it — the subagent
// keeps running until its result or a terminal RuntimeEvent lands, so
// neither flag reaps here.
func (m *UI) upsertAgentTask(msg *message.Message, tc message.ToolCall) tea.Cmd {
	// A settled dispatch stays gone: the step-finish update of the
	// assistant message publishes after its tool results, so this call
	// runs again for an already-reaped task and would resurrect it as a
	// forever-running ghost.
	if m.reapedAgentTasks[tc.ID] {
		return nil
	}

	var params agent.AgentDispatchParams
	_ = json.Unmarshal([]byte(tc.Input), &params)
	if isAgentWaitCall(tc.Name, params) {
		return nil
	}

	task := m.agentTaskByToolCall(tc.ID)
	if task == nil {
		task = &agentTask{
			toolCallID: tc.ID,
			status:     subagents.StatusRunning,
			startedAt:  time.Now(),
		}
		m.agentTasks = append(m.agentTasks, task)
	}

	task.name = params.SubagentType
	if task.name == "" {
		task.name = subagentDisplayName
	}
	if task.color == "" {
		task.color = subagents.AutoColor(task.name)
	}
	task.prompt = params.Prompt
	task.background = !params.Blocking
	task.childSessionID = m.childSessionIDFor(msg.ID, tc.ID)

	if m.tasksSpinning() {
		m.updateLayoutAndSize()
		return m.taskSpinner.Tick
	}
	m.updateLayoutAndSize()
	return nil
}

// resolveAgentTaskResult records a parent-session tool result against its
// task and reports whether the tool call belonged to one. A result means
// the subagent finished, so the task is reaped: the strip is a viewer for
// ongoing work only.
func (m *UI) resolveAgentTaskResult(tr message.ToolResult) bool {
	task := m.agentTaskByToolCall(tr.ToolCallID)
	if task == nil {
		return false
	}
	// A background dispatch's tool result is only the start handle — the
	// subagent has not returned yet. Keep it spinning; a terminal
	// RuntimeEvent (or reconciliation against the running list) reaps it.
	if task.background {
		return true
	}
	task.result = &tr
	task.status = subagents.StatusCompleted
	if tr.IsError {
		task.status = subagents.StatusFailed
	}
	m.reapAgentTask(tr.ToolCallID)
	return true
}

// reapAgentTask removes a finished task from the strip and marks the
// dispatch as settled so late assistant-message updates for the same
// tool call cannot re-register it. When the strip empties while it
// held focus, focus falls back to the editor through the single
// focusEditor path, otherwise the caret would vanish with the strip
// (Draw only draws a cursor for the editor) and keys would route to a
// dead surface until the user tabbed away and back.
func (m *UI) reapAgentTask(toolCallID string) {
	if m.reapedAgentTasks == nil {
		m.reapedAgentTasks = make(map[string]bool)
	}
	m.reapedAgentTasks[toolCallID] = true
	for i, t := range m.agentTasks {
		if t.toolCallID == toolCallID {
			m.agentTasks = append(m.agentTasks[:i], m.agentTasks[i+1:]...)
			break
		}
	}
	if m.expandedTaskID == toolCallID {
		m.expandedTaskID = ""
		m.taskSubCursor = -1
	}
	m.clampTaskCursor()
	if m.focus == uiFocusTasks && len(m.agentTasks) == 0 {
		m.focusEditor()
	}
}

// subagentDisplayName is the strip title for a dispatch that did not
// name a subagent type.
const subagentDisplayName = "subagent"

// isAgentWaitCall reports whether an agent tool call is the waiting form
// rather than a dispatch. The agent tool doubles as both: with a prompt
// it starts a subagent, without one it blocks until the background
// subagents already running report back (see coordinator.waitForSubagents).
// A wait starts nothing, so it gets no strip row -- the rows it is waiting
// on are already there, and a second spinner named "subagent" (the
// fallback title, since a wait names no subagent_type) only doubles them.
// A call still streaming its input looks the same until its prompt
// arrives, which is also when there is a subagent worth showing a row for.
func isAgentWaitCall(name string, params agent.AgentDispatchParams) bool {
	return name == agent.AgentToolName && params.Prompt == ""
}

// childSessionIDFor derives the sub-session ID behind a dispatch. Empty
// when no workspace is wired (unit tests); the task then learns the ID
// from runtime events instead.
func (m *UI) childSessionIDFor(msgID, toolCallID string) string {
	if m.com == nil || m.com.Workspace == nil {
		return ""
	}
	return m.com.Workspace.CreateAgentToolSessionID(msgID, toolCallID)
}

// tasksSpinning reports whether any tracked task is still in flight.
func (m *UI) tasksSpinning() bool {
	for _, t := range m.agentTasks {
		if t.status == subagents.StatusRunning || t.status == subagents.StatusRetrying {
			return true
		}
	}
	return false
}

// resetAgentTasks drops task state on session switches.
func (m *UI) resetAgentTasks() {
	m.agentTasks = nil
	m.expandedTaskID = ""
	m.lastTaskFocusID = ""
	m.taskRows = nil
}

// loadAgentTasks rebuilds the task list from a session's persisted
// messages. The strip shows ongoing work only: finished dispatches are
// skipped, and unfinished ones count as running only while the session
// is actually busy (a session killed mid-generation would otherwise
// leave a ghost spinner, mirroring the chat's ghost-spinner guard).
func (m *UI) loadAgentTasks(msgs []*message.Message, toolResults map[string]message.ToolResult) {
	m.resetAgentTasks()
	busy := m.isAgentBusy()
	for _, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}
		for _, tc := range msg.ToolCalls() {
			if !chat.IsSubagentTool(tc.Name) {
				continue
			}
			var params agent.AgentDispatchParams
			_ = json.Unmarshal([]byte(tc.Input), &params)
			_, hasResult := toolResults[tc.ID]
			canceled := msg.FinishReason() == message.FinishReasonCanceled
			// A set Finished flag only means the model finished emitting
			// the call; the subagent runs until a result lands. Skip only
			// blocking dispatches that settled, or that cannot be running
			// because the session is idle. A background dispatch (the
			// default) is excepted on both counts: its tool result is just
			// the start handle and it runs independently of the parent's
			// busy state; the running list reconciles it instead.
			if canceled || (params.Blocking && (hasResult || !busy)) {
				continue
			}
			if isAgentWaitCall(tc.Name, params) {
				continue
			}
			task := &agentTask{
				toolCallID:     tc.ID,
				startedAt:      time.Unix(msg.CreatedAt, 0),
				status:         subagents.StatusRunning,
				background:     !params.Blocking,
				childSessionID: m.childSessionIDFor(msg.ID, tc.ID),
			}
			task.name = params.SubagentType
			if task.name == "" {
				task.name = subagentDisplayName
			}
			task.color = subagents.AutoColor(task.name)
			task.prompt = params.Prompt
			m.agentTasks = append(m.agentTasks, task)

			m.loadTaskNestedTools(msg, tc, task)
		}
	}
	// Count the report-backs each running task already received.
	noteCounts := subagentNoteCounts(msgs)
	for _, t := range m.agentTasks {
		if t.childSessionID != "" {
			t.messages = noteCounts[t.childSessionID]
		}
	}
	m.clampTaskCursor()
}

// subagentNoteCounts counts report-backs per child session across a
// session's messages, keyed the way tasks are (by child session ID).
func subagentNoteCounts(msgs []*message.Message) map[string]int {
	var counts map[string]int
	for _, msg := range msgs {
		for _, note := range msg.SubagentNotes() {
			if note.ChildSessionID == "" {
				continue
			}
			if counts == nil {
				counts = make(map[string]int)
			}
			counts[note.ChildSessionID]++
		}
	}
	return counts
}

// countSubagentNote bumps the report-back count on the task running the
// note's child session. The note itself never renders: the count is the
// user-facing trace that a background agent reported back.
func (m *UI) countSubagentNote(note message.SubagentNote) {
	if note.ChildSessionID == "" {
		return
	}
	if task := m.agentTaskByChildSession(note.ChildSessionID); task != nil {
		task.messages++
	}
}

// agentTaskByChildSession returns the task running the given child
// session, or nil.
func (m *UI) agentTaskByChildSession(childSessionID string) *agentTask {
	for _, t := range m.agentTasks {
		if t.childSessionID == childSessionID {
			return t
		}
	}
	return nil
}

// loadTaskNestedTools fetches a finished subagent's own tool calls from
// its child session so the expanded strip entry can show them.
func (m *UI) loadTaskNestedTools(msg *message.Message, tc message.ToolCall, task *agentTask) {
	agentSessionID := m.com.Workspace.CreateAgentToolSessionID(msg.ID, tc.ID)
	nestedMsgs, err := m.com.Workspace.ListMessages(context.Background(), agentSessionID)
	if err != nil {
		return
	}
	nestedPtrs := make([]*message.Message, len(nestedMsgs))
	for i := range nestedMsgs {
		nestedPtrs[i] = &nestedMsgs[i]
	}
	resultMap := chat.BuildToolResultMap(nestedPtrs)
	for _, nestedMsg := range nestedPtrs {
		for _, item := range chat.ExtractMessageItems(m.com.Styles, nestedMsg, resultMap) {
			if nestedTool, ok := item.(chat.ToolMessageItem); ok {
				if simplifiable, ok := nestedTool.(chat.Compactable); ok {
					simplifiable.SetCompact(true)
				}
				task.nested = append(task.nested, nestedTool)
			}
		}
	}
}

// updateAgentTaskFromChildSession folds a child-session message into its
// task: tool calls and results become the task's nested one-liners.
func (m *UI) updateAgentTaskFromChildSession(event message.Message) {
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(event.SessionID)
	if !ok {
		return
	}
	task := m.agentTaskByToolCall(toolCallID)
	if task == nil {
		return
	}

	for _, tc := range event.ToolCalls() {
		// Context plumbing (skill_search, tool_search) is noise even in
		// the expanded task view.
		if chat.IsInternalContextTool(tc.Name) {
			continue
		}
		found := false
		for _, existing := range task.nested {
			if existing.ToolCall().ID == tc.ID {
				existing.SetToolCall(tc)
				found = true
				break
			}
		}
		if !found {
			nested := chat.NewToolMessageItem(m.com.Styles, event.ID, tc, nil, false)
			if simplifiable, ok := nested.(chat.Compactable); ok {
				simplifiable.SetCompact(true)
			}
			task.nested = append(task.nested, nested)
		}
	}
	for _, tr := range event.ToolResults() {
		for _, nested := range task.nested {
			if nested.ToolCall().ID == tr.ToolCallID {
				res := tr
				nested.SetResult(&res)
				break
			}
		}
	}
}

// applyRunningSubagentInfo merges live runtime info (name, color, model,
// status, tokens) into the matching task. A terminal status reaps the
// task: the strip shows ongoing work only.
func (m *UI) applyRunningSubagentInfo(info childSessionInfo) {
	if m.com == nil || m.com.Workspace == nil {
		return
	}
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(info.ChildSessionID)
	if !ok {
		return
	}
	task := m.agentTaskByToolCall(toolCallID)
	if task == nil {
		return
	}
	if task.childSessionID == "" {
		task.childSessionID = info.ChildSessionID
	}
	switch info.Status {
	case subagents.StatusCompleted, subagents.StatusCancelled, subagents.StatusFailed:
		m.reapAgentTask(toolCallID)
		return
	}
	if info.Name != "" {
		task.name = info.Name
	}
	if info.Color != "" {
		task.color = info.Color
	}
	if info.Model != "" {
		task.model = info.Model
	}
	if info.Status != "" {
		task.status = info.Status
	}
	task.promptTokens = info.PromptTokens
	task.completionTokens = info.CompletionTokens
}

// tasksReconcileInterval paces the backstop refresh of the running
// list while tasks spin (see maybeReconcileSpinningTasks).
const tasksReconcileInterval = 2 * time.Second

// tasksReconcileGrace is how long a task must have been running before
// the authoritative running list may settle it. A fetch dispatched
// right after a dispatch's assistant message landed can race the
// runtime Register that announces the child; the grace keeps that fetch
// from reaping a task that is about to start. A lost terminal event
// still heals within interval plus grace.
const tasksReconcileGrace = 10 * time.Second

// maybeReconcileSpinningTasks refreshes the authoritative running list
// while tasks spin, at most once per tasksReconcileInterval. Runtime
// events drive the normal path; this is the backstop for a terminal
// event lost in flight — the runtime entry is already deleted, so no
// later event would ever settle the strip row and the spinner would run
// forever.
func (m *UI) maybeReconcileSpinningTasks() tea.Cmd {
	if !m.tasksSpinning() || m.session == nil {
		return nil
	}
	if m.lastTasksReconcile.IsZero() {
		m.lastTasksReconcile = time.Now()
		return nil
	}
	if time.Since(m.lastTasksReconcile) < tasksReconcileInterval {
		return nil
	}
	m.lastTasksReconcile = time.Now()
	return m.refreshRunningSubagents(m.session.ID)
}

// reconcileBackgroundTasks settles dispatches the event stream can miss
// (a Finished RuntimeEvent raced a session switch or was lost in
// flight, or the session was reloaded from history): the fetched
// running list is authoritative, so a task whose child session is
// absent from it is done — blocking and background dispatches alike.
// Young tasks are left alone so a fetch that raced the runtime Register
// cannot reap a dispatch that is about to start (see
// tasksReconcileGrace).
func (m *UI) reconcileBackgroundTasks(list []workspace.RunningSubagentInfo) {
	running := make(map[string]bool, len(list))
	for _, info := range list {
		running[info.ChildSessionID] = true
	}
	tasks := append([]*agentTask(nil), m.agentTasks...)
	for _, t := range tasks {
		if t.status != subagents.StatusRunning && t.status != subagents.StatusRetrying {
			continue
		}
		if t.childSessionID == "" || running[t.childSessionID] {
			continue
		}
		if time.Since(t.startedAt) < tasksReconcileGrace {
			continue
		}
		m.reapAgentTask(t.toolCallID)
	}
}

// childSessionInfo is the subset of runtime/running-subagent data the
// strip consumes, so both the RuntimeEvent and the enriched
// runningSubagentsMsg paths can feed it.
type childSessionInfo struct {
	ChildSessionID   string
	Name             string
	Color            string
	Model            string
	Status           string
	PromptTokens     int64
	CompletionTokens int64
}

// tasksAreaHeight returns the strip height: one row per visible task
// plus the expanded entry's detail block.
func (m *UI) tasksAreaHeight() int {
	if m.state != uiChat || len(m.agentTasks) == 0 {
		m.tasksView = ""
		m.taskRows = nil
		return 0
	}
	m.tasksView = m.renderTasks(m.width)
	return lipgloss.Height(m.tasksView)
}

// taskWindow returns the strip's scrolling window: at most three rows
// around the cursor, so the list scrolls with selection instead of
// growing past three lines.
func (m *UI) taskWindow() ([]*agentTask, int) {
	const windowSize = 3
	n := len(m.agentTasks)
	if n == 0 {
		return nil, 0
	}
	cursor := min(max(m.taskCursor, 0), n-1)
	start := max(0, min(cursor-1, n-windowSize))
	if n <= windowSize {
		start = 0
	}
	end := min(n, start+windowSize)
	return m.agentTasks[start:end], start
}

// clampTaskCursor keeps the cursor inside the task list after tasks
// arrive, finish, or the strip is focused.
func (m *UI) clampTaskCursor() {
	n := len(m.agentTasks)
	if n == 0 {
		m.taskCursor, m.taskSubCursor = 0, -1
		return
	}
	m.taskCursor = min(max(m.taskCursor, 0), n-1)
	task := m.agentTasks[m.taskCursor]
	if task.toolCallID != m.expandedTaskID || m.taskSubCursor >= len(task.nested) {
		m.taskSubCursor = -1
	}
}

// noteTaskFocus records the task under the strip cursor so focus can
// return to it later. Reaps shift indices, so an index alone can drift
// onto a different task between visits.
func (m *UI) noteTaskFocus() {
	if m.focus == uiFocusTasks && len(m.agentTasks) > 0 {
		m.lastTaskFocusID = m.agentTasks[m.taskCursor].toolCallID
	}
}

// focusTasks moves focus to the strip, returning the cursor to the task
// the user last focused when it is still tracked, and leaving it where
// clampTaskCursor parks it otherwise.
func (m *UI) focusTasks() {
	if len(m.agentTasks) == 0 {
		return
	}
	if m.lastTaskFocusID != "" {
		if i := slices.IndexFunc(m.agentTasks, func(t *agentTask) bool {
			return t.toolCallID == m.lastTaskFocusID
		}); i >= 0 {
			m.taskCursor = i
			m.taskSubCursor = -1
		}
	}
	m.focus = uiFocusTasks
	m.textarea.Blur()
	m.chat.Blur()
	m.clampTaskCursor()
}

// taskCursorDown moves the strip cursor down: through the cursor
// task's nested calls when expanded, otherwise to the next task. It
// reports whether the cursor moved.
func (m *UI) taskCursorDown() bool {
	if len(m.agentTasks) == 0 {
		return false
	}
	m.clampTaskCursor()
	task := m.agentTasks[m.taskCursor]
	if task.toolCallID == m.expandedTaskID && m.taskSubCursor < len(task.nested)-1 {
		m.taskSubCursor++
		m.noteTaskFocus()
		return true
	}
	if m.taskCursor < len(m.agentTasks)-1 {
		m.taskCursor++
		m.taskSubCursor = -1
		m.noteTaskFocus()
		return true
	}
	return false
}

// taskCursorUp moves the strip cursor up, mirroring taskCursorDown. It
// reports whether the cursor moved.
func (m *UI) taskCursorUp() bool {
	if len(m.agentTasks) == 0 {
		return false
	}
	m.clampTaskCursor()
	if m.taskSubCursor > 0 {
		m.taskSubCursor--
		m.noteTaskFocus()
		return true
	}
	if m.taskSubCursor == 0 {
		m.taskSubCursor = -1
		return true
	}
	if m.taskCursor > 0 {
		m.taskCursor--
		task := m.agentTasks[m.taskCursor]
		if task.toolCallID == m.expandedTaskID && len(task.nested) > 0 {
			m.taskSubCursor = len(task.nested) - 1
		} else {
			m.taskSubCursor = -1
		}
		m.noteTaskFocus()
		return true
	}
	return false
}

// toggleTaskAtCursor expands or collapses whatever the strip cursor is
// on: a nested call toggles between its one-liner and full view, a task
// row toggles the task's detail block.
func (m *UI) toggleTaskAtCursor() {
	if len(m.agentTasks) == 0 {
		return
	}
	m.clampTaskCursor()
	task := m.agentTasks[m.taskCursor]
	if m.taskSubCursor >= 0 && m.taskSubCursor < len(task.nested) {
		chat.ToggleFullView(task.nested[m.taskSubCursor])
		m.updateLayoutAndSize()
		return
	}
	if m.expandedTaskID == task.toolCallID {
		m.expandedTaskID = ""
	} else {
		m.expandedTaskID = task.toolCallID
	}
	m.taskSubCursor = -1
	m.updateLayoutAndSize()
}

// enterTaskAtCursor implements the enter key: go in one level. On a task
// row that expands the task and drops the cursor on its first call; on
// a call line it opens that call's full view.
func (m *UI) enterTaskAtCursor() {
	if len(m.agentTasks) == 0 {
		return
	}
	m.clampTaskCursor()
	task := m.agentTasks[m.taskCursor]
	if m.taskSubCursor >= 0 && m.taskSubCursor < len(task.nested) {
		nested := task.nested[m.taskSubCursor]
		if !chat.ShowsFullView(nested) {
			chat.ToggleFullView(nested)
			m.updateLayoutAndSize()
		}
		return
	}
	if m.expandedTaskID != task.toolCallID {
		m.expandedTaskID = task.toolCallID
	}
	if len(task.nested) > 0 {
		m.taskSubCursor = 0
	}
	m.updateLayoutAndSize()
}

// ascendTaskAtCursor implements the escape key: go out one level. A
// fully rendered call collapses back to its one-liner; with the
// selection already collapsed (or on the task row), the task itself
// collapses. With nothing left to leave, the strip hands focus back to
// the editor.
func (m *UI) ascendTaskAtCursor() tea.Cmd {
	if len(m.agentTasks) == 0 {
		return m.focusEditorFromTasks()
	}
	m.clampTaskCursor()
	task := m.agentTasks[m.taskCursor]
	if m.taskSubCursor >= 0 && m.taskSubCursor < len(task.nested) {
		nested := task.nested[m.taskSubCursor]
		if chat.ShowsFullView(nested) {
			chat.ToggleFullView(nested)
			m.updateLayoutAndSize()
			return nil
		}
	}
	if m.expandedTaskID == task.toolCallID {
		m.expandedTaskID = ""
		m.taskSubCursor = -1
		m.updateLayoutAndSize()
		return nil
	}
	return m.focusEditorFromTasks()
}

// handleTaskKey processes a keypress while the strip is focused. Arrows
// (plain or shifted) move the cursor within the current level, enter
// goes in, escape goes out, tab leaves for the editor.
func (m *UI) handleTaskKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keyMap.Chat.Up):
		m.taskCursorUp()
		return true, nil
	case key.Matches(msg, m.keyMap.Chat.UpOneItem):
		// Shift+up at the top of the strip continues into the
		// transcript, landing on its newest entry; the plain arrow
		// stops at the edge.
		if !m.taskCursorUp() {
			return true, m.focusChatFromTasks(chatEntryFromBelow)
		}
		return true, nil
	case key.Matches(msg, m.keyMap.Chat.Down):
		m.taskCursorDown()
		return true, nil
	case key.Matches(msg, m.keyMap.Chat.DownOneItem):
		// Shift+down at the bottom of the strip continues into the
		// editor; the plain arrow stops at the edge.
		if !m.taskCursorDown() {
			return true, m.focusEditorFromTasks()
		}
		return true, nil
	case key.Matches(msg, m.keyMap.Chat.DigIn):
		m.enterTaskAtCursor()
		return true, nil
	case key.Matches(msg, m.keyMap.Chat.Expand):
		m.toggleTaskAtCursor()
		return true, nil
	case key.Matches(msg, m.keyMap.Chat.ClearHighlight):
		return true, m.ascendTaskAtCursor()
	case key.Matches(msg, m.keyMap.Tab):
		return true, m.focusEditorFromTasks()
	case key.Matches(msg, m.keyMap.ShiftTab):
		return true, m.focusChatFromTasks(chatEntryCycled)
	}
	return false, nil
}

func (m *UI) focusEditorFromTasks() tea.Cmd {
	return m.focusEditor()
}

// chatEntry names the gesture that moves focus into the transcript,
// since the two land differently: the tab cycle restores the last
// selection, an upward arrow arriving from below always lands on the
// newest entry.
type chatEntry uint8

// Possible chatEntry values.
const (
	chatEntryCycled chatEntry = iota
	chatEntryFromBelow
)

// focusChat enters the transcript with the entry gesture's landing
// rule. It only picks the Chat entry point; the caller owns moving
// focus off the region it came from.
func (m *UI) focusChat(entry chatEntry) tea.Cmd {
	if entry == chatEntryFromBelow {
		return m.chat.FocusSelectingNewest()
	}
	return m.chat.FocusRestoringSelection()
}

func (m *UI) focusChatFromTasks(entry chatEntry) tea.Cmd {
	m.focus = uiFocusMain
	return m.focusChat(entry)
}

// focusBelowChat moves focus out of the transcript to the region below
// it: the background tasks strip when present, otherwise the editor.
func (m *UI) focusBelowChat() tea.Cmd {
	m.chat.Blur()
	if m.state == uiChat && len(m.agentTasks) > 0 {
		m.focusTasks()
		return nil
	}
	return m.focusEditor()
}

// focusAboveEditor moves focus out of the editor to the region above
// it: the background tasks strip when present, otherwise the
// transcript, entered with the given gesture's landing rule. The
// landing state has no transcript, so it stays put.
func (m *UI) focusAboveEditor(entry chatEntry) tea.Cmd {
	if m.state == uiChat && len(m.agentTasks) > 0 {
		m.focusTasks()
		return nil
	}
	if m.state == uiLanding {
		return nil
	}
	m.setState(m.state, uiFocusMain)
	m.textarea.Blur()
	return m.focusChat(entry)
}

// renderTasks renders the background tasks strip and records the row
// layout for mouse handling. Rows carry the same focused/blurred prefix
// bar as transcript items, and the list is a three-row scrolling window
// around the cursor.
func (m *UI) renderTasks(width int) string {
	t := m.com.Styles
	m.taskRows = nil
	m.clampTaskCursor()

	visible, start := m.taskWindow()
	if len(visible) == 0 {
		return ""
	}
	focused := m.focus == uiFocusTasks
	focusedPrefix := t.Messages.ToolCallFocused.Render()
	blurredPrefix := t.Messages.ToolCallBlurred.Render()
	prefixWidth := lipgloss.Width(focusedPrefix)

	renderRow := func(task *agentTask) string {
		dot := t.SubagentDot(task.color)

		statusStyle := t.Resource.AdditionalText
		var status string
		switch task.status {
		case subagents.StatusRunning, subagents.StatusRetrying:
			status = m.taskSpinner.View()
			if task.status == subagents.StatusRetrying {
				status += " retrying"
			}
		default:
			status = statusStyle.Render(task.status)
		}

		line := dot + " " + t.Resource.Name.Render(task.name) + " " + status
		if task.model != "" {
			meta := task.model
			if count := common.FormatSubagentTokenCount(task.promptTokens, task.completionTokens); count != "" {
				meta += " " + count
			}
			line += " " + t.Resource.AdditionalText.Render(meta)
		}
		if task.messages > 0 {
			line += " " + t.Resource.AdditionalText.Render(fmt.Sprintf("%d msg", task.messages))
		}
		if len(task.nested) > 0 {
			line += " " + t.Resource.AdditionalText.Render(fmtToolCalls(len(task.nested)))
		}
		return ansi.Truncate(line, max(width-prefixWidth, 1), "…")
	}

	var rows []string
	for i, task := range visible {
		onCursor := focused && start+i == m.taskCursor
		subCursor := -1
		if onCursor && task.toolCallID == m.expandedTaskID {
			subCursor = m.taskSubCursor
		}
		prefix := blurredPrefix
		// The bar marks one row at a time: once the sub-cursor is down
		// in the task's own calls it moves there instead of staying on
		// the task row in a second color.
		if onCursor && subCursor < 0 {
			prefix = focusedPrefix
		}
		rows = append(rows, prefix+renderRow(task))

		if task.toolCallID == m.expandedTaskID {
			rows = append(rows, m.renderTaskDetails(task, width, subCursor)...)
		}
	}

	// Record row bounds for click handling: each task occupies its own
	// row (expanded details belong to the task's own rows).
	y := 0
	for _, task := range visible {
		height := 1
		if task.toolCallID == m.expandedTaskID {
			height += lipgloss.Height(strings.Join(m.renderTaskDetails(task, width, -1), "\n"))
		}
		m.taskRows = append(m.taskRows, taskRow{yStart: y, yEnd: y + height, toolCallID: task.toolCallID})
		y += height
	}

	return t.Pills.Area.Render(strings.Join(rows, "\n"))
}

// renderTaskDetails renders the expanded block under a task row: the
// subagent's own tool calls as one-liners and its result. The dispatch
// prompt and any send_message content stay out — they are context for
// the model, not the transcript; the row's msg count covers report-backs.
// Detail lines fill the row's content width - the body width the
// transcript's ToolBodyWidth derives - under the strip's two-column
// indent, which the sub-cursor recolors into the focused bar.
func (m *UI) renderTaskDetails(task *agentTask, width, subCursor int) []string {
	t := m.com.Styles
	bodyWidth := chat.ToolBodyWidth(width, 0)
	var lines []string
	for j, nested := range task.nested {
		indent := "  "
		if j == subCursor {
			indent = t.Messages.ToolCallFocused.Render()
		}
		lines = append(lines, chat.NestedToolLines(t, nested, bodyWidth, indent, j == subCursor)...)
	}
	if task.result != nil && task.result.Content != "" {
		excerpt := chat.FirstLine(task.result.Content)
		if excerpt != "" {
			label := "Result: "
			lines = append(lines, "  "+t.Resource.AdditionalText.Render(label)+ansi.Truncate(excerpt, max(bodyWidth-lipgloss.Width(label), 1), "…"))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "  "+t.Resource.AdditionalText.Render("No activity recorded"))
	}
	return lines
}

// handleTaskClick toggles the expanded task for a click inside the strip
// and moves the keyboard cursor onto the clicked row.
func (m *UI) handleTaskClick(_, y int) bool {
	if len(m.taskRows) == 0 {
		return false
	}
	for _, row := range m.taskRows {
		if y >= row.yStart && y < row.yEnd {
			for i, task := range m.agentTasks {
				if task.toolCallID == row.toolCallID {
					m.taskCursor = i
					m.taskSubCursor = -1
					m.lastTaskFocusID = row.toolCallID
					break
				}
			}
			if m.expandedTaskID == row.toolCallID {
				m.expandedTaskID = ""
			} else {
				m.expandedTaskID = row.toolCallID
			}
			m.updateLayoutAndSize()
			return true
		}
	}
	return false
}

func fmtToolCalls(n int) string {
	if n == 1 {
		return "(1 tool call)"
	}
	return fmt.Sprintf("(%d tool calls)", n)
}
