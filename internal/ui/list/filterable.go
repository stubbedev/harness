package list

import (
	"slices"

	"github.com/rivo/uniseg"
	"github.com/sahilm/fuzzy"
	"github.com/stubbedev/harness/internal/fuzzyrank"
)

// MatchedRanges converts a list of match indexes into contiguous ranges.
// Shared by every fuzzy-match highlighter (completions popup, dialog
// lists).
func MatchedRanges(in []int) [][2]int {
	if len(in) == 0 {
		return [][2]int{}
	}
	current := [2]int{in[0], in[0]}
	if len(in) == 1 {
		return [][2]int{current}
	}
	var out [][2]int
	for i := 1; i < len(in); i++ {
		if in[i] == current[1]+1 {
			current[1] = in[i]
		} else {
			out = append(out, current)
			current = [2]int{in[i], in[i]}
		}
	}
	out = append(out, current)
	return out
}

// BytePosToVisibleCharPos converts byte positions in str to visible
// character positions: grapheme clusters count as one character at their
// cell width, so styled ranges line up with what the terminal shows.
func BytePosToVisibleCharPos(str string, rng [2]int) (int, int) {
	bytePos, byteStart, byteStop := 0, rng[0], rng[1]
	pos := 0
	gr := uniseg.NewGraphemes(str)
	for byteStart > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	start := pos
	for byteStop > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	return start, pos
}

// FilterableItem is an item that can be filtered via a query.
type FilterableItem interface {
	Item
	// Filter returns the value to be used for filtering.
	Filter() string
}

// MatchSettable is an interface for items that can have their match indexes
// and match score set.
type MatchSettable interface {
	SetMatch(fuzzy.Match)
}

// TieredFilterItem is an optional FilterableItem extension that splits the
// item's filter text into the name tier (which ranks above the rest and
// carries the match highlighting) and the rest (descriptions and other
// secondary text, which still matches but ranks below). The concatenation
// "primary + " " + rest" must reproduce Filter(). Items without it filter
// on Filter() alone.
type TieredFilterItem interface {
	FilterableItem
	FilterFields() (primary, rest string)
}

type FilterOrder func(query string, a, b FilterableItem) int

// FilterableList is a list that takes filterable items that can be filtered
// via a settable query.
type FilterableList struct {
	*List
	items []FilterableItem
	query string
	// order re-ranks the filtered items after matching (stable),
	// so what renders and what selection walks are the same order. A
	// nil order keeps the tiered fuzzy score order.
	order FilterOrder
}

// NewFilterableList creates a new filterable list.
func NewFilterableList(items ...FilterableItem) *FilterableList {
	f := &FilterableList{
		List:  NewList(),
		items: items,
	}
	f.RegisterRenderCallback(FocusedRenderCallback(f.List))
	f.SetItems(items...)
	return f
}

// SetItems sets the list items and re-applies the current filter, so the
// visible items always match the query. The selection follows the same
// item (see refilter), not the same position.
func (f *FilterableList) SetItems(items ...FilterableItem) {
	f.items = items
	f.refilter()
}

// AppendItems appends items to the list and re-applies the current
// filter, keeping the selection on the same item.
func (f *FilterableList) AppendItems(items ...FilterableItem) {
	f.items = append(f.items, items...)
	f.refilter()
}

// PrependItems prepends items to the list and re-applies the current
// filter, keeping the selection on the same item.
func (f *FilterableList) PrependItems(items ...FilterableItem) {
	f.items = append(items, f.items...)
	f.refilter()
}

// SetFilterOrder sets an optional stable re-ranking applied to the
// matched items on every filter; nil keeps the tiered fuzzy score order.
// Match highlighting is unaffected.
func (f *FilterableList) SetFilterOrder(cmp FilterOrder) {
	f.order = cmp
	f.refilter()
}

// refilter shows the items matching the current query and keeps the
// selection on the item it was on: the same item value, or an item
// with the same ID when both are [Identifiable] (callers rebuild their
// items on every change). When that item is gone the selection keeps
// its position, clamped to the new length.
func (f *FilterableList) refilter() {
	prev := f.SelectedItem()
	filtered := f.FilteredItems()
	f.List.SetItems(filtered...)
	if prev == nil {
		return
	}
	if idx := slices.IndexFunc(filtered, func(item Item) bool { return sameItem(item, prev) }); idx >= 0 {
		f.SetSelected(idx)
	}
}

// sameItem reports whether a and b are the same logical item: the same
// value, or Identifiable items with equal IDs.
func sameItem(a, b Item) bool {
	if a == b {
		return true
	}
	ai, aok := a.(Identifiable)
	bi, bok := b.(Identifiable)
	return aok && bok && ai.ID() == bi.ID()
}

// SetFilter sets the filter query and updates the list items.
func (f *FilterableList) SetFilter(q string) {
	f.query = q
	f.List.SetItems(f.FilteredItems()...)
	f.ScrollToTop()
}

// FilteredItems returns the visible items after filtering.
func (f *FilterableList) FilteredItems() []Item {
	if f.query == "" {
		items := make([]Item, len(f.items))
		for i, item := range f.items {
			if ms, ok := item.(MatchSettable); ok {
				ms.SetMatch(fuzzy.Match{})
				item = ms.(FilterableItem)
			}
			items[i] = item
		}
		return items
	}

	ranked := rank(f.query, f.items, f.order)
	items := make([]Item, len(ranked))
	for i, r := range ranked {
		if ms, ok := r.item.(MatchSettable); ok {
			// Only the primary field's offsets reach the highlighter:
			// rows highlight their label, and offsets into the rest of
			// the filter text point past it.
			ms.SetMatch(fuzzy.Match{Str: r.filter, Index: r.index, MatchedIndexes: r.result.Primary, Score: r.result.Score})
		}
		items[i] = r.item
	}
	return items
}

func Rank[T FilterableItem](query string, items []T, order FilterOrder) []T {
	if query == "" {
		return items
	}
	ranked := rank(query, items, order)
	out := make([]T, len(ranked))
	for i, r := range ranked {
		out[i] = r.item
	}
	return out
}

type rankedItem[T FilterableItem] struct {
	item    T
	index   int
	filter  string
	result  fuzzyrank.Result
	primary int
}

func rank[T FilterableItem](query string, items []T, order FilterOrder) []rankedItem[T] {
	var matches []rankedItem[T]
	for i, item := range items {
		filter := item.Filter()
		fields := fuzzyrank.Fields{Primary: filter}
		if tiered, ok := FilterableItem(item).(TieredFilterItem); ok {
			fields.Primary, fields.Rest = tiered.FilterFields()
		}
		res, ok := fuzzyrank.Match(query, fields)
		if !ok {
			continue
		}
		matches = append(matches, rankedItem[T]{item: item, index: i, filter: filter, result: res, primary: len(fields.Primary)})
	}
	slices.SortStableFunc(matches, func(a, b rankedItem[T]) int {
		if a.result.Score != b.result.Score {
			return b.result.Score - a.result.Score
		}
		return a.primary - b.primary
	})
	if order != nil {
		slices.SortStableFunc(matches, func(a, b rankedItem[T]) int {
			return order(query, a.item, b.item)
		})
	}
	return matches
}

// Render renders the filterable list.
func (f *FilterableList) Render() string {
	f.refilter()
	return f.List.Render()
}
