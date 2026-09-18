package common

import (
	tea "charm.land/bubbletea/v2"
)

// OffsetCursor shifts an input cursor by the given cell offsets,
// returning nil when the cursor is nil. x and y are the origin of
// the area or view the input was drawn in (0 when the cursor is
// already relative to it), leftWidth accounts for prefixes drawn to
// the left of the input (cursor bars, prompt glyphs, frame borders
// and padding), and topRows for rows drawn above it (titles,
// messages, scrolled-away lines). Every input component places its
// cursor through this function so the arithmetic cannot drift
// between them.
func OffsetCursor(cur *tea.Cursor, x, y, leftWidth, topRows int) *tea.Cursor {
	if cur == nil {
		return nil
	}
	c := *cur
	c.X += x + leftWidth
	c.Y += y + topRows
	return &c
}
