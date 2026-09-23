package dialog

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// CommandItem wraps a uicmd.Command to implement the ListItem interface.
type CommandItem struct {
	*list.Versioned
	id    string
	title string
	// filterTitle is the searchable form of the title when it differs
	// from it, e.g. a skill's name with its source prefix stripped. It
	// must be a suffix of the title: match indexes are shifted into the
	// title for highlighting.
	filterTitle string
	shortcut    string
	description string
	action      Action
	aliases     []string
	t           *styles.Styles
	m           fuzzy.Match
	cache       map[int]string
	focused     bool
	hideInfo    bool
}

var (
	_ ListItem   = &CommandItem{Versioned: list.NewVersioned()}
	_ PickerItem = &CommandItem{Versioned: list.NewVersioned()}
)

// NewCommandItem creates a new CommandItem.
func NewCommandItem(t *styles.Styles, id, title, shortcut string, action Action) *CommandItem {
	return &CommandItem{
		Versioned: list.NewVersioned(),
		id:        id,
		t:         t,
		title:     title,
		shortcut:  shortcut,
		action:    action,
	}
}

// Finished implements list.Item. Command items are render-stable
// outside of explicit SetFocused / SetMatch.
func (c *CommandItem) Finished() bool {
	return true
}

// WithAliases returns the CommandItem with the given aliases for filtering.
func (c *CommandItem) WithAliases(aliases ...string) *CommandItem {
	c.aliases = aliases
	return c
}

// WithDescription returns the CommandItem with a description displayed below
// the title.
func (c *CommandItem) WithDescription(desc string) *CommandItem {
	c.description = desc
	return c
}

// WithFilterTitle sets the text fuzzy matching sees as the item's
// name. It must be a suffix of the title: matches are highlighted on
// the title by shifting the indexes past the hidden prefix.
func (c *CommandItem) WithFilterTitle(filterTitle string) *CommandItem {
	c.filterTitle = filterTitle
	return c
}

// Filter implements ListItem.
func (c *CommandItem) Filter() string {
	primary, rest := c.FilterFields()
	switch {
	case rest == "":
		return primary
	case primary == "":
		return rest
	default:
		return primary + " " + rest
	}
}

// FilterFields implements list.TieredFilterItem: the title and aliases
// are the name tier; the description only matches, it never outranks a
// title match.
func (c *CommandItem) FilterFields() (primary, rest string) {
	title := c.title
	if c.filterTitle != "" {
		title = c.filterTitle
	}
	if len(c.aliases) == 0 {
		return title, c.description
	}
	return title + " " + strings.Join(c.aliases, " "), c.description
}

// ID implements ListItem.
func (c *CommandItem) ID() string {
	return c.id
}

// Title returns the item's display title, so dialogs that map a
// selection back to a name resolve it through the selected item
// rather than a positional index (which filtering reorders).
func (c *CommandItem) Title() string {
	return c.title
}

// Value implements PickerItem; the payload the palette resolves a
// selection to is the command's action.
func (c *CommandItem) Value() any { return c.action }

// Label implements PickerItem; the title is the row's main text.
func (c *CommandItem) Label() string { return c.title }

// RightLabel implements PickerItem; the shortcut hint is the row's
// right-aligned info column.
func (c *CommandItem) RightLabel() string { return c.shortcut }

// SetFocused implements ListItem.
func (c *CommandItem) SetFocused(focused bool) {
	if c.focused == focused {
		return
	}
	c.cache = nil
	c.focused = focused
	if c.Versioned != nil {
		c.Bump()
	}
}

// SetMatch implements ListItem.
func (c *CommandItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(c.m, m) {
		return
	}
	c.cache = nil
	c.m = m
	if c.Versioned != nil {
		c.Bump()
	}
}

// Action returns the action associated with the command item.
func (c *CommandItem) Action() Action {
	return c.action
}

// Shortcut returns the shortcut associated with the command item.
func (c *CommandItem) Shortcut() string {
	return c.shortcut
}

// InfoText implements infoColumnItem; the command shortcut is its info.
func (c *CommandItem) InfoText() string {
	return c.shortcut
}

// SetHideInfo controls whether the shortcut hint column is shown. The
// dialog hides it uniformly when it would crowd the command names.
func (c *CommandItem) SetHideInfo(v bool) {
	if c.hideInfo == v {
		return
	}
	c.cache = nil
	c.hideInfo = v
	if c.Versioned != nil {
		c.Bump()
	}
}

// Render implements ListItem.
func (c *CommandItem) Render(width int) string {
	styles := pickerItemStyles(c.t)
	shortcut := c.shortcut
	if c.hideInfo {
		shortcut = ""
	}
	match := c.matchForTitle()
	rendered := renderItem(styles, c.title, shortcut, c.focused, width, c.cache, &match)
	if c.description != "" {
		descStyle := c.t.Dialog.SecondaryText
		if c.focused {
			descStyle = c.t.Dialog.SelectedItem
		}
		contentWidth := max(0, width-descStyle.GetHorizontalFrameSize()+1)
		description := ansi.Truncate(strings.TrimSpace(c.description), contentWidth, "…")
		descVisWidth := lipgloss.Width(description)
		gap := strings.Repeat(" ", max(0, contentWidth-descVisWidth))
		if description == "" {
			description = " "
		}
		rendered = lipgloss.JoinVertical(lipgloss.Left, rendered, descStyle.Render(description+gap))
	}
	return rendered
}

// matchForTitle returns the item's match with its indexes shifted into
// the title, for items whose filter text is a suffix of it. The fuzzy
// indexes are offsets into the filter text, but the highlighter maps
// them onto the rendered title.
func (c *CommandItem) matchForTitle() fuzzy.Match {
	if c.filterTitle == "" || len(c.m.MatchedIndexes) == 0 {
		return c.m
	}
	offset := len(c.title) - len(c.filterTitle)
	indexes := make([]int, len(c.m.MatchedIndexes))
	for i, idx := range c.m.MatchedIndexes {
		indexes[i] = idx + offset
	}
	m := c.m
	m.MatchedIndexes = indexes
	return m
}
