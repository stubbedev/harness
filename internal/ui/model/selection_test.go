package model

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/chat"
)

// TestDragSelectionIncludesPointerCell pins terminal-native selection
// geometry: both the pressed cell and the cell under the pointer are
// inside the selection, so the highlight keeps up with the cursor the
// way Claude Code's does instead of trailing one cell behind. A click
// without movement selects nothing, and word and line selects still
// cover exactly the word or line.
func TestDragSelectionIncludesPointerCell(t *testing.T) {
	u := newTestUI()
	msg := &message.Message{
		ID:   "m1",
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "the quick brown fox jumps over"},
		},
	}
	u.chat.SetMessages(chat.NewUserMessageItem(u.com.Styles, msg, nil))
	u.updateLayoutAndSize()
	c := u.chat

	// "brown" starts at column 14 of the rendered line (border plus
	// padding sit in front of it); the click lands on its first cell.
	const brownCol = 14

	// Drag across columns 10..12 of the item: cells 10, 11 and 12 all
	// highlight.
	c.mouseDown = true
	c.mouseDownItem, c.mouseDownY, c.mouseDownX = 0, 0, 10
	c.mouseDragItem, c.mouseDragY, c.mouseDragX = 0, 0, 12

	si, sl, sc, ei, el, ec := c.getHighlightRange()
	require.Equal(t, 0, si)
	require.Equal(t, 0, ei)
	require.Equal(t, sl, el)
	require.Equal(t, 10, sc)
	require.Equal(t, 13, ec, "pointer cell is included in the selection")

	// Backward drag: the pressed cell is the right edge instead.
	c.mouseDownX, c.mouseDragX = 12, 10
	_, _, sc, _, _, ec = c.getHighlightRange()
	require.Equal(t, 10, sc)
	require.Equal(t, 13, ec, "pressed cell stays included when dragging up")

	// Plain click: no selection, so nothing paints or copies.
	c.mouseDownX, c.mouseDragX = 10, 10
	si, _, _, ei, _, _ = c.getHighlightRange()
	require.Equal(t, -1, si)
	require.Equal(t, -1, ei)
	require.False(t, c.HasHighlight())
	require.Empty(t, c.HighlightContent())

	// Word select still covers exactly the word: the stored end is the
	// word's last cell and the extension lands one past it, so the
	// copied text is the word alone.
	c.selectWord(0, brownCol+chat.MessageLeftPaddingTotal, 0)
	require.True(t, c.HasHighlight())
	_ = renderToBuffer(t, c, 80, 20)
	require.Equal(t, "brown", c.HighlightContent())

	// Line select still covers the whole line.
	c.selectLine(0, 0)
	_ = renderToBuffer(t, c, 80, 20)
	require.Equal(t, "the quick brown fox jumps over", c.HighlightContent())
}
