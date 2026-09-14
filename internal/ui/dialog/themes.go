package dialog

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/sahilm/fuzzy"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

const (
	// ThemesID is the identifier for the theme picker dialog.
	ThemesID              = "themes"
	themesDialogMaxWidth  = 50
	themesDialogMinHeight = 8
	themesDialogMaxHeight = 16
	// themeSwatchBlock is the glyph painted once per swatch color.
	themeSwatchBlock = "█"
)

// Themes represents a dialog for selecting the TUI color theme. Moving
// through the list applies each theme to the whole UI immediately, so the
// user sees the colors before committing; closing without confirming
// restores the theme that was active when the dialog opened.
type Themes struct {
	com   *common.Common
	help  help.Model
	list  *list.FilterableList
	input textinput.Model

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

// ThemeItem represents a theme list item.
type ThemeItem struct {
	*list.Versioned
	name      string
	title     string
	swatch    string
	isCurrent bool
	t         *styles.Styles
	m         fuzzy.Match
	cache     map[int]string
	focused   bool
}

// Finished implements list.Item. Theme items are render-stable outside of
// explicit SetFocused / SetMatch / invalidate calls, all of which bump the
// version and therefore drop the frozen cache entry.
func (t *ThemeItem) Finished() bool {
	return true
}

var (
	_ Dialog   = (*Themes)(nil)
	_ ListItem = (*ThemeItem)(nil)
)

// NewThemes creates a new theme picker dialog.
func NewThemes(com *common.Common) *Themes {
	t := &Themes{com: com}

	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()
	t.help = h

	t.list = list.NewFilterableList()
	t.list.Focus()

	t.input = textinput.New()
	t.input.SetVirtualCursor(false)
	t.input.Placeholder = "Type to filter"
	t.input.SetStyles(com.Styles.TextInput)
	t.input.Focus()

	km := dialogKeys()
	t.keyMap.Select = km.Select
	t.keyMap.Next = km.Next
	t.keyMap.Previous = km.Previous
	// Moving through this list applies each theme, so the help says
	// "preview" where other dialogs say "choose".
	t.keyMap.UpDown = keys.WithDesc(km.UpDown, "preview")
	t.keyMap.Close = km.Close

	t.setItems()
	return t
}

// ID implements Dialog.
func (t *Themes) ID() string {
	return ThemesID
}

// HandleMsg implements [Dialog].
func (t *Themes) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, t.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, t.keyMap.Previous):
			t.list.Focus()
			if t.list.IsSelectedFirst() {
				t.list.SelectLast()
				t.list.ScrollToBottom()
			} else {
				t.list.SelectPrev()
				t.list.ScrollToSelected()
			}
			return t.previewAction(nil)
		case key.Matches(msg, t.keyMap.Next):
			t.list.Focus()
			if t.list.IsSelectedLast() {
				t.list.SelectFirst()
				t.list.ScrollToTop()
			} else {
				t.list.SelectNext()
				t.list.ScrollToSelected()
			}
			return t.previewAction(nil)
		case key.Matches(msg, t.keyMap.Select):
			item := t.selectedItem()
			if item == nil {
				break
			}
			return ActionSelectTheme{Name: item.name}
		default:
			prevValue := t.input.Value()
			var cmd tea.Cmd
			t.input, cmd = t.input.Update(msg)
			value := t.input.Value()
			if value == prevValue {
				return ActionCmd{cmd}
			}
			t.list.SetFilter(value)
			t.list.ScrollToTop()
			t.list.SetSelected(0)
			// Filtering moves the selection, so the preview follows it.
			return t.previewAction(cmd)
		}
	}
	return nil
}

// previewAction returns the action that previews the currently selected
// theme, carrying cmd along so the caller doesn't have to choose between
// the preview and a pending input command.
func (t *Themes) previewAction(cmd tea.Cmd) Action {
	item := t.selectedItem()
	if item == nil {
		return ActionCmd{cmd}
	}
	// The styles this dialog renders with are about to be replaced in
	// place, so every cached row is stale.
	t.invalidateItems()
	return ActionPreviewTheme{Name: item.name, Cmd: cmd}
}

// selectedItem returns the selected theme item, or nil when the list is
// empty (e.g. a filter that matches nothing).
func (t *Themes) selectedItem() *ThemeItem {
	selected := t.list.SelectedItem()
	if selected == nil {
		return nil
	}
	item, ok := selected.(*ThemeItem)
	if !ok {
		return nil
	}
	return item
}

// invalidateItems drops every item's render cache and bumps its version so
// the list re-renders them under the incoming theme.
func (t *Themes) invalidateItems() {
	for _, it := range t.list.FilteredItems() {
		if item, ok := it.(*ThemeItem); ok {
			item.invalidate()
		}
	}
}

// Cursor returns the cursor position relative to the dialog.
func (t *Themes) Cursor() *tea.Cursor {
	return InputCursor(t.com.Styles, t.input.Cursor())
}

