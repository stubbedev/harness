package model

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

// agentTask tracks one subagent run for the background tasks strip.
// Subagents do not render in the transcript; this is their holder, and
// the transcript shows their session when the row is activated.
type agentTask struct {
	toolCallID     string
	childSessionID string
	description    string
	status         string
	startedAt      time.Time
	background     bool
	dispatched     bool
	// titled marks a description that came from the child session's
	// generated title rather than the dispatch-prompt excerpt, so a
	// later message update cannot overwrite it with the excerpt again.
	titled bool
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
	if agent.IsAgentWaitCall(tc.Name, tc.Input) {
		return nil
	}

	task := m.agentTaskByToolCall(tc.ID)
	if task == nil {
		task = &agentTask{
			toolCallID:  tc.ID,
			status:      subagents.StatusRunning,
			startedAt:   time.Now(),
			description: promptExcerpt(params.Prompt),
		}
		m.agentTasks = append(m.agentTasks, task)
	}
	task.background = !params.Blocking
	if !task.titled {
		task.description = promptExcerpt(params.Prompt)
	}
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
	// An error result is different: the dispatch never started (unknown
	// type, build or session failure), so it is the run's terminal
	// signal and settles the task. Without this an unstarted background
	// dispatch would spin forever — it never registers with the runtime,
	// so no terminal event can ever arrive.
	task.dispatched = true
	if task.background && tr.IsError {
		task.status = subagents.StatusFailed
		m.reapAgentTask(tr.ToolCallID)
		return true
	}
	if task.background {
		return true
	}
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
	m.clampTaskCursor()
	if m.focus == uiFocusTasks && m.taskRowCount() == 0 {
		m.focusEditor()
	}
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
	return m.runningAgentCount() > 0
}

// runningAgentCount is how many tracked tasks are still running. One
// source for the wait call's "Waiting for N agents" header (via
// itemEnv) and the strip's spinning state.
func (m *UI) runningAgentCount() int {
	n := 0
	for _, t := range m.agentTasks {
		if t.status == subagents.StatusRunning || t.status == subagents.StatusRetrying {
			n++
		}
	}
	return n
}

// resetAgentTasks drops task state on session switches.
func (m *UI) resetAgentTasks() {
	m.agentTasks = nil
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
			if agent.IsAgentWaitCall(tc.Name, tc.Input) {
				continue
			}
			task := &agentTask{
				toolCallID:     tc.ID,
				startedAt:      time.Unix(msg.CreatedAt, 0),
				status:         subagents.StatusRunning,
				background:     !params.Blocking,
				dispatched:     hasResult,
				description:    promptExcerpt(params.Prompt),
				childSessionID: m.childSessionIDFor(msg.ID, tc.ID),
			}
			m.agentTasks = append(m.agentTasks, task)
		}
	}
	m.clampTaskCursor()
}

// promptExcerpt builds a strip row's fallback description: the first
// line of the dispatch prompt, ellipsized. The child session's generated
// title replaces it once it lands (applyTaskTitle).
func promptExcerpt(prompt string) string {
	const max = 64
	line := strings.TrimSpace(chat.FirstLine(prompt))
	return ansi.Truncate(line, max, "…")
}

