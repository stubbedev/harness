package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/chat"
)

// newToolItemForGroup builds a finished shell tool item for grouping tests.
func newToolItemForGroup(u *UI, id string) chat.MessageItem {
	return chat.NewToolMessageItem(
		u.com.Styles,
		"msg",
		message.ToolCall{ID: id, Name: "Bash", Input: `{"command":"ls"}`, Finished: true},
		&message.ToolResult{ToolCallID: id, Name: "Bash", Content: "ok"},
		false,
		"/tmp",
	)
}

func TestChatFoldsToolRunsIntoGroups(t *testing.T) {
	t.Parallel()
	u := newTestUI()

	text := chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
		ID: "m-text", Role: message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "working on it"}},
	})
	info := chat.NewAssistantInfoItem(u.com.Styles, &message.Message{ID: "m-a"}, &config.Config{}, time.Time{})

	// tool, tool, info footer, tool (same run), text, tool (new run).
	u.chat.SetMessages(
		newToolItemForGroup(u, "t1"),
		newToolItemForGroup(u, "t2"),
		info,
		newToolItemForGroup(u, "t3"),
		text,
		newToolItemForGroup(u, "t4"),
	)

	// Four list items: group(t1,t2,t3), the trailing info footer, text,
	// group(t4).
	require.Equal(t, 4, u.chat.Len())

	g1, ok := u.chat.list.ItemAt(0).(*chat.ToolGroupMessageItem)
	require.True(t, ok, "first item should be the tool group")
	assert.Len(t, g1.ToolChildren(), 3)

	// Children resolve through the group; unknown IDs do not.
	require.NotNil(t, u.chat.ToolItem("t3"))
	require.NotNil(t, u.chat.ToolItem("t4"))
	assert.Nil(t, u.chat.ToolItem("missing"))

	// A trailing text item closes the run, so the next tool opens a new
	// group; an info footer between appends does not.
	u.chat.AppendMessages(newToolItemForGroup(u, "t5"))
	require.Equal(t, 4, u.chat.Len())
	u.chat.AppendMessages(chat.NewAssistantInfoItem(u.com.Styles, &message.Message{ID: "m-b"}, &config.Config{}, time.Time{}))
	u.chat.AppendMessages(newToolItemForGroup(u, "t6"))
	require.Equal(t, 5, u.chat.Len())
	g2, ok := u.chat.list.ItemAt(3).(*chat.ToolGroupMessageItem)
	require.True(t, ok, "t5 and the footer should not close the run")
	assert.Len(t, g2.ToolChildren(), 3)
}

func TestChatUpdateToolItemMutatesThroughGroup(t *testing.T) {
	t.Parallel()
	u := newTestUI()

	u.chat.SetMessages(newToolItemForGroup(u, "t1"), newToolItemForGroup(u, "t2"))
	group, ok := u.chat.list.ItemAt(0).(*chat.ToolGroupMessageItem)
	require.True(t, ok)

	before := group.Version()
	u.chat.UpdateToolItem("t2", func(item chat.ToolMessageItem) {
		item.SetResult(&message.ToolResult{ToolCallID: "t2", Name: "Bash", Content: "changed", IsError: true})
	})
	require.NotNil(t, u.chat.ToolItem("t2").Result())
	assert.True(t, u.chat.ToolItem("t2").Result().IsError)
	assert.Greater(t, group.Version(), before, "the group must bump so the list re-renders")
}

// TestChatAppendPreservesBatchOrder is the fragmentation regression: a
// batch extracted as [text, tool, tool] must append the text first and
// group only the tools that follow it. Absorbing the batch's tools
// before its text merged them into the previous run and rendered the
// text after its own tool calls.
func TestChatAppendPreservesBatchOrder(t *testing.T) {
	t.Parallel()
	u := newTestUI()

	text := chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
		ID: "m-text", Role: message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "digging in"}},
	})

	// A first run, then a text-separated batch, then more calls that
	// must join the batch's run across an info footer.
	u.chat.AppendMessages(newToolItemForGroup(u, "a1"), newToolItemForGroup(u, "a2"))
	u.chat.AppendMessages(text, newToolItemForGroup(u, "b1"), newToolItemForGroup(u, "b2"))
	u.chat.AppendMessages(
		chat.NewAssistantInfoItem(u.com.Styles, &message.Message{ID: "m-info"}, &config.Config{}, time.Time{}),
		newToolItemForGroup(u, "b3"),
	)

	// List shape: run 1, text, run 2 (with b3 absorbed past the footer),
	// then the footer itself.
	require.Equal(t, 4, u.chat.Len(), "two runs, one text message, one trailing footer")
	g1, ok := u.chat.list.ItemAt(0).(*chat.ToolGroupMessageItem)
	require.True(t, ok)
	assert.Len(t, g1.ToolChildren(), 2)
	_, isText := u.chat.list.ItemAt(1).(*chat.AssistantMessageItem)
	assert.True(t, isText, "the text must render between the two runs")
	g2, ok := u.chat.list.ItemAt(2).(*chat.ToolGroupMessageItem)
	require.True(t, ok)
	assert.Len(t, g2.ToolChildren(), 3, "b3 joins b1/b2's run past the info footer")
	_, isInfo := u.chat.list.ItemAt(3).(*chat.AssistantInfoItem)
	assert.True(t, isInfo, "the footer renders after the group it trailed")
}