// Draw implements [Dialog].
func (t *Themes) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	st := t.com.Styles
	width := max(0, min(themesDialogMaxWidth, area.Dx()-st.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - st.Dialog.View.GetHorizontalFrameSize()

	t.input.SetWidth(dialogInputTextWidth(st, t.input, innerWidth))

	// Size the dialog to fit the list content, clamped to min/max bounds.
	heightOffset := st.Dialog.Title.GetVerticalFrameSize() + titleContentHeight +
		st.Dialog.InputPrompt.GetVerticalFrameSize() + inputContentHeight +
		st.Dialog.HelpView.GetVerticalFrameSize() +
		st.Dialog.View.GetVerticalFrameSize()
	desiredHeight := heightOffset + t.list.TotalHeight()
	maxAvailable := area.Dy() - st.Dialog.View.GetVerticalBorderSize()
	height := max(themesDialogMinHeight, min(themesDialogMaxHeight, desiredHeight, maxAvailable))

	listHeight, listTotalHeight, _ := sizeDialogList(st, t.list, innerWidth, height)

	rc := NewRenderContext(st, width)
	rc.Title = "Switch Theme"
	rc.AddPart(st.Dialog.InputPrompt.Render(t.input.View()))

	if t.list.Height() >= len(t.list.FilteredItems()) {
		t.list.ScrollToTop()
	} else {
		t.list.ScrollToSelected()
	}

	listView := st.Dialog.List.Height(t.list.Height()).Render(t.list.Render())
	listView = joinScrollbar(st, listView, listHeight, listTotalHeight, listHeight, t.list.Offset())
	rc.AddPart(listView)
	rc.Help = renderDialogHelp(st, &t.help, t, innerWidth)

	view := rc.Render()

	cur := t.Cursor()
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (t *Themes) ShortHelp() []key.Binding {
	return []key.Binding{
		t.keyMap.UpDown,
		t.keyMap.Select,
		t.keyMap.Close,
	}
}

// FullHelp implements [help.KeyMap].
func (t *Themes) FullHelp() [][]key.Binding {
	return [][]key.Binding{{
		t.keyMap.Select,
		t.keyMap.Next,
		t.keyMap.Previous,
		t.keyMap.Close,
	}}
}

func (t *Themes) setItems() {
	current := common.ThemeNameFromConfig(t.com.Config())

	names := styles.BuiltinThemeNames()
	items := make([]list.FilterableItem, 0, len(names))
	selectedIndex := 0
	for i, name := range names {
		item := &ThemeItem{
			Versioned: list.NewVersioned(),
			name:      name,
			title:     themeDisplayName(name),
			swatch:    themeSwatch(name),
			isCurrent: strings.EqualFold(name, current),
			t:         t.com.Styles,
		}
		if item.isCurrent {
			selectedIndex = i
		}
		items = append(items, item)
	}

	t.list.SetItems(items...)
	t.list.SetSelected(selectedIndex)
	t.list.ScrollToSelected()
}

// themeDisplayName turns a theme identifier into a title for the list:
// "catppuccin-mocha" becomes "Catppuccin Mocha".
func themeDisplayName(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_'
	})
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// themeSwatch renders the named theme's brand colors as colored blocks,
// styled with that theme's own palette rather than the active one so each
// row shows what it is offering.
func themeSwatch(name string) string {
	colors := styles.ThemeSwatch(name)
	if len(colors) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range colors {
		b.WriteString(lipgloss.NewStyle().Foreground(c).Render(themeSwatchBlock))
	}
	return b.String()
}

// Filter returns the filter value for the theme item. Both the display
// title and the raw identifier match, since the identifier is what ends up
// in the config file.
func (t *ThemeItem) Filter() string {
	return t.title + " " + t.name
}

// ID returns the unique identifier for the theme.
func (t *ThemeItem) ID() string {
	return t.name
}

// SetFocused sets the focus state of the theme item.
func (t *ThemeItem) SetFocused(focused bool) {
	if t.focused == focused {
		return
	}
	t.cache = nil
	t.focused = focused
	if t.Versioned != nil {
		t.Bump()
	}
}

// SetMatch sets the fuzzy match for the theme item.
func (t *ThemeItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(t.m, m) {
		return
	}
	t.cache = nil
	t.m = m
	if t.Versioned != nil {
		t.Bump()
	}
}

// invalidate drops the item's render cache and bumps its version. Called
// when the active theme changes underneath the dialog: the item renders
// through a shared [styles.Styles] pointer whose contents were replaced,
// which no cache key can see.
func (t *ThemeItem) invalidate() {
	t.cache = nil
	if t.Versioned != nil {
		t.Bump()
	}
}

// Render returns the string representation of the theme item.
func (t *ThemeItem) Render(width int) string {
	info := ""
	if t.isCurrent {
		info = "current"
	}
	title := t.title
	if t.swatch != "" {
		title += "  " + t.swatch
	}
	st := ListItemStyles{
		ItemBlurred:     t.t.Dialog.NormalItem,
		ItemFocused:     t.t.Dialog.SelectedItem,
		InfoTextBlurred: t.t.Dialog.ListItem.InfoBlurred,
		InfoTextFocused: t.t.Dialog.ListItem.InfoFocused,
	}
	return renderItem(st, title, info, t.focused, width, t.cache, &t.m)
}
