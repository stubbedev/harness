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

// FilterableList is a list that takes filterable items that can be filtered
// via a settable query.
type FilterableList struct {
	*List
	items []FilterableItem
	query string
	// order re-ranks the filtered items after matching (stable),
	// so what renders and what selection walks are the same order. A
	// nil order keeps the tiered fuzzy score order.
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
// matched items on every filter; nil keeps the tiered fuzzy score order.
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

	type matched struct {
		item    FilterableItem
		score   int
		primary int
	}
	var matches []matched
	for i, item := range f.items {
		fields, tiered := item.(TieredFilterItem)
		filter := item.Filter()
		rankFields := fuzzyrank.Fields{Primary: filter}
		if tiered {
			rankFields.Primary, rankFields.Rest = fields.FilterFields()
		}
		res, ok := fuzzyrank.Match(f.query, rankFields)
		if !ok {
			continue
		}
		if ms, ok := item.(MatchSettable); ok {
			// Only the primary field's offsets reach the highlighter:
			// rows highlight their label, and offsets into the rest of
			// the filter text point past it.
			ms.SetMatch(fuzzy.Match{Str: filter, Index: i, MatchedIndexes: res.Primary, Score: res.Score})
		}
		matches = append(matches, matched{item: item, score: res.Score, primary: len(rankFields.Primary)})
	}

	// A set filter order re-ranks the matched items after matching;
	// Render and every consumer below see the same order. Without one,
	// the tiered score ranks, with equal scores breaking toward the
	// shorter name tier, then the original order.
	if f.order != nil {
		filterable := make([]FilterableItem, len(matches))
		for i, m := range matches {
			filterable[i] = m.item
		}
		slices.SortStableFunc(filterable, f.order)
		items := make([]Item, len(filterable))
		for i, item := range filterable {
			items[i] = item
		}
		return items
	}
	slices.SortStableFunc(matches, func(a, b matched) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return a.primary - b.primary
	})
	items := make([]Item, len(matches))
	for i, m := range matches {
		items[i] = m.item
	}
	return items
}

// Render renders the filterable list.
func (f *FilterableList) Render() string {
	f.List.SetItems(f.FilteredItems()...)
	return f.List.Render()
}