// applyTaskTitle adopts a child session's generated title as its task's
// description. The "New Agent Session" placeholder the dispatch created
// the session with is ignored: it says nothing the prompt excerpt does
// not.
func (m *UI) applyTaskTitle(childSessionID, title string) bool {
	title = strings.TrimSpace(title)
	if title == "" || title == "New Agent Session" {
		return false
	}
	task := m.agentTaskByChildSession(childSessionID)
	if task == nil || task.titled && task.description == title {
		return false
	}
	task.description = title
	task.titled = true
	return true
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

// agentViewRef is the shown/requested session pair behind the
// transcript's agent view; see UI.agentView. Empty strings mean main.
type agentViewRef struct {
	shown     string
	requested string
}

// agentTranscriptMsg carries the messages of the session whose
// transcript the strip switched to (an agent's, or back to main),
// fetched off-thread. forSession guards against a session switch
// racing the fetch, childSessionID against a newer view switch.
type agentTranscriptMsg struct {
	forSession     string
	childSessionID string
	msgs           []message.Message
	// retraced marks the second fetch of a switch, sent to pick up
	// events that landed between the first snapshot and the swap; its
	// handler must not retrace again.
	retraced bool
}

// viewAgentSession switches the transcript to a subagent's session: its
// dispatch prompt is its first user message, so its work renders exactly
// like the main chat. The fetch is off-thread; the switch back is
// viewMainSession.
func (m *UI) viewAgentSession(childSessionID string) tea.Cmd {
	if childSessionID == "" {
		return util.ReportWarn("Agent has not started yet")
	}
	if m.agentView.requested == childSessionID && m.agentView.shown == childSessionID {
		return nil
	}
	if m.session == nil {
		return nil
	}
	m.agentView.requested = childSessionID
	m.focusTranscript()
	sessionID := m.session.ID
	return m.fetchAgentTranscript(sessionID, childSessionID, false)
}

// viewMainSession switches the transcript back to the main session and
// reloads it: messages that arrived while an agent was viewed were
// never applied to the chat.
func (m *UI) viewMainSession() tea.Cmd {
	if m.agentView.shown == "" && m.agentView.requested == "" || m.session == nil {
		return nil
	}
	m.agentView.requested = ""
	m.focusTranscript()
	sessionID := m.session.ID
	return m.fetchAgentTranscript(sessionID, "", false)
}

// fetchAgentTranscript builds the off-thread transcript fetch. The
// session ID is captured so a session switch during the fetch invalidates
// the result. An empty child session ID means main and lists the main
// session itself: the workspace keys on a concrete session ID.
func (m *UI) fetchAgentTranscript(sessionID, childSessionID string, retraced bool) tea.Cmd {
	return func() tea.Msg {
		listID := childSessionID
		if listID == "" {
			listID = sessionID
		}
		msgs, err := m.com.Workspace.ListMessages(context.Background(), listID)
		if err != nil {
			return util.InfoMsg{Type: util.InfoTypeError, Msg: fmt.Sprintf("Failed to load transcript: %v", err)}
		}
		return agentTranscriptMsg{forSession: sessionID, childSessionID: childSessionID, msgs: msgs, retraced: retraced}
	}
}

// handleAgentTranscriptMsg applies a fetched transcript. A fetch is
// discarded when it raced a newer switch: a session change, or the user
// moving to another agent (or back to main) before it resolved. The swap
// is atomic — the old transcript stays visible until the fetched one is
// complete, so a switch never shows a half-populated view — and either
// view retraces once to fold in events that landed between snapshot and
// swap (the viewed session's own traffic does not paint mid-switch).
func (m *UI) handleAgentTranscriptMsg(msg agentTranscriptMsg) tea.Cmd {
	if m.session == nil || msg.forSession != m.session.ID {
		return nil
	}
	if msg.childSessionID != m.agentView.requested {
		return nil
	}
	if msg.childSessionID == "" {
		m.agentView.shown = ""
		// The set's animation ticks are dropped, mirroring the child
		// path below: the retrace applies again with fresh messages.
		m.setSessionMessages(msg.msgs)
		if msg.retraced {
			return nil
		}
		return m.fetchAgentTranscript(msg.forSession, "", true)
	}
	m.setChildSessionMessages(msg.childSessionID, msg.msgs)
	m.agentView.shown = msg.childSessionID
	if msg.retraced {
		return nil
	}
	return m.fetchAgentTranscript(msg.forSession, msg.childSessionID, true)
}

// setChildSessionMessages renders a subagent's session into the
// transcript, mirroring setSessionMessages minus the main-session
// bookkeeping (the task strip is not rebuilt from a child's history,
// queued prompts are a main-session concept, and assistant-info items
// are only defined for the main turn).
func (m *UI) setChildSessionMessages(childSessionID string, msgs []message.Message) {
	msgPtrs := make([]*message.Message, len(msgs))
	for i := range msgs {
		msgPtrs[i] = &msgs[i]
	}
	toolResultMap := chat.BuildToolResultMap(msgPtrs)
	items := m.buildTranscriptItems(msgPtrs, toolResultMap)
	// A viewed agent is by definition working; keep the animation clock
	// running so its in-flight tool spinners move.
	m.chat.SetAnimationsAllowed(true)
	m.chat.SetMessages(childSessionID, items...)
}

// buildTranscriptItems converts messages into transcript items. One
// path for every transcript build — main or agent, load or live append
// is delegated through it — so the item shape cannot drift between
// them.
func (m *UI) buildTranscriptItems(msgs []*message.Message, toolResultMap map[string]message.ToolResult) []chat.MessageItem {
	items := make([]chat.MessageItem, 0, len(msgs))
	for _, msg := range msgs {
		items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap, m.itemEnv())...)
	}
	return items
}

