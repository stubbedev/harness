package model

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

func TestFocusRestoringSelection_ReturnsToKeptItem(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.focus = uiFocusEditor
	u.textarea.Focus()
	u.chat.Blur()

	// First visit with no prior selection lands on the newest item.
	_ = u.chat.FocusRestoringSelection()
	require.False(t, u.chat.HasManualSelection())
	require.Equal(t, u.chat.Len()-1, u.chat.Selected())

	// Move to an older item, leave the chat for the editor, and come
	// back: focus returns to that same item instead of the newest.
	u.chat.SetSelected(10)
	require.True(t, u.chat.HasManualSelection())
	u.focus = uiFocusEditor
	u.textarea.Focus()
	u.chat.Blur()
	_ = u.chat.FocusRestoringSelection()
	require.Equal(t, 10, u.chat.Selected(), "focus must return to the last-focused transcript item")
}

func TestFocusTasks_ReturnsToLastFocusedTaskAfterReaps(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		_ = u.upsertAgentTask(msg, agentToolCall(id))
	}

	// Focus the strip and walk the cursor onto the third task.
	u.focusTasks()
	u.taskCursorDown()
	u.taskCursorDown()
	require.Equal(t, 2, u.taskCursor)
	require.Equal(t, "a3", u.lastTaskFocusID)

	// Leave for the editor; while away, an earlier task is reaped and
	// the indices shift.
	u.focusEditorFromTasks()
	u.reapAgentTask("a1")
	u.clampTaskCursor()

	// Returning to the strip lands on the same task, not whatever the
	// shifted index now points at.
	u.focusTasks()
	task := u.agentTaskByToolCall("a3")
	require.NotNil(t, task)
	require.Equal(t, "a3", u.agentTasks[u.taskCursor].toolCallID)

	// Once the remembered task is gone, focus falls back to the
	// clamped cursor without inventing a task.
	u.reapAgentTask("a3")
	u.focusTasks()
	require.GreaterOrEqual(t, u.taskCursor, 0)
	require.Less(t, u.taskCursor, len(u.agentTasks))
}

func TestResetAgentTasks_ClearsTaskFocusMemory(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.state = uiChat

	msg := &message.Message{ID: "m1", Role: message.Assistant}
	_ = u.upsertAgentTask(msg, agentToolCall("a1"))
	_ = u.upsertAgentTask(msg, agentToolCall("a2"))

	u.focusTasks()
	u.taskCursorDown()
	require.Equal(t, "a2", u.lastTaskFocusID)

	u.resetAgentTasks()
	require.Empty(t, u.lastTaskFocusID)
}
