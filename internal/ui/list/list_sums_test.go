package list

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// resizingItem renders a body whose line count follows its content, so
// tests can change an item's height the way a streaming item or a
// markdown reflow does.
type resizingItem struct {
	*Versioned
	id     string
	body   string
	hits   int
	called func()
}

func (r *resizingItem) Render(width int) string {
	r.hits++
	if r.called != nil {
		r.called()
	}
	return strings.Repeat("x\n", r.lines()-1) + "x"
}

func (r *resizingItem) lines() int {
	n, err := strconv.Atoi(r.body)
	if err != nil {
		return 1
	}
	return n
}

func (r *resizingItem) Finished() bool { return true }

// referenceTotalHeight computes the total height the way the pre-sums
// implementation did: render every item and add the heights, one gap
// between adjacent items.
func referenceTotalHeight(l *List, lines []int) int {
	total := 0
	for idx := range lines {
		total += lines[idx]
		if l.gap > 0 && idx < len(lines)-1 {
			total += l.gap
		}
	}
	return total
}

// referenceOffset computes the scroll offset the way the pre-sums
// implementation did.
func referenceOffset(l *List, lines []int) int {
	offset := 0
	for idx := 0; idx < l.offsetIdx && idx < len(lines); idx++ {
		offset += lines[idx]
		if l.gap > 0 && idx < len(lines)-1 {
			offset += l.gap
		}
	}
	return offset + l.offsetLine
}

// heightsOf re-derives the per-item line counts of the list's current
// items from their rendered content.
func heightsOf(l *List) []int {
	lines := make([]int, len(l.items))
	for idx, item := range l.items {
		lines[idx] = strings.Count(item.Render(l.width), "\n") + 1
	}
	return lines
}

// TestList_SumsTrackMutations pins that TotalHeight and Offset always
// equal a from-scratch reference walk, across appends, removals,
// prepends, gap changes, resizes and height-changing re-renders.
func TestList_SumsTrackMutations(t *testing.T) {
	t.Parallel()

	a := &resizingItem{Versioned: NewVersioned(), id: "a", body: "3"}
	b := &resizingItem{Versioned: NewVersioned(), id: "b", body: "5"}
	c := &resizingItem{Versioned: NewVersioned(), id: "c", body: "2"}

	l := NewList()
	l.SetSize(40, 10)
	l.SetGap(1)
	l.AppendItems(a, b)

	// Warm the sums with one query, then mutate and re-check. A
	// version-bumped item's new height is discovered by the frame's
	// render, so render before querying.
	require.Equal(t, 9, l.TotalHeight())
	a.body = "7"
	a.Bump()
	l.SetSize(40, 1000)
	_ = l.Render()
	require.Equal(t, 13, l.TotalHeight(), "a height change must re-base the sums")

	l.AppendItems(c)
	require.Equal(t, 16, l.TotalHeight())

	l.RemoveItem(1)
	require.Equal(t, 10, l.TotalHeight(), "removal must re-base the sums from the removed index")

	l.PrependItems(b)
	require.Equal(t, 16, l.TotalHeight())

	l.SetGap(2)
	require.Equal(t, 18, l.TotalHeight(), "a gap change must re-base the sums")

	l.SetSize(40, 10)
	l.SetItems(a, c)
	require.Equal(t, 11, l.TotalHeight())

	// Offset: scroll and compare against the reference at each step.
	l.SetItems(a, b, c)
	require.Equal(t, 18, l.TotalHeight())
	for idx := range 3 {
		l.ScrollToIndex(idx)
		require.Equal(t, referenceOffset(l, []int{7, 5, 2}), l.Offset(), "offset at idx %d", idx)
	}
}

// check mirrors a frame: the list renders (discovering re-renders and
// any height changes), then the scrollbar queries the sums. The tall
// viewport makes Render walk every item, which is the discovery the
// reference walk below assumes.
func (l *List) check(t *testing.T, step string) {
	t.Helper()
	l.SetSize(40, 1000)
	_ = l.Render()
	require.Equal(t, referenceTotalHeight(l, heightsOf(l)), l.TotalHeight(), "total height after %s", step)
	require.Equal(t, referenceOffset(l, heightsOf(l)), l.Offset(), "offset after %s", step)
}

// TestList_SumsMatchReferenceAfterEveryMutation drives a scripted
// sequence of renders and mutations, checking both sums against the
// reference walk after each step so an unnoticed re-base bug cannot
// survive.
func TestList_SumsMatchReferenceAfterEveryMutation(t *testing.T) {
	t.Parallel()

	items := make([]*resizingItem, 0, 8)
	l := NewList()
	l.SetSize(40, 10)
	l.SetGap(1)

	for i := range 6 {
		items = append(items, &resizingItem{Versioned: NewVersioned(), id: "i", body: strconv.Itoa(i%4 + 1)})
		l.AppendItems(items[len(items)-1])
		l.check(t, "append "+strconv.Itoa(i))
	}

	// Re-render an item taller: its height changes at a version bump.
	items[2].body = "9"
	items[2].Bump()
	l.check(t, "grow item 2")

	items[2].body = "1"
	items[2].Bump()
	l.check(t, "shrink item 2")

	l.RemoveItem(4)
	l.check(t, "remove item 4")

	l.ScrollToIndex(2)
	l.check(t, "scroll")

	l.PrependItems(&resizingItem{Versioned: NewVersioned(), id: "p", body: "4"})
	l.check(t, "prepend")

	l.SetItems(items[0], items[1], items[2])
	l.ScrollToBottom()
	l.check(t, "set items + scroll to bottom")
}

// TestList_TotalHeightDoesNotReRenderPinnedItems pins the point of the
// sums: once built, frames that only re-render a bottom item (an
// animation tick that does not change its height) must not re-render
// earlier items to answer TotalHeight or Offset.
func TestList_TotalHeightDoesNotReRenderPinnedItems(t *testing.T) {
	t.Parallel()

	items := make([]*resizingItem, 6)
	l := NewList()
	l.SetSize(40, 10)
	l.SetGap(1)
	for i := range items {
		items[i] = &resizingItem{Versioned: NewVersioned(), id: "i", body: "2"}
		l.AppendItems(items[i])
	}

	require.Equal(t, 17, l.TotalHeight())
	first := make([]int, len(items))
	for i, it := range items {
		first[i] = it.hits
	}

	// An animation tick on the last item: re-render, same height.
	items[len(items)-1].Bump()
	require.Equal(t, 17, l.TotalHeight())
	require.Equal(t, 0, items[0].hits-first[0], "the first item must not be re-rendered by TotalHeight")

	l.ScrollToIndex(len(items) - 1)
	// heightsOf renders the items itself, so take the hit snapshot
	// after the reference is computed.
	ref := referenceOffset(l, heightsOf(l))
	first = make([]int, len(items))
	for i, it := range items {
		first[i] = it.hits
	}
	require.Equal(t, ref, l.Offset())
	require.Equal(t, 0, items[0].hits-first[0], "the first item must not be re-rendered by Offset")
}