// TestGroupSubCursorNavigation covers the keyboard flow inside a group:
// expand, descend into the calls, expand one call to its full view, and
// leave via focus loss which clears the sub-cursor.
func TestGroupSubCursorNavigation(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.chat.SetMessages(newToolItemForGroup(u, "t1"), newToolItemForGroup(u, "t2"))
	u.chat.Focus()
	u.chat.SelectLast()

	group, ok := u.chat.list.SelectedItem().(*chat.ToolGroupMessageItem)
	require.True(t, ok)

	// Enter expands the run to one-liners and drops the cursor on the
	// first call.
	u.chat.EnterSelectedItem()
	require.True(t, group.ExpandedLevel())
	require.Equal(t, 0, group.SelectedChild())

	// Space expands exactly that one call to its full view.
	u.chat.ToggleExpandedSelectedItem()
	assert.True(t, chat.ShowsFullView(u.chat.ToolItem("t1")), "the sub-cursor's call renders its full view")
	assert.False(t, chat.ShowsFullView(u.chat.ToolItem("t2")))

	// Escape collapses t1's full view, down to the second call, enter
	// there expands that call without resetting the cursor.
	require.True(t, u.chat.AscendSelectedItem())
	assert.False(t, chat.ShowsFullView(u.chat.ToolItem("t1")))
	require.True(t, u.chat.SubCursorDown())
	require.Equal(t, 1, group.SelectedChild())
	u.chat.EnterSelectedItem()
	assert.True(t, chat.ShowsFullView(u.chat.ToolItem("t2")), "enter expands the call under the cursor")
	require.Equal(t, 1, group.SelectedChild(), "the cursor stays on the entered call")

	// Up parks back on the group row.
	require.True(t, u.chat.SubCursorUp())
	require.True(t, u.chat.SubCursorUp())
	require.Equal(t, -1, group.SelectedChild())
	require.False(t, u.chat.SubCursorUp(), "up from the group row falls through to list navigation")

	// Escape climbs out one level at a time: the open group, then
	// nothing left to consume.
	require.True(t, u.chat.AscendSelectedItem())
	require.False(t, group.ExpandedLevel())
	require.False(t, u.chat.AscendSelectedItem())

	// Losing focus clears the sub-cursor.
	u.chat.EnterSelectedItem()
	require.True(t, u.chat.SubCursorDown())
	u.chat.Blur()
	group.SetFocused(false)
	require.Equal(t, -1, group.SelectedChild())
}

// TestCollapseRestoresView is the "reset the render after collapsing"
// regression: expanding a group pushes later content off-screen, and
// collapsing it (via escape) must re-anchor the view on the group so
// that content comes back instead of staying scrolled past it.
func TestCollapseRestoresView(t *testing.T) {
	t.Parallel()
	u := newTestUI()

	// A group followed by enough text items to fill the viewport.
	u.chat.SetMessages(
		newToolItemForGroup(u, "t1"),
		newToolItemForGroup(u, "t2"),
	)
	for i := range 30 {
		_ = i
		u.chat.AppendMessages(chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
			ID:   fmt.Sprintf("m-%d", i),
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: fmt.Sprintf("filler line %d", i)},
			},
		}))
	}
	u.chat.SetSize(80, 10)
	u.chat.Focus()
	u.chat.SetSelected(0)

	// Expand the run, then scroll down as the expanded content pushes
	// the filler off-screen.
	u.chat.EnterSelectedItem()
	require.True(t, u.chat.list.SelectedItem().(*chat.ToolGroupMessageItem).ExpandedLevel())
	u.chat.ScrollBy(15)

	offsetBefore, _ := u.chat.list.ScrollPosition()
	require.Greater(t, offsetBefore, 0, "the expanded content should have scrolled the view")

	// Escaping out re-anchors the group: the offset returns to it
	// rather than staying wherever the expansion pushed the view.
	require.True(t, u.chat.AscendSelectedItem())
	g := u.chat.list.SelectedItem().(*chat.ToolGroupMessageItem)
	require.False(t, g.ExpandedLevel())
	offsetAfter, _ := u.chat.list.ScrollPosition()
	assert.Equal(t, 0, offsetAfter, "collapsing must restore the view to the group")
}
