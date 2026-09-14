package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/chat"
)

// liveFlowUI returns a UI wired like a live session (a workspace stub
// with a config, so appendSessionMessage can build info footers).
func liveFlowUI() *UI {
	u := newTestUI()
	u.state = uiChat
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}
	return u
}

func groupsIn(u *UI) []*chat.ToolGroupMessageItem {
	var groups []*chat.ToolGroupMessageItem
	for i := range u.chat.Len() {
		if g, ok := u.chat.list.ItemAt(i).(*chat.ToolGroupMessageItem); ok {
			groups = append(groups, g)
		}
	}
	return groups
}

// TestLiveFlowStreamingFolds is the fragmentation regression: assistant
// messages stream in empty (their placeholder text item arrives first),
// tool calls land in updates, and info footers sit between turns. All
// consecutive calls must fold into one run.
func TestLiveFlowStreamingFolds(t *testing.T) {
	t.Parallel()
	u := liveFlowUI()

	m1 := message.Message{ID: "m1", Role: message.Assistant}
	m1Upd := message.Message{ID: "m1", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "a1", Name: "Bash", Input: `{}`, Finished: true},
		message.ToolCall{ID: "a2", Name: "View", Input: `{}`, Finished: true},
	}}
	_ = u.appendSessionMessage(m1)
	_ = u.updateSessionMessage(m1Upd)
	u.chat.AppendMessages(chat.NewAssistantInfoItem(u.com.Styles, &m1Upd, &config.Config{}, time.Time{}))

	m2 := message.Message{ID: "m2", Role: message.Assistant}
	m2Upd := message.Message{ID: "m2", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "b1", Name: "Bash", Input: `{}`, Finished: true},
		message.ToolCall{ID: "b2", Name: "View", Input: `{}`, Finished: true},
		message.ToolCall{ID: "b3", Name: "Grep", Input: `{}`, Finished: true},
	}}
	_ = u.appendSessionMessage(m2)
	_ = u.updateSessionMessage(m2Upd)

	groups := groupsIn(u)
	require.Len(t, groups, 1, "consecutive turns without text must fold into one run")
	assert.Len(t, groups[0].ToolChildren(), 5)
}

// TestLiveFlowBatchFolds covers providers that deliver whole messages:
// each created event already carries its tool calls.
func TestLiveFlowBatchFolds(t *testing.T) {
	t.Parallel()
	u := liveFlowUI()

	m1 := message.Message{ID: "m1", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "a1", Name: "Bash", Input: `{}`, Finished: true},
		message.ToolCall{ID: "a2", Name: "View", Input: `{}`, Finished: true},
	}}
	m2 := message.Message{ID: "m2", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "b1", Name: "Bash", Input: `{}`, Finished: true},
	}}
	_ = u.appendSessionMessage(m1)
	_ = u.appendSessionMessage(m2)

	groups := groupsIn(u)
	require.Len(t, groups, 1)
	assert.Len(t, groups[0].ToolChildren(), 3)
}

// TestLiveFlowMergeOnRemovedSeparator covers the belt-and-suspenders
// path: if anything is removed from between two runs, the runs merge.
// The separator carries text, since an empty assistant item is the
// turn's working spinner and no longer closes a run at all.
func TestLiveFlowMergeOnRemovedSeparator(t *testing.T) {
	t.Parallel()
	u := liveFlowUI()

	separator := chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
		ID:    "m-sep",
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "between runs"}},
	})
	u.chat.AppendMessages(newToolItemForGroup(u, "a1"))
	u.chat.AppendMessages(separator)
	u.chat.AppendMessages(newToolItemForGroup(u, "b1"))

	require.Len(t, groupsIn(u), 2)
	u.chat.RemoveMessage("m-sep")

	groups := groupsIn(u)
	require.Len(t, groups, 1, "removing the separator merges the runs")
	assert.Len(t, groups[0].ToolChildren(), 2)
}

// TestLiveFlowSpinnerStaysLast covers the working spinner: the empty
// assistant item that stands in for the message being generated must
// neither split a run of tool calls nor be left buried above the group
// they folded into.
func TestLiveFlowSpinnerStaysLast(t *testing.T) {
	t.Parallel()
	u := liveFlowUI()

	spinner := chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
		ID: "m-spin", Role: message.Assistant,
	})
	require.True(t, chat.IsWorkingSpinner(spinner), "an empty assistant item is the spinner")

	u.chat.AppendMessages(newToolItemForGroup(u, "a1"))
	u.chat.AppendMessages(spinner)
	u.chat.AppendMessages(newToolItemForGroup(u, "b1"))

	groups := groupsIn(u)
	require.Len(t, groups, 1, "the spinner must not split a run of calls")
	assert.Len(t, groups[0].ToolChildren(), 2)

	last := u.chat.list.ItemAt(u.chat.Len() - 1)
	assert.Same(t, spinner, last, "the spinner belongs at the end of the chat")
}

// TestSpinnerIsNotSelectable covers the selection walk skipping the
// working spinner: it animates, it holds nothing to read or copy, and a
// focus border around it is noise.
func TestSpinnerIsNotSelectable(t *testing.T) {
	t.Parallel()
	u := liveFlowUI()

	text := chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
		ID:    "m-text",
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "done"}},
	})
	spinner := chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
		ID: "m-spin", Role: message.Assistant,
	})
	u.chat.AppendMessages(text, spinner)

	require.False(t, u.chat.isSelectable(u.chat.Len()-1))
	u.chat.SelectLast()
	assert.Equal(t, u.chat.Len()-2, u.chat.list.Selected(), "selection stops on the last real message")
	assert.False(t, u.chat.HasManualSelection(), "the spinner is not the newest selectable item")
}
