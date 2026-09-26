package model

import (
	"strconv"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/chat"
	"github.com/stubbedev/harness/internal/ui/list"
)

// growingItem is a message item whose content grows over time, simulating
// streaming output, with distinguishable lines.
type growingItem struct {
	id      string
	lines   int
	version uint64
}

func (m *growingItem) ID() string           { return m.id }
func (m *growingItem) Version() uint64      { return m.version }
func (m *growingItem) Finished() bool       { return false }
func (m *growingItem) SetFocused(bool)      {}
func (m *growingItem) Focused() bool        { return false }
func (m *growingItem) RawRender(int) string { return m.Render(80) }
func (m *growingItem) Render(width int) string {
	out := make([]string, m.lines)
	for i := range m.lines {
		out[i] = "grow-" + m.id + "-" + strconv.Itoa(i)
	}
	return strings.Join(out, "\n")
}

var _ chat.MessageItem = (*growingItem)(nil)

// expandableTestItem is an expandable item with distinct collapsed and
// expanded heights.
type expandableTestItem struct {
	id            string
	expanded      bool
	version       uint64
	expandedLines int
}

func (m *expandableTestItem) ID() string           { return m.id }
func (m *expandableTestItem) Version() uint64      { return m.version }
func (m *expandableTestItem) Finished() bool       { return true }
func (m *expandableTestItem) SetFocused(bool)      {}
func (m *expandableTestItem) Focused() bool        { return false }
func (m *expandableTestItem) RawRender(int) string { return m.Render(80) }
func (m *expandableTestItem) Render(width int) string {
	count := 1
	prefix := "col"
	if m.expanded {
		count = m.expandedLines
		if count == 0 {
			count = 30
		}
		prefix = "exp"
	}
	out := make([]string, count)
	for i := range count {
		out[i] = prefix + "-" + m.id + "-" + strconv.Itoa(i)
	}
	return strings.Join(out, "\n")
}

func (m *expandableTestItem) ToggleExpanded() bool {
	m.SetExpansionLevel(1 - m.ExpansionLevel())
	return m.expanded
}

func (m *expandableTestItem) ExpansionLevel() uint8 {
	if m.expanded {
		return 1
	}
	return 0
}

func (m *expandableTestItem) SetExpansionLevel(level uint8) {
	if level > 1 || (level == 1) == m.expanded {
		return
	}
	m.expanded = level == 1
	m.version++
}

var (
	_ chat.MessageItem = (*expandableTestItem)(nil)
	_ chat.Expandable  = (*expandableTestItem)(nil)
)

// drawChatLines renders the chat into a fresh screen buffer and returns the
// visible text lines.
func drawChatLines(t *testing.T, c *Chat, w, h int) []string {
	t.Helper()
	scr := uv.NewScreenBuffer(w, h)
	c.Draw(scr, uv.Rect(0, 0, w, h))
	rendered := strings.Split(strings.TrimRight(scr.Render(), "\n"), "\n")
	lines := make([]string, len(rendered))
	for i, line := range rendered {
		lines[i] = strings.TrimRight(line, " ")
	}
	return lines
}

// TestChatFollowResticksWhenItemGrowsPastViewport reproduces the second
// scroll bug: while following, a bottom item that grows past the viewport
// height must keep the view pinned to the bottom, not leave the newest
// content above a blank region until the next item arrives.
func TestChatFollowResticksWhenItemGrowsPastViewport(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	tall := &growingItem{id: "tall", lines: 3}
	streaming := &growingItem{id: "stream", lines: 4}
	u.chat.SetMessages("", tall, streaming)
	u.chat.SetSize(80, 10)
	u.chat.ScrollToBottom()
	require.True(t, u.chat.Follow())

	lines := drawChatLines(t, u.chat, 80, 10)
	require.Contains(t, strings.Join(lines, "\n"), "grow-stream-3",
		"pre-growth view must show the streaming item's last line")

	streaming.lines = 25
	streaming.version++

	lines = drawChatLines(t, u.chat, 80, 10)
	require.Equal(t, "grow-stream-24", lines[len(lines)-1],
		"grown item must keep the view pinned to the bottom")
}

