package model

import (
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/subagents"
)

func mouseMotion(x, y int) tea.MouseMotionMsg {
	return tea.MouseMotionMsg(tea.Mouse{X: x, Y: y})
}

// TestChatHoverMarksClickableItem pins the transcript affordance: the
// item under the pointer carries the hover bar, a repeated motion over
// the same item does not churn, and leaving it drops the mark.
func TestChatHoverMarksClickableItem(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.chat.SetMessages("", commandToolItem(u, "t1", "ls"))
	u.chat.SetSize(80, 8)
	blurred := u.chat.list.Render()

	require.True(t, u.chat.HandleMouseMove(0, 0))
	hovered := u.chat.list.Render()
	require.NotEqual(t, blurred, hovered)
	require.Contains(t, hovered, u.com.Styles.Messages.ToolCallHovered.Render())

	require.False(t, u.chat.HandleMouseMove(0, 0), "staying on the same item must not churn the render")

	require.True(t, u.chat.HandleMouseMove(0, 7), "moving off the item must clear the mark")
	require.Equal(t, blurred, u.chat.list.Render())
}

// TestChatHoverIgnoresDragAndClearsOnRebuild pins the two ways a hover
// mark would go stale: a selection drag owns the pointer, and a rebuilt
// list must not keep the old index.
func TestChatHoverIgnoresDragAndClearsOnRebuild(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.chat.SetMessages("", commandToolItem(u, "t1", "ls"))
	u.chat.SetSize(80, 8)
	blurred := u.chat.list.Render()

	u.chat.mouseDown = true
	require.False(t, u.chat.HandleMouseMove(0, 0), "motion during a drag belongs to selection")
	require.Equal(t, blurred, u.chat.list.Render())

	u.chat.mouseDown = false
	require.True(t, u.chat.HandleMouseMove(0, 0))
	u.chat.SetMessages("", commandToolItem(u, "t2", "pwd"))
	require.Equal(t, -1, u.chat.hoveredIdx, "a rebuilt list drops the old hover index")
	require.NotContains(t, u.chat.list.Render(), u.com.Styles.Messages.ToolCallHovered.Render())
}

// TestTasksStripHoverPrefersCursor pins the strip affordance: the row
// under the pointer gets the hover bar, and the keyboard cursor wins
// when both point at the same row.
func TestTasksStripHoverPrefersCursor(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.agentTasks = []*agentTask{{
		toolCallID:  "a1",
		description: "Audit the dispatch path",
		status:      subagents.StatusRunning,
	}}
	u.taskHoverY = 0

	hovered := u.renderTasks(80)
	require.Contains(t, hovered, u.com.Styles.Messages.ToolCallHovered.Render())

	u.focus = uiFocusTasks
	u.taskCursor = 0
	focused := u.renderTasks(80)
	require.Contains(t, focused, u.com.Styles.Messages.ToolCallFocused.Render())
	require.NotContains(t, focused, u.com.Styles.Messages.ToolCallHovered.Render())

	u.taskHoverY = -1
	require.NotContains(t, u.renderTasks(80), u.com.Styles.Messages.ToolCallHovered.Render())
}

// TestUpdateHoverRoutesRegions pins the pointer mapping: the transcript
// and the strip each get the mark only while the pointer is inside them.
func TestUpdateHoverRoutesRegions(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.chat.SetMessages("", commandToolItem(u, "t1", "ls"))
	u.chat.SetSize(80, 8)
	u.agentTasks = []*agentTask{{toolCallID: "a1", description: "one", status: subagents.StatusRunning}}
	u.layout.main = image.Rect(0, 0, 80, 8)
	u.layout.tasks = image.Rect(0, 8, 80, 9)

	u.updateHover(mouseMotion(0, 0))
	require.Equal(t, -1, u.taskHoverY)
	require.NotEqual(t, -1, u.chat.hoveredIdx)

	u.updateHover(mouseMotion(0, 8))
	require.Equal(t, 0, u.taskHoverY)
	require.Equal(t, -1, u.chat.hoveredIdx)

	u.updateHover(mouseMotion(0, 20))
	require.Equal(t, -1, u.taskHoverY)
	require.Equal(t, -1, u.chat.hoveredIdx)
}

// TestHoverMotionReusesFrameWhenNothingChanged pins the cost of
// all-motion mouse reporting: a pointer wandering within the same item
// changes nothing, so the update stays eligible for the memoized frame
// instead of forcing a full redraw per motion event.
func TestHoverMotionReusesFrameWhenNothingChanged(t *testing.T) {
	t.Parallel()

	u := newFrameTestUI(t)
	u.chat.SetMessages("", commandToolItem(u, "t1", "ls"))
	u.updateLayoutAndSize()

	_, _ = u.Update(mouseMotion(u.layout.main.Min.X, u.layout.main.Min.Y))
	require.False(t, u.scrollOnlyUpdate, "the first hover change must redraw")

	_, _ = u.Update(mouseMotion(u.layout.main.Min.X, u.layout.main.Min.Y))
	require.True(t, u.scrollOnlyUpdate, "an unchanged hover must reuse the frame")
}
