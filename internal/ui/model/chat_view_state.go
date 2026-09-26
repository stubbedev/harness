package model

import (
	"github.com/stubbedev/harness/internal/ui/chat"
)

// ChatViewState is how the user left a transcript: what they expanded,
// where the selection sat and how far they had scrolled. Items are
// rebuilt from messages whenever the transcript switches between the
// main session and an agent's, so the state is keyed by item ID rather
// than held by the items, and survives the rebuild.
type ChatViewState struct {
	// levels holds every expandable item's expansion level, tool group
	// calls included, by item ID.
	levels map[string]uint8
	// selectedID is the selected item, selectedChild the tool group
	// sub-cursor on it (-1 on the group row).
	selectedID    string
	selectedChild int
	// manualSelection records that the user had moved the selection off
	// the newest item, so it must not snap back to the newest on return.
	manualSelection bool
	// offsetID and offsetLine anchor the scroll position to the first
	// visible item, so content added above or below it while the view
	// was away does not shift what the user was reading.
	offsetID   string
	offsetLine int
	// follow is the auto-scroll flag: a view left at the bottom returns
	// to the bottom, whatever arrived meanwhile.
	follow bool
}

// eachExpandable calls fn for every expandable item in the transcript,
// the calls inside tool groups included. Capture and restore both walk
// through it, so they cannot disagree about which items carry state.
func (m *Chat) eachExpandable(fn func(id string, e chat.Expandable)) {
	visit := func(item chat.MessageItem) {
		if e, ok := item.(chat.Expandable); ok {
			fn(item.ID(), e)
		}
	}
	for i := range m.list.Len() {
		item, ok := m.list.ItemAt(i).(chat.MessageItem)
		if !ok {
			continue
		}
		visit(item)
		if group, ok := item.(chat.ToolGroupContainer); ok {
			for _, child := range group.ToolChildren() {
				visit(child)
			}
		}
	}
}

// saveViewState stores the held transcript's view state under its
// session. A list that holds no session's transcript has nothing to
// come back to.
func (m *Chat) saveViewState() {
	if m.transcriptID != "" {
		m.viewStates[m.transcriptID] = m.captureViewState()
	}
}

// captureViewState records the transcript's current view state.
func (m *Chat) captureViewState() ChatViewState {
	vs := ChatViewState{
		levels:          make(map[string]uint8),
		selectedChild:   -1,
		manualSelection: m.manualSelection,
		follow:          m.follow,
	}
	m.eachExpandable(func(id string, e chat.Expandable) {
		vs.levels[id] = e.ExpansionLevel()
	})
	if item, ok := m.list.SelectedItem().(chat.MessageItem); ok {
		vs.selectedID = item.ID()
	}
	if g, ok := m.selectedGroup(); ok {
		vs.selectedChild = g.SelectedChild()
	}
	offsetIdx, offsetLine := m.list.ScrollPosition()
	if item, ok := m.list.ItemAt(offsetIdx).(chat.MessageItem); ok {
		vs.offsetID, vs.offsetLine = item.ID(), offsetLine
	}
	return vs
}

// restoreViewState reapplies a captured view state to the current
// items. Every item the state names takes its captured level; an item
// that arrived since keeps the shape it was built with. A selection or
// scroll anchor whose item is gone falls back to the newest item and
// the bottom, the view a fresh transcript opens on.
func (m *Chat) restoreViewState(vs ChatViewState) {
	m.eachExpandable(func(id string, e chat.Expandable) {
		if level, ok := vs.levels[id]; ok {
			e.SetExpansionLevel(level)
		}
	})

	if idx, ok := m.itemIndex(vs.selectedID); ok && vs.manualSelection {
		m.SetSelected(idx)
		if g, ok := m.selectedGroup(); ok {
			g.SetSelectedChild(vs.selectedChild)
		}
	} else {
		m.SelectLast()
	}

	idx, ok := m.itemIndex(vs.offsetID)
	if vs.follow || !ok {
		m.ScrollToBottom()
		return
	}
	m.list.ScrollToIndex(idx)
	m.list.ScrollBy(vs.offsetLine)
	m.follow = m.AtBottom()
}

// itemIndex resolves a top-level item's index by its own ID. Unlike the
// ID map it does not resolve a tool call to its group: the state names
// the items the list held, and a call since folded into another group
// is not the item the user left.
func (m *Chat) itemIndex(id string) (int, bool) {
	idx, ok := m.idInxMap[id]
	if !ok || id == "" {
		return 0, false
	}
	item, ok := m.list.ItemAt(idx).(chat.MessageItem)
	if !ok || item.ID() != id {
		return 0, false
	}
	return idx, true
}