// TestChatFollowResticksWhenContentShrinks reproduces the second scroll bug
// from the other direction: while following, content that shrinks (a
// streaming item rewrapping shorter, a placeholder being removed) leaves the
// stale offset "at bottom" with the newest content floated up above a blank
// region. Follow must re-anchor on the next draw, not wait for the next
// append.
func TestChatFollowResticksWhenContentShrinks(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	tall := &growingItem{id: "tall", lines: 3}
	streaming := &growingItem{id: "stream", lines: 20}
	u.chat.SetMessages("", tall, streaming)
	u.chat.SetSize(80, 10)
	u.chat.ScrollToBottom()
	require.True(t, u.chat.Follow())
	require.True(t, u.chat.AtBottom())

	lines := drawChatLines(t, u.chat, 80, 10)
	require.Equal(t, "grow-stream-19", lines[len(lines)-1])

	streaming.lines = 4
	streaming.version++

	lines = drawChatLines(t, u.chat, 80, 10)
	require.Contains(t, strings.Join(lines, "\n"), "grow-stream-3",
		"shrunk content must stay pinned to the bottom, not float above blanks")
	require.Contains(t, strings.Join(lines, "\n"), "grow-tall-0",
		"content scrolled off the top must return once space frees up")
}

// TestChatExpandBottomItemScrollsIntoView reproduces the first scroll bug:
// expanding the bottommost item while following must scroll the expanded
// content into view.
func TestChatExpandBottomItemScrollsIntoView(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	short := &growingItem{id: "short", lines: 2}
	bottom := &expandableTestItem{id: "bottom"}
	u.chat.SetMessages("", short, bottom)
	u.chat.SetSize(80, 10)
	u.chat.ScrollToBottom()
	u.chat.SetSelected(1)

	u.chat.ToggleExpandedSelectedItem()

	lines := drawChatLines(t, u.chat, 80, 10)
	require.Contains(t, strings.Join(lines, "\n"), "exp-bottom-29",
		"expanded tail must be scrolled into view")
}

// TestChatExpandBottomItemScrollsIntoViewWhenNotFollowing is the same
// expansion with follow off (the user had scrolled up earlier): the
// expanded content must still be brought into view instead of growing
// off-screen below the viewport.
func TestChatExpandBottomItemScrollsIntoViewWhenNotFollowing(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	filler := &growingItem{id: "filler", lines: 8}
	bottom := &expandableTestItem{id: "bottom"}
	u.chat.SetMessages("", filler, bottom)
	u.chat.SetSize(80, 10)
	u.chat.ScrollToBottom()
	u.chat.ScrollBy(-3)
	require.False(t, u.chat.Follow())
	u.chat.SetSelected(1)

	u.chat.ToggleExpandedSelectedItem()

	lines := drawChatLines(t, u.chat, 80, 10)
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "exp-bottom-0",
		"expanded item must top-align when taller than the viewport")
	require.Contains(t, joined, "exp-bottom-9",
		"expanded content must be scrolled into view")
}

// TestChatExpandMidViewItemDoesNotJump asserts ScrollItemIntoView's minimal
// scroll: expanding an item that still fits in the viewport must not move
// the viewport at all.
func TestChatExpandMidViewItemDoesNotJump(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	top := &growingItem{id: "top", lines: 3}
	mid := &expandableTestItem{id: "mid", expandedLines: 3}
	u.chat.SetMessages("", top, mid)
	u.chat.SetSize(80, 10)
	u.chat.ScrollToBottom()
	u.chat.ScrollBy(-2)
	u.chat.SetSelected(1)

	before := drawChatLines(t, u.chat, 80, 10)
	u.chat.ToggleExpandedSelectedItem()
	lines := drawChatLines(t, u.chat, 80, 10)

	require.Contains(t, strings.Join(lines, "\n"), "exp-mid-2",
		"expanded content that fits must be visible without scrolling")
	require.Equal(t, before[0], lines[0],
		"expansion that still fits must not move the viewport")
}

func lastRenderedLine(rendered string) string {
	lines := strings.Split(rendered, "\n")
	return lines[len(lines)-1]
}

// TestListScrollItemIntoView exercises the list primitive directly.
func TestListScrollItemIntoView(t *testing.T) {
	t.Parallel()

	l := list.NewList()
	l.SetGap(1)
	l.SetSize(80, 8)
	l.SetItems(
		&growingItem{id: "a", lines: 3},
		&growingItem{id: "b", lines: 3},
		&growingItem{id: "c", lines: 3},
		&growingItem{id: "d", lines: 3},
	)

	// Fully visible item: no-op.
	l.ScrollToIndex(0)
	l.ScrollItemIntoView(1)
	idx, line := l.ScrollPosition()
	require.Equal(t, 0, idx)
	require.Equal(t, 0, line)
	// Item below the fold: scroll down just enough to show its end.
	l.ScrollItemIntoView(3)
	require.True(t, l.AtBottom(), "item d must end on the bottom row")
	require.Equal(t, "grow-d-2", lastRenderedLine(l.Render()))

	// Item above the viewport: top-align.
	l.ScrollItemIntoView(0)
	idx, line = l.ScrollPosition()
	require.Equal(t, 0, idx)
	require.Equal(t, 0, line)
}
