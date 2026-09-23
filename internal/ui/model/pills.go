package model

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/chat"
	"github.com/stubbedev/harness/internal/ui/styles"
)

const (
	// pillHeightWithBorder is the height of a pill including its border.
	pillHeightWithBorder = 3
	// maxTaskDisplayLength is the maximum length of a task name in the pill.
	maxTaskDisplayLength = 40
)

// hasIncompleteTodos returns true if there are any non-completed todos.
func hasIncompleteTodos(todos []session.Todo) bool {
	return session.HasIncompleteTodos(todos)
}

// hasInProgressTodo returns true if there is at least one in-progress todo.
func hasInProgressTodo(todos []session.Todo) bool {
	for _, todo := range todos {
		if todo.Status == session.TodoStatusInProgress {
			return true
		}
	}
	return false
}

// todoPill renders the todo progress pill with optional spinner and task name.
// Pills always render with a border.
func todoPill(todos []session.Todo, spinnerView string, panelFocused bool, t *styles.Styles) string {
	if !hasIncompleteTodos(todos) {
		return ""
	}

	completed := 0
	var currentTodo *session.Todo
	for i := range todos {
		switch todos[i].Status {
		case session.TodoStatusCompleted:
			completed++
		case session.TodoStatusInProgress:
			if currentTodo == nil {
				currentTodo = &todos[i]
			}
		}
	}

	total := len(todos)

	label := t.Pills.TodoLabel.Render("To-Do")
	progress := t.Pills.TodoProgress.Render(fmt.Sprintf("%d/%d", completed, total))

	var content string
	if panelFocused {
		content = fmt.Sprintf("%s %s", label, progress)
	} else if currentTodo != nil {
		taskText := currentTodo.Content
		if currentTodo.ActiveForm != "" {
			taskText = currentTodo.ActiveForm
		}
		if ansi.StringWidth(taskText) > maxTaskDisplayLength {
			taskText = ansi.Truncate(taskText, maxTaskDisplayLength-1, "…")
		}
		task := t.Pills.TodoCurrentTask.Render(taskText)
		content = fmt.Sprintf("%s %s %s  %s", spinnerView, label, progress, task)
	} else {
		content = fmt.Sprintf("%s %s", label, progress)
	}

	return t.Pills.Focused.Render(content)
}

// todoList renders the expanded todo list.
func todoList(sessionTodos []session.Todo, spinnerView string, t *styles.Styles, width int) string {
	return chat.FormatTodosList(t, sessionTodos, spinnerView, width)
}

// pillsHeightReasonableTerminalHeight is the minimum terminal height at which
// we auto-expand pills when there are incomplete todos.
const pillsHeightReasonableTerminalHeight = 40

// autoExpandPillsIfReasonable expands the pills panel if the terminal has
// enough vertical space to show the expanded list comfortably.
func (m *UI) autoExpandPillsIfReasonable() {
	if !m.hasSession() {
		return
	}
	if m.activeInline != nil {
		return
	}
	if m.height < pillsHeightReasonableTerminalHeight {
		return
	}
	if !hasIncompleteTodos(m.session.Todos) {
		return
	}
	if m.pillsExpanded {
		return
	}
	if m.pillsAutoExpanded {
		return
	}
	m.pillsExpanded = true
	m.pillsAutoExpanded = true
	m.updateLayoutAndSize()
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
	}
}

// togglePillsExpanded toggles the pills panel expansion state.
func (m *UI) togglePillsExpanded() {
	if !m.hasSession() {
		return
	}
	if !hasIncompleteTodos(m.session.Todos) {
		return
	}
	m.pillsExpanded = !m.pillsExpanded
	m.updateLayoutAndSize()

	// Make sure to follow scroll if follow is enabled when toggling pills.
	// Note: uses ScrollToBottom (no scrollbar) since this is layout adjustment,
	// not user-initiated scrolling.
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
	}
}

// pillsAreaHeight calculates the total height needed for the pills area.
func (m *UI) pillsAreaHeight() int {
	if !m.hasSession() {
		return 0
	}
	// Suppress pills when an inline editor (e.g. question form) is active
	// to avoid competing for screen space.
	if m.activeInline != nil {
		return 0
	}
	if !hasIncompleteTodos(m.session.Todos) {
		return 0
	}

	pillsAreaHeight := pillHeightWithBorder
	if m.pillsExpanded {
		pillsAreaHeight += len(m.session.Todos)
	}
	return pillsAreaHeight
}

// renderPills renders the pills panel and stores it in m.pillsView.
func (m *UI) renderPills() {
	m.pillsView = ""
	if !m.hasSession() {
		return
	}
	// Suppress pills when an inline editor (e.g. question form) is active.
	if m.activeInline != nil {
		return
	}

	width := m.layout.pills.Dx()
	if width <= 0 {
		return
	}

	paddingLeft := 3
	contentWidth := max(width-paddingLeft, 0)

	if !hasIncompleteTodos(m.session.Todos) {
		return
	}

	t := m.com.Styles

	inProgressIcon := t.Tool.TodoInProgressIcon.Render(styles.SpinnerIcon)
	if m.todoIsSpinning {
		inProgressIcon = m.todoSpinner.View()
	}

	pill := todoPill(m.session.Todos, inProgressIcon, m.pillsExpanded, t)

	var expandedList string
	if m.pillsExpanded {
		expandedList = todoList(m.session.Todos, inProgressIcon, t, contentWidth)
	}

	pillsRow := pill

	helpDesc := "open"
	if m.pillsExpanded {
		helpDesc = "close"
	}
	helpKey := t.Pills.HelpKey.Render(m.keyMap.Chat.TogglePills.Help().Key)
	helpText := t.Pills.HelpText.Render(helpDesc)
	helpHint := lipgloss.JoinHorizontal(lipgloss.Center, helpKey, " ", helpText)
	pillsRow = lipgloss.JoinHorizontal(lipgloss.Center, pillsRow, " ", helpHint)

	pillsArea := pillsRow
	if expandedList != "" {
		pillsArea = lipgloss.JoinVertical(lipgloss.Left, pillsRow, expandedList)
	}

	m.pillsView = t.Pills.Area.MaxWidth(width).PaddingLeft(paddingLeft).Render(pillsArea)
}