// itemEnv carries the live bindings message items render from: the
// still-running subagent count behind an agent-wait call's header.
func (m *UI) itemEnv() *chat.ItemEnv {
	return &chat.ItemEnv{WaitingAgents: m.runningAgentCount}
}

// focusTranscript moves focus onto the transcript after a view switch:
// the point of switching is reading the transcript, so the user lands
// there with the editor blurred.
func (m *UI) focusTranscript() {
	m.setState(m.state, uiFocusMain)
	m.textarea.Blur()
	m.chat.Focus()
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
	if info.Status != "" {
		task.status = info.Status
	}
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
// Only dispatched tasks qualify: a dispatch that is still streaming
// its input registers a strip row long before fantasy executes the
// call and creates the child session, so its absence from the list is
// expected, not terminal — settling it there reaped all but the newest
// row of a wide fan-out while the model was still emitting it. Young
// dispatched tasks are also left alone so a fetch that raced the
// runtime Register cannot reap a dispatch that is about to start (see
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
		if !t.dispatched || t.childSessionID == "" || running[t.childSessionID] {
			continue
		}
		if time.Since(t.startedAt) < tasksReconcileGrace {
			continue
		}
		m.reapAgentTask(t.toolCallID)
	}
}

// childSessionInfo is the subset of runtime data the strip consumes, so
// both the RuntimeEvent and the enriched runningSubagentsMsg paths can
// feed it.
type childSessionInfo struct {
	ChildSessionID string
	Status         string
}

// mainRowCount is the number of pinned rows ahead of the task rows:
// the Main Agent row exists only while an agent's session is viewed.
func (m *UI) mainRowCount() int {
	if m.agentView.shown != "" {
		return 1
	}
	return 0
}

// taskRowCount is the strip's row count. The strip is one level deep:
// its task rows are the children of the viewed session — the main
// session's dispatches, or none at all while an agent is viewed
// (subagents never dispatch), where only the pinned Main row shows.
func (m *UI) taskRowCount() int {
	if m.agentView.shown != "" {
		return m.mainRowCount()
	}
	return len(m.agentTasks)
}

// taskRowAt resolves a strip row index to its task, or to the Main row
// (task nil, isMain true). While an agent is viewed the Main row is
// pinned first and is the only row; from main, the tasks follow in
// dispatch order.
func (m *UI) taskRowAt(row int) (task *agentTask, isMain bool) {
	if row < m.mainRowCount() {
		return nil, true
	}
	if m.agentView.shown != "" {
		return nil, false
	}
	row -= m.mainRowCount()
	if row < 0 || row >= len(m.agentTasks) {
		return nil, false
	}
	return m.agentTasks[row], false
}

