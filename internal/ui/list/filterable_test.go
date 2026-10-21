package list

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// idItem is a filterable item with a stable ID, rebuilt as a fresh
// value on every SetItems the way dialog callers rebuild their rows.
type idItem struct {
	plainItem
	id string
}

func (i *idItem) ID() string { return i.id }

func idItems(titles ...string) []FilterableItem {
	items := make([]FilterableItem, len(titles))
	for i, title := range titles {
		items[i] = &idItem{text: title, id: title}
	}
	return items
}

func selectedID(t *testing.T, l *FilterableList) string {
	t.Helper()
	item, ok := l.SelectedItem().(*idItem)
	require.True(t, ok, "an item is selected")
	return item.id
}

// TestFilterableListSetItemsKeepsFilter pins that replacing the items
// re-applies the active query: the list never shows (or selects from)
// items the filter input excludes.
func TestFilterableListSetItemsKeepsFilter(t *testing.T) {
	t.Parallel()

	l := NewFilterableList(idItems("newest", "middle", "oldest")...)
	l.SetFilter("old")
	l.SelectFirst()
	require.Equal(t, "oldest", selectedID(t, l))

	l.SetItems(idItems("newest", "middle", "oldest")...)
	require.Equal(t, 1, l.Len(), "the rebuilt list stays filtered")
	require.Len(t, l.FilteredItems(), 1)
	require.Equal(t, "oldest", selectedID(t, l))
}

// TestFilterableListSetItemsKeepsSelectedItem pins that the selection
// follows the selected item's identity across a rebuild that reorders
// the items, rather than staying on the same index.
func TestFilterableListSetItemsKeepsSelectedItem(t *testing.T) {
	t.Parallel()

	l := NewFilterableList(idItems("a", "b", "c")...)
	l.SetSelected(1)
	require.Equal(t, "b", selectedID(t, l))

	l.SetItems(idItems("c", "a", "b")...)
	require.Equal(t, "b", selectedID(t, l), "selection follows the item, not the index")

	l.AppendItems(idItems("d")...)
	require.Equal(t, "b", selectedID(t, l))

	l.PrependItems(idItems("z")...)
	require.Equal(t, "b", selectedID(t, l))
}

// TestFilterableListSetItemsDroppedSelectionKeepsPosition pins the
// fallback: when the selected item is gone the selection stays at its
// position, clamped to the new length, so a deleted row hands the
// selection to its successor.
func TestFilterableListSetItemsDroppedSelectionKeepsPosition(t *testing.T) {
	t.Parallel()

	l := NewFilterableList(idItems("a", "b", "c")...)
	l.SetSelected(1)

	l.SetItems(idItems("a", "c")...)
	require.Equal(t, "c", selectedID(t, l))

	l.SetItems(idItems("a")...)
	require.Equal(t, "a", selectedID(t, l))
}

func itemIDs(items []Item) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.(*idItem).id
	}
	return ids
}

func TestFilterOrderTiesKeepFuzzyOrder(t *testing.T) {
	t.Parallel()

	l := NewFilterableList(idItems("xaxxbxxc", "abc")...)
	l.SetFilterOrder(func(string, FilterableItem, FilterableItem) int { return 0 })
	l.SetFilter("abc")
	require.Equal(t, []string{"abc", "xaxxbxxc"}, itemIDs(l.FilteredItems()))
}

func TestFilterOrderSeesQuery(t *testing.T) {
	t.Parallel()

	var seen []string
	l := NewFilterableList(idItems("ab", "abc")...)
	l.SetFilterOrder(func(query string, _, _ FilterableItem) int {
		seen = append(seen, query)
		return 0
	})
	l.SetFilter("ab")
	require.NotEmpty(t, seen)
	for _, q := range seen {
		require.Equal(t, "ab", q)
	}
}

func TestRankMatchesFilteredItems(t *testing.T) {
	t.Parallel()

	items := idItems("newest", "middle", "oldest", "model", "nodes")
	order := func(_ string, a, b FilterableItem) int { return len(b.Filter()) - len(a.Filter()) }
	l := NewFilterableList(items...)
	l.SetFilterOrder(order)
	for _, q := range []string{"", "e", "de", "old", "zz"} {
		l.SetFilter(q)
		ranked := Rank(q, items, order)
		got := make([]Item, len(ranked))
		for i, item := range ranked {
			got[i] = item
		}
		require.Equal(t, itemIDs(l.FilteredItems()), itemIDs(got), q)
	}
}
