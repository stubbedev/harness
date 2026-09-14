package dialog

import (
	"sort"
	"strings"

	"github.com/sahilm/fuzzy"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// ModelsList is a list specifically for model items and groups.
type ModelsList struct {
	*list.List
	groups []ModelGroup
	query  string
	t      *styles.Styles

	// The flat filter index, rebuilt only when the groups change so a
	// keystroke runs a single fuzzy pass over every model instead of one
	// pass per provider group.
	filterItems     []*ModelItem
	filterNames     []string
	filterGroup     []int
	filterPrefixLen []int

	// Incremental match cache. Fuzzy matching is monotone in the query:
	// a string that matches a longer query also matches any prefix of
	// it. So when the query only grows (the common typing path), the
	// next pass can search just the previous pass's matches instead of
	// the whole catalog. Reset by rebuildFilterIndex and by any query
	// that is not an extension of the cached one.
	filterQuery   string
	filterMatched []int
}

// rebuildFilterIndex flattens the groups into parallel slices for fuzzy
// matching: one search string per model (its group title plus the model's
// filter text), which group the model belongs to, and how long that
// group's name prefix is so match indexes can be shifted back.
func (f *ModelsList) rebuildFilterIndex() {
	f.filterItems = make([]*ModelItem, 0, f.Len())
	f.filterNames = f.filterNames[:0]
	f.filterGroup = f.filterGroup[:0]
	f.filterPrefixLen = f.filterPrefixLen[:0]
	for gi, g := range f.groups {
		name := strings.ToLower(g.Title) + " "
		for _, item := range g.Items {
			f.filterItems = append(f.filterItems, item)
			f.filterNames = append(f.filterNames, name+item.Filter())
			f.filterGroup = append(f.filterGroup, gi)
			f.filterPrefixLen = append(f.filterPrefixLen, len(name))
		}
	}
	f.filterQuery = ""
	f.filterMatched = nil
}

// NewModelsList creates a new list suitable for model items and groups.
func NewModelsList(sty *styles.Styles, groups ...ModelGroup) *ModelsList {
	f := &ModelsList{
		List:   list.NewList(),
		groups: groups,
		t:      sty,
	}
	f.RegisterRenderCallback(list.FocusedRenderCallback(f.List))
	return f
}

// Len returns the number of model items across all groups.
func (f *ModelsList) Len() int {
	n := 0
	for _, g := range f.groups {
		n += len(g.Items)
	}
	return n
}

// SetGroups sets the model groups and updates the list items.
func (f *ModelsList) SetGroups(groups ...ModelGroup) {
	f.groups = groups
	f.rebuildFilterIndex()
	items := []list.Item{}
	for _, g := range f.groups {
		items = append(items, &g)
		for _, item := range g.Items {
			items = append(items, item)
		}
		// Add a space separator after each provider section
		items = append(items, list.NewSpacerItem(1))
	}
	f.SetItems(items...)
}

// SetFilter sets the filter query and updates the list items.
func (f *ModelsList) SetFilter(q string) {
	f.query = q
	f.SetItems(f.VisibleItems()...)
}

// SetSelected sets the selected item index. It overrides the base method to
// skip non-model items.
func (f *ModelsList) SetSelected(index int) {
	if index < 0 || index >= f.Len() {
		f.List.SetSelected(index)
		return
	}

	f.List.SetSelected(index)
	for {
		selectedItem := f.SelectedItem()
		if _, ok := selectedItem.(*ModelItem); ok {
			return
		}
		f.List.SetSelected(index + 1)
		index++
		if index >= f.Len() {
			return
		}
	}
}

// SetSelectedItem sets the selected item in the list by item ID.
func (f *ModelsList) SetSelectedItem(itemID string) {
	if itemID == "" {
		return
	}

	// Walk the selectable model items using the same helpers that
	// keyboard navigation uses, so we stay in sync with the flat
	// list layout.
	for ok := f.SelectFirst(); ok; ok = f.SelectNext() {
		if mi, is := f.SelectedItem().(*ModelItem); is && mi.ID() == itemID {
			return
		}
	}
}

// SelectNext selects the next model item, skipping any non-focusable items
// like group headers and spacers.
func (f *ModelsList) SelectNext() (v bool) {
	v = f.List.SelectNext()
	for v {
		selectedItem := f.SelectedItem()
		if _, ok := selectedItem.(*ModelItem); ok {
			return v
		}
		v = f.List.SelectNext()
	}
	return v
}

// SelectPrev selects the previous model item, skipping any non-focusable items
// like group headers and spacers.
func (f *ModelsList) SelectPrev() (v bool) {
	v = f.List.SelectPrev()
	for v {
		selectedItem := f.SelectedItem()
		if _, ok := selectedItem.(*ModelItem); ok {
			return v
		}
		v = f.List.SelectPrev()
	}
	return v
}

// SelectFirst selects the first model item in the list.
func (f *ModelsList) SelectFirst() (v bool) {
	v = f.List.SelectFirst()
	for v {
		selectedItem := f.SelectedItem()
		_, ok := selectedItem.(*ModelItem)
		if ok {
			return v
		}
		v = f.List.SelectNext()
	}
	return v
}

// SelectLast selects the last model item in the list.
func (f *ModelsList) SelectLast() (v bool) {
	v = f.List.SelectLast()
	for v {
		selectedItem := f.SelectedItem()
		if _, ok := selectedItem.(*ModelItem); ok {
			return v
		}
		v = f.List.SelectPrev()
	}
	return v
}

// IsSelectedFirst checks if the selected item is the first model item.
func (f *ModelsList) IsSelectedFirst() bool {
	originalIndex := f.Selected()
	f.SelectFirst()
	isFirst := f.Selected() == originalIndex
	f.List.SetSelected(originalIndex)
	return isFirst
}

// IsSelectedLast checks if the selected item is the last model item.
func (f *ModelsList) IsSelectedLast() bool {
	originalIndex := f.Selected()
	f.SelectLast()
	isLast := f.Selected() == originalIndex
	f.List.SetSelected(originalIndex)
	return isLast
}

// VisibleItems returns the visible items after filtering. The query runs
// once over the whole flat index; matches are then bucketed back into
// their groups to rebuild the grouped view.
func (f *ModelsList) VisibleItems() []list.Item {
	query := strings.ToLower(strings.ReplaceAll(f.query, " ", ""))

	if query == "" {
		// No filter, return all items with group headers
		items := []list.Item{}
		for _, g := range f.groups {
			items = append(items, &g)
			for _, item := range g.Items {
				item.SetMatch(fuzzy.Match{})
				items = append(items, item)
			}
			// Add a space separator after each provider section
			items = append(items, list.NewSpacerItem(1))
		}
		return items
	}

	if f.filterItems == nil {
		f.rebuildFilterIndex()
	}

	// Narrow the candidate set to the previous pass's matches when the
	// query only grew; otherwise start from the full index.
	candidates := f.filterMatched
	if f.filterQuery == "" || !strings.HasPrefix(query, f.filterQuery) || candidates == nil {
		candidates = make([]int, len(f.filterItems))
		for i := range candidates {
			candidates[i] = i
		}
	}
	names := make([]string, len(candidates))
	for i, idx := range candidates {
		names[i] = f.filterNames[idx]
	}

	matches := fuzzy.Find(query, names)

	// The candidate order preserves flat-index order, so sorting by the
	// candidate index keeps groups contiguous and order within each
	// group stable.
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Index < matches[j].Index
	})

	// Cache this pass's matches for the next keystroke.
	filtered := make([]int, 0, len(matches))
	for _, match := range matches {
		filtered = append(filtered, candidates[match.Index])
	}
	f.filterQuery = query
	f.filterMatched = filtered

	items := []list.Item{}
	lastGroup := -1
	for _, match := range matches {
		idx := candidates[match.Index]
		gi := f.filterGroup[idx]
		if gi != lastGroup {
			if lastGroup != -1 {
				// A space separator after each provider section
				items = append(items, list.NewSpacerItem(1))
			}
			g := f.groups[gi]
			items = append(items, &g)
			lastGroup = gi
		}

		idxs := make([]int, 0, len(match.MatchedIndexes))
		prefix := f.filterPrefixLen[idx]
		for _, i := range match.MatchedIndexes {
			// Drop the provider-name highlight offsets
			if i < prefix {
				continue
			}
			idxs = append(idxs, i-prefix)
		}
		match.MatchedIndexes = idxs

		item := f.filterItems[idx]
		item.SetMatch(match)
		items = append(items, item)
	}
	if lastGroup != -1 {
		items = append(items, list.NewSpacerItem(1))
	}

	return items
}

// Render renders the filterable list.
func (f *ModelsList) Render() string {
	return f.List.Render()
}

type modelGroups []ModelGroup

func (m modelGroups) Len() int {
	n := 0
	for _, g := range m {
		n += len(g.Items)
	}
	return n
}

func (m modelGroups) String(i int) string {
	count := 0
	for _, g := range m {
		if i < count+len(g.Items) {
			return g.Items[i-count].Filter()
		}
		count += len(g.Items)
	}
	return ""
}