// tasksAreaHeight returns the strip height: one line per visible row.
func (m *UI) tasksAreaHeight() int {
	if m.state != uiChat || m.taskRowCount() == 0 {
		m.tasksView = ""
		m.taskRows = nil
		return 0
	}
	m.tasksView = m.renderTasks(m.width)
	return lipgloss.Height(m.tasksView)
}

// clampTaskCursor keeps the cursor inside the row list after rows
// arrive, finish, or the strip is focused.
func (m *UI) clampTaskCursor() {
	n := m.taskRowCount()
	if n == 0 {
		m.taskCursor = 0
		return
	}
	m.taskCursor = min(max(m.taskCursor, 0), n-1)
}

// noteTaskFocus records the row under the strip cursor so focus can
// return to it later. Reaps shift indices, so an index alone can drift
// onto a different row between visits.
func (m *UI) noteTaskFocus() {
	if m.focus != uiFocusTasks {
		return
	}
	if task, isMain := m.taskRowAt(m.taskCursor); isMain || task == nil {
		m.lastTaskFocusID = ""
	} else {
		m.lastTaskFocusID = task.toolCallID
	}
}

// focusTasks moves focus to the strip, returning the cursor to the row
// the user last focused when it is still tracked, and leaving it where
// clampTaskCursor parks it otherwise.
func (m *UI) focusTasks() {
	if m.taskRowCount() == 0 {
		return
	}
	if m.lastTaskFocusID != "" {
		for i, t := range m.agentTasks {
			if t.toolCallID == m.lastTaskFocusID {
				m.taskCursor = i + m.mainRowCount()
				break
			}
		}
	}
	m.focus = uiFocusTasks
	m.textarea.Blur()
	m.chat.Blur()
	m.clampTaskCursor()
}

// taskCursorDown moves the strip cursor down one row. It reports
// whether the cursor moved.
func (m *UI) taskCursorDown() bool {
	if m.taskRowCount() == 0 {
		return false
	}
	m.clampTaskCursor()
	if m.taskCursor < m.taskRowCount()-1 {
		m.taskCursor++
		m.noteTaskFocus()
		return true
	}
	return false
}

// taskCursorUp moves the strip cursor up one row, mirroring
// taskCursorDown. It reports whether the cursor moved.
func (m *UI) taskCursorUp() bool {
	if m.taskRowCount() == 0 {
		return false
	}
	m.clampTaskCursor()
	if m.taskCursor > 0 {
		m.taskCursor--
		m.noteTaskFocus()
		return true
	}
	return false
}

// activateTaskAtCursor implements the enter key and clicks: switch the
// transcript to the cursor row's session — the agent's from a task row,
// back to main from the Main row.
func (m *UI) activateTaskAtCursor() tea.Cmd {
	if m.taskRowCount() == 0 {
		return nil
	}
	m.clampTaskCursor()
	task, isMain := m.taskRowAt(m.taskCursor)
	if isMain {
		return m.viewMainSession()
	}
	if task == nil {
		return nil
	}
	return m.viewAgentSession(task.childSessionID)
}

