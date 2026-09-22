package list

import (
	"slices"

	"github.com/rivo/uniseg"
	"github.com/sahilm/fuzzy"
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
	pos, start, stop := 0, 0, 0
	gr := uniseg.NewGraphemes(str)
	for byteStart > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	start = pos
	for byteStop > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	stop = pos
	return start, stop
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

// FilterableList is a list that takes filterable items that can be filtered
// via a settable query.
type FilterableList struct {
	*List
	items []FilterableItem
	query string
	// order re-ranks the filtered items after fuzzy matching (stable),
	// so what renders and what selection walks are the same order. A
	// nil order keeps fuzzy score order.
	order func(a, b FilterableItem) int
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

// SetItems sets the list items and updates the filtered items.
func (f *FilterableList) SetItems(items ...FilterableItem) {
	f.items = items
	fitems := make([]Item, len(items))
	for i, item := range items {
		fitems[i] = item
	}
	f.List.SetItems(fitems...)
}

// AppendItems appends items to the list and updates the filtered items.
func (f *FilterableList) AppendItems(items ...FilterableItem) {
	f.items = append(f.items, items...)
	itms := make([]Item, len(f.items))
	for i, item := range f.items {
		itms[i] = item
	}
	f.List.SetItems(itms...)
}

// PrependItems prepends items to the list and updates the filtered items.
func (f *FilterableList) PrependItems(items ...FilterableItem) {
	f.items = append(items, f.items...)
	itms := make([]Item, len(f.items))
	for i, item := range f.items {
		itms[i] = item
	}
	f.List.SetItems(itms...)
}

// SetFilterOrder sets an optional stable re-ranking applied to the
// fuzzy-matched items on every filter; nil restores fuzzy score order.
// Match highlighting is unaffected.
func (f *FilterableList) SetFilterOrder(cmp func(a, b FilterableItem) int) {
	f.order = cmp
	f.List.SetItems(f.FilteredItems()...)
}

// SetFilter sets the filter query and updates the list items.
func (f *FilterableList) SetFilter(q string) {
	f.query = q
	f.List.SetItems(f.FilteredItems()...)
	f.ScrollToTop()
}

// FilterableItemsSource is a type that implements [fuzzy.Source] for filtering
// [FilterableItem]s.
type FilterableItemsSource []FilterableItem

// Len returns the length of the source.
func (f FilterableItemsSource) Len() int {
	return len(f)
}

// String returns the string representation of the item at index i.
func (f FilterableItemsSource) String(i int) string {
	return f[i].Filter()
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

	items := FilterableItemsSource(f.items)
	matches := fuzzy.FindFrom(f.query, items)
	matchedItems := []Item{}
	resultSize := len(matches)
	for i := range resultSize {
		match := matches[i]
		item := items[match.Index]
		if ms, ok := item.(MatchSettable); ok {
			ms.SetMatch(match)
			item = ms.(FilterableItem)
		}
		matchedItems = append(matchedItems, item)
	}

	// A set filter order re-ranks the matched items after fuzzy
	// matching; Render and every consumer below see the same order.
	if f.order != nil {
		filterable := make([]FilterableItem, len(matchedItems))
		for i, item := range matchedItems {
			filterable[i] = item.(FilterableItem)
		}
		slices.SortStableFunc(filterable, f.order)
		for i, item := range filterable {
			matchedItems[i] = item
		}
	}

	return matchedItems
}

// Render renders the filterable list.
func (f *FilterableList) Render() string {
	f.List.SetItems(f.FilteredItems()...)
	return f.List.Render()
}
