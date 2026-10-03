package list

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type plainItem struct{ text string }

func (i plainItem) Render(width int) string { return i.text }
func (i plainItem) Version() uint64         { return 0 }
func (i plainItem) Finished() bool          { return false }
func (i plainItem) Filter() string          { return i.text }

// TestList_SetItemsRecoversFromEmptyOffset pins the /mcp-servers bug:
// a list born empty carries the offsetIdx=-1 empty sentinel, and the
// next SetItems with items must leave the viewport at the top instead
// of keeping -1, which made Render start at renderItemEntry(-1) and
// draw nothing — every server manager dialog rendered blank.
func TestList_SetItemsRecoversFromEmptyOffset(t *testing.T) {
	t.Parallel()

	l := NewFilterableList()
	require.Empty(t, l.Render(), "an empty list renders nothing")

	l.SetSize(40, 5)
	l.SetItems(plainItem{text: "alpha"}, plainItem{text: "beta"})

	out := l.Render()
	require.Contains(t, out, "alpha", "items added after an empty birth must render")
	require.Contains(t, out, "beta")
	idx, _ := l.ScrollPosition()
	require.Equal(t, 0, idx, "the viewport sits at the top")
}

// TestList_RenderOutputStableAfterEmptyBirth guards the reverse: an
// always-non-empty list is unaffected by the clamp.
func TestList_RenderOutputStableAfterEmptyBirth(t *testing.T) {
	t.Parallel()

	l := NewFilterableList(plainItem{text: "only"})
	l.SetSize(40, 5)
	require.Equal(t, "only", strings.TrimRight(l.Render(), "\n"))
}