// handleTaskKey processes a keypress while the strip is focused. Arrows
// (plain or shifted) move the cursor or hand focus off at the edges,
// enter/space switch the transcript to the row's session, escape
// returns to main while an agent is viewed (otherwise it leaves for the
// editor), tab leaves for the editor.
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
	case key.Matches(msg, m.keyMap.Chat.DigIn), key.Matches(msg, m.keyMap.Chat.Expand):
		return true, m.activateTaskAtCursor()
	case key.Matches(msg, m.keyMap.Chat.ClearHighlight):
		// Escape is the fast way back: from an agent's transcript it
		// returns to main without hunting for the Main row.
		if m.agentView.shown != "" {
			return true, m.viewMainSession()
		}
		return true, m.focusEditorFromTasks()
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
	if m.state == uiChat && m.taskRowCount() > 0 {
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
	if m.state == uiChat && m.taskRowCount() > 0 {
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
// bar as transcript items, the word is styled like a tool group's verb
// (pending while the agent runs, error/cancelled when it did not
// survive), and the description is the child session's generated title
// or, until that lands, the dispatch-prompt excerpt. The list is a
// three-row scrolling window around the cursor.
func (m *UI) renderTasks(width int) string {
	t := m.com.Styles
	m.taskRows = nil
	m.clampTaskCursor()
	n := m.taskRowCount()
	if n == 0 {
		return ""
	}
	focused := m.focus == uiFocusTasks
	focusedPrefix := t.Messages.ToolCallFocused.Render()
	blurredPrefix := t.Messages.ToolCallBlurred.Render()
	prefixWidth := lipgloss.Width(focusedPrefix)

	const windowSize = 3
	start := 0
	if n > windowSize {
		start = max(0, min(m.taskCursor-1, n-windowSize))
	}
	end := min(n, start+windowSize)

	var rows []string
	for row := start; row < end; row++ {
		task, isMain := m.taskRowAt(row)
		onCursor := focused && row == m.taskCursor
		prefix := blurredPrefix
		if onCursor {
			prefix = focusedPrefix
		}
		// Both rows share the accent color: agent rows must pop out of
		// the grey tool-call surroundings they sit beside, not blend
		// into them. The glyph tells the main session from a subagent.
		icon := t.Tool.AgentIcon.Render(styles.AgentIcon)
		var word lipgloss.Style
		var label string
		switch {
		case isMain:
			icon = t.Tool.AgentIcon.Render(styles.MainAgentIcon)
			word, label = t.Tool.NameNormal, "Main Agent"
			if onCursor {
				word = t.Tool.NameNormalSelected
			}
		default:
			running := task.status == subagents.StatusRunning || task.status == subagents.StatusRetrying
			word = chat.GroupVerbStyle(t, running,
				task.status == subagents.StatusCancelled,
				boolToInt(task.status == subagents.StatusFailed),
				boolToInt(task.status == subagents.StatusCompleted),
				onCursor)
			label = "Agent"
		}
		line := icon + " " + word.Render(label)
		if !isMain && task.description != "" {
			line += " " + t.Tool.Body.Render(task.description)
		}
		rows = append(rows, prefix+ansi.Truncate(line, max(width-prefixWidth, 1), "…"))
	}

	// Record row bounds for click handling: one line per row.
	for row := start; row < end; row++ {
		toolCallID := ""
		if task, isMain := m.taskRowAt(row); !isMain && task != nil {
			toolCallID = task.toolCallID
		}
		m.taskRows = append(m.taskRows, taskRow{yStart: row - start, yEnd: row - start + 1, toolCallID: toolCallID})
	}

	// The strip borrows the tool group's row style (the one behind
	// "Ran/Running (N tool calls)"), so subagent rows read like the
	// transcript's tool-call rows they sit beside.
	return t.Tool.Body.Render(strings.Join(rows, "\n"))
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// handleTaskClick moves the cursor onto the clicked row and activates
// it: the transcript switches to the clicked agent's session, or back
// to main from the Main row. It reports whether a row was hit.
func (m *UI) handleTaskClick(_, y int) (bool, tea.Cmd) {
	if len(m.taskRows) == 0 {
		return false, nil
	}
	for _, row := range m.taskRows {
		if y >= row.yStart && y < row.yEnd {
			if row.toolCallID == "" {
				m.taskCursor, m.lastTaskFocusID = 0, ""
			} else {
				for i, task := range m.agentTasks {
					if task.toolCallID == row.toolCallID {
						m.taskCursor = i + m.mainRowCount()
						m.lastTaskFocusID = row.toolCallID
						break
					}
				}
			}
			return true, m.activateTaskAtCursor()
		}
	}
	return false, nil
}
