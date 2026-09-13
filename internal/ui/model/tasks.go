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
	task := m.agentTaskByToolCall(tc.ID)
	if task == nil {
		task = &agentTask{
			toolCallID: tc.ID,
			status:     subagents.StatusRunning,
			startedAt:  time.Now(),
		}
		m.agentTasks = append(m.agentTasks, task)
	}

	var params agent.AgentParams
	_ = json.Unmarshal([]byte(tc.Input), &params)
	task.name = params.SubagentType
	if task.name == "" {
		task.name = subagentDisplayName
	}
	if task.color == "" {
		task.color = subagents.AutoColor(task.name)
	}
	task.prompt = params.Prompt

	if m.tasksSpinning() {
		m.syncSubagentWaitItem()
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
// tool call cannot re-register it.
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
	m.syncSubagentWaitItem()
}

// subagentDisplayName is the strip title for a dispatch that did not
// name a subagent type.
const subagentDisplayName = "subagent"

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
	m.taskRows = nil
	m.syncSubagentWaitItem()
}

// syncSubagentWaitItem keeps the transcript's "Waiting for N
// subagents" entry in step with the running set: appended and animated
// while dispatches are in flight, removed once they all settle. The
// timer counts from the oldest running dispatch.
func (m *UI) syncSubagentWaitItem() {
	if m.state != uiChat || m.chat == nil {
		return
	}
	if !m.tasksSpinning() {
		if m.chat != nil && m.chat.MessageItem(chat.SubagentWaitID) != nil {
			m.chat.RemoveMessage(chat.SubagentWaitID)
		}
		return
	}
	oldest := time.Now()
	for _, t := range m.agentTasks {
		if t.status == subagents.StatusRunning || t.status == subagents.StatusRetrying {
			if t.startedAt.Before(oldest) {
				oldest = t.startedAt
			}
		}
	}
	if item, ok := m.chat.MessageItem(chat.SubagentWaitID).(*chat.SubagentWaitItem); ok {
		item.Update(len(m.agentTasks), oldest)
		return
	}
	m.chat.AppendMessages(chat.NewSubagentWaitItem(m.com.Styles, len(m.agentTasks), oldest))
	// Subagent waits can outlive the parent's own streaming traffic;
	// keep the animation clock allowed so the spinner stays live.
	m.chat.SetAnimationsAllowed(true)
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
	}
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
			_, hasResult := toolResults[tc.ID]
			canceled := msg.FinishReason() == message.FinishReasonCanceled
			// A set Finished flag only means the model finished emitting
			// the call; the subagent runs until a result lands. Skip only
			// dispatches that settled, or that cannot be running because
			// the session is idle.
			if hasResult || canceled || !busy {
				continue
			}
			task := &agentTask{
				toolCallID: tc.ID,
				startedAt:  time.Unix(msg.CreatedAt, 0),
				status:     subagents.StatusRunning,
			}
			var params agent.AgentParams
			_ = json.Unmarshal([]byte(tc.Input), &params)
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
	m.clampTaskCursor()
	m.syncSubagentWaitItem()
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
		for _, item := range chat.ExtractMessageItems(m.com.Styles, nestedMsg, resultMap, m.com.Workspace.WorkingDir()) {
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
		found := false
		for _, existing := range task.nested {
			if existing.ToolCall().ID == tc.ID {
				existing.SetToolCall(tc)
				found = true
				break
			}
		}
		if !found {
			nested := chat.NewToolMessageItem(m.com.Styles, event.ID, tc, nil, false, m.com.Workspace.WorkingDir())
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
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(info.ChildSessionID)
	if !ok {
		return
	}
	task := m.agentTaskByToolCall(toolCallID)
	if task == nil {
		return
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

// focusTasks moves focus to the strip, parking the cursor on the most
// recent task when it had none.
func (m *UI) focusTasks() {
	if len(m.agentTasks) == 0 {
		return
	}
	m.focus = uiFocusTasks
	m.textarea.Blur()
	m.chat.Blur()
	m.clampTaskCursor()
}

// taskCursorDown moves the strip cursor down: through the cursor
// task's nested calls when expanded, otherwise to the next task.
func (m *UI) taskCursorDown() {
	if len(m.agentTasks) == 0 {
		return
	}
	m.clampTaskCursor()
	task := m.agentTasks[m.taskCursor]
	if task.toolCallID == m.expandedTaskID && m.taskSubCursor < len(task.nested)-1 {
		m.taskSubCursor++
		return
	}
	if m.taskCursor < len(m.agentTasks)-1 {
		m.taskCursor++
		m.taskSubCursor = -1
	}
}

// taskCursorUp moves the strip cursor up, mirroring taskCursorDown.
func (m *UI) taskCursorUp() {
	if len(m.agentTasks) == 0 {
		return
	}
	m.clampTaskCursor()
	if m.taskSubCursor > 0 {
		m.taskSubCursor--
		return
	}
	if m.taskSubCursor == 0 {
		m.taskSubCursor = -1
		return
	}
	if m.taskCursor > 0 {
		m.taskCursor--
		task := m.agentTasks[m.taskCursor]
		if task.toolCallID == m.expandedTaskID && len(task.nested) > 0 {
			m.taskSubCursor = len(task.nested) - 1
		} else {
			m.taskSubCursor = -1
		}
	}
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
		toggleNestedFullView(task.nested[m.taskSubCursor])
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

// toggleNestedFullView flips a nested tool call between its compact
// one-liner and its full renderer output, mirroring the chat's
// per-call expansion.
func toggleNestedFullView(nested chat.ToolMessageItem) {
	compact, isCompact := nested.(chat.Compactable)
	if probe, ok := nested.(interface{ IsCompact() bool }); ok && probe.IsCompact() {
		if isCompact {
			compact.SetCompact(false)
		}
		if e, ok := nested.(chat.Expandable); ok && !isToolExpandedInChat(nested) {
			_ = e.ToggleExpanded()
		}
		return
	}
	// Already full: collapse back to the one-liner.
	if e, ok := nested.(chat.Expandable); ok && isToolExpandedInChat(nested) {
		_ = e.ToggleExpanded()
	}
	if isCompact {
		compact.SetCompact(true)
	}
}

func isToolExpandedInChat(t chat.ToolMessageItem) bool {
	if probe, ok := t.(interface{ Expanded() bool }); ok {
		return probe.Expanded()
	}
	return false
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
		if probe, ok := nested.(interface{ IsCompact() bool }); !ok || probe.IsCompact() {
			toggleNestedFullView(nested)
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
func (m *UI) ascendTaskAtCursor() {
	if len(m.agentTasks) == 0 {
		m.focusEditorFromTasks()
		return
	}
	m.clampTaskCursor()
	task := m.agentTasks[m.taskCursor]
	if m.taskSubCursor >= 0 && m.taskSubCursor < len(task.nested) {
		nested := task.nested[m.taskSubCursor]
		if probe, ok := nested.(interface{ IsCompact() bool }); ok && !probe.IsCompact() {
			toggleNestedFullView(nested)
			m.updateLayoutAndSize()
			return
		}
	}
	if m.expandedTaskID == task.toolCallID {
		m.expandedTaskID = ""
		m.taskSubCursor = -1
		m.updateLayoutAndSize()
		return
	}
	m.focusEditorFromTasks()
}

// handleTaskKey processes a keypress while the strip is focused. Arrows
// (plain or shifted) move the cursor within the current level, enter
// goes in, escape goes out, tab leaves for the editor.
func (m *UI) handleTaskKey(msg tea.KeyPressMsg) bool {
	switch {
	case key.Matches(msg, m.keyMap.Chat.Up), key.Matches(msg, m.keyMap.Chat.UpOneItem):
		m.taskCursorUp()
		return true
	case key.Matches(msg, m.keyMap.Chat.Down), key.Matches(msg, m.keyMap.Chat.DownOneItem):
		m.taskCursorDown()
		return true
	case key.Matches(msg, m.keyMap.Chat.DigIn):
		m.enterTaskAtCursor()
		return true
	case key.Matches(msg, m.keyMap.Chat.Expand):
		m.toggleTaskAtCursor()
		return true
	case key.Matches(msg, m.keyMap.Chat.ClearHighlight):
		m.ascendTaskAtCursor()
		return true
	case key.Matches(msg, m.keyMap.Tab):
		m.focusEditorFromTasks()
		return true
	}
	return false
}

func (m *UI) focusEditorFromTasks() {
	m.focus = uiFocusEditor
	_ = m.textarea.Focus()
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
			if tokens := task.promptTokens + task.completionTokens; tokens > 0 {
				meta += " " + t.Resource.AdditionalText.Render(fmt.Sprintf("%d tok", tokens))
			}
			line += " " + t.Resource.AdditionalText.Render(meta)
		}
		if len(task.nested) > 0 {
			line += " " + t.Resource.AdditionalText.Render(fmtToolCalls(len(task.nested)))
		}
		return ansi.Truncate(line, max(width-prefixWidth, 1), "…")
	}

	var rows []string
	for i, task := range visible {
		prefix := blurredPrefix
		if focused && start+i == m.taskCursor {
			prefix = focusedPrefix
		}
		rows = append(rows, prefix+renderRow(task))

		if task.toolCallID == m.expandedTaskID {
			subCursor := -1
			if focused && start+i == m.taskCursor {
				subCursor = m.taskSubCursor
			}
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
// prompt, the subagent's own tool calls as one-liners, and its result.
func (m *UI) renderTaskDetails(task *agentTask, width, subCursor int) []string {
	t := m.com.Styles
	inner := max(width-6, 1)
	var lines []string
	if task.prompt != "" {
		lines = append(lines, "  "+ansi.Truncate("Prompt: "+task.prompt, inner, "…"))
	}
	for j, nested := range task.nested {
		indent := "  "
		if j == subCursor {
			indent = t.Messages.ToolCallSelected.Render()
		}
		if probe, ok := nested.(interface{ IsCompact() bool }); ok && !probe.IsCompact() {
			// Expanded call: RawRender skips the per-item left prefix so
			// the full view keeps the one-liner indentation.
			for ln := range strings.SplitSeq(nested.RawRender(inner), "\n") {
				lines = append(lines, indent+ln)
			}
			continue
		}
		lines = append(lines, indent+nestedOneLiner(t, nested, inner-2))
	}
	if task.result != nil && task.result.Content != "" {
		excerpt := task.result.Content
		if i := strings.IndexByte(excerpt, '\n'); i >= 0 {
			excerpt = excerpt[:i]
		}
		excerpt = strings.Join(strings.Fields(excerpt), " ")
		if excerpt != "" {
			lines = append(lines, "  "+t.Resource.AdditionalText.Render("Result: ")+ansi.Truncate(excerpt, inner-9, "…"))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "  "+t.Resource.AdditionalText.Render("No activity recorded"))
	}
	return lines
}

// nestedOneLiner renders one nested tool call as a single line.
func nestedOneLiner(t *styles.Styles, nested chat.ToolMessageItem, width int) string {
	glyph := t.Tool.IconSuccess.Render()
	if a, ok := nested.(chat.Animatable); ok && a.Spinning() {
		glyph = "…"
	} else if res := nested.Result(); res != nil && res.IsError {
		glyph = t.Tool.IconError.Render()
	}
	name := chat.PrettifyToolName(nested.ToolCall().Name)
	line := glyph + " " + name
	if summary := chat.ToolCallSummary(nested.ToolCall()); summary != "" {
		line += " " + summary
	}
	return ansi.Truncate(line, max(width, 1), "…")
}

// handleTaskClick toggles the expanded task for a click inside the strip
// and moves the keyboard cursor onto the clicked row.
func (m *UI) handleTaskClick(x, y int) bool {
	if len(m.taskRows) == 0 {
		return false
	}
	for _, row := range m.taskRows {
		if y >= row.yStart && y < row.yEnd {
			for i, task := range m.agentTasks {
				if task.toolCallID == row.toolCallID {
					m.taskCursor = i
					m.taskSubCursor = -1
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

func fmtToolCallsExtra(n int) string {
	return fmt.Sprintf("… and %d more tasks", n)
}
