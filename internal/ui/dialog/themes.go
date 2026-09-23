package dialog

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

const (
	// ThemesID is the identifier for the theme picker dialog.
	ThemesID              ID = "themes"
	themesDialogMinHeight    = 8
	themesDialogMaxHeight    = 16
)

// Themes represents a dialog for selecting the TUI color theme. Moving
// through the list applies each theme to the whole UI immediately, so the
// user sees the colors before committing; closing without confirming
// restores the theme that was active when the dialog opened.
type Themes struct {
	com   *common.Common
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

var _ Dialog = (*Themes)(nil)

// NewThemes creates a new theme picker dialog.
func NewThemes(com *common.Common) *Themes {
	t := &Themes{com: com}

	t.list = list.NewFilterableList()
	t.list.Focus()

	t.input = textinput.New()
	t.input.SetVirtualCursor(false)
	t.input.Prompt = "❯ "
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
func (t *Themes) ID() ID {
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
			selectPrevWrap(t.list)
			return t.previewAction(nil)
		case key.Matches(msg, t.keyMap.Next):
			t.list.Focus()
			selectNextWrap(t.list)
			return t.previewAction(nil)
		case key.Matches(msg, t.keyMap.Select):
			name, ok := t.selectedTheme()
			if !ok {
				break
			}
			return ActionSelectTheme{Name: name}
		default:
			cmd, changed := filterInput(&t.input, msg, applyListFilter(t.list))
			if !changed {
				return ActionCmd{cmd}
			}
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
	name, ok := t.selectedTheme()
	if !ok {
		return ActionCmd{cmd}
	}
	// The styles every row renders through are about to be replaced in
	// place, and the shared rows cache their renders, so rebuild them:
	// fresh rows re-render under the incoming theme.
	t.refreshItems()
	return ActionPreviewTheme{Name: name, Cmd: cmd}
}

// selectedTheme resolves the selected row to its theme name through the
// shared picker item, or reports false when the list is empty (e.g. a
// filter that matches nothing).
func (t *Themes) selectedTheme() (string, bool) {
	item, ok := t.list.SelectedItem().(PickerItem)
	if !ok || item == nil {
		return "", false
	}
	name, ok := item.Value().(string)
	return name, ok
}

// Cursor returns the cursor position relative to the dialog.
func (t *Themes) Cursor() *tea.Cursor {
	return InputCursor(t.com.Styles, t.input.Cursor())
}

// Draw implements [Dialog].
func (t *Themes) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	st := t.com.Styles
	width := DialogWidth(st, area)
	innerWidth := DialogInnerWidth(st, width)

	t.input.SetWidth(dialogInputTextWidth(st, t.input, innerWidth))

	// Size the dialog to fit the list content, clamped to min/max bounds.
	heightOffset := dialogChromeHeight(st, st.Dialog.HelpView)
	desiredHeight := heightOffset + t.list.TotalHeight()
	maxAvailable := DialogHeightCeiling(st, area, themesDialogMaxHeight)
	height := max(themesDialogMinHeight, min(themesDialogMaxHeight, desiredHeight, maxAvailable))

	listHeight, listTotalHeight, _ := sizeDialogList(st, t.list, innerWidth, height, true)

	rc := NewRenderContext(st, width)
	rc.Title = "Switch Theme"
	rc.AddInput(t.input.View())

	if t.list.Height() >= len(t.list.FilteredItems()) {
		t.list.ScrollToTop()
	} else {
		t.list.ScrollToSelected()
	}

	listView := st.Dialog.List.Height(t.list.Height()).Render(t.list.Render())
	listView = joinScrollbar(st, listView, listHeight, listTotalHeight, listHeight, t.list.Offset())
	rc.AddPart(listView)

	view := rc.Render()

	cur := DialogCursor(st, view, t.input.Cursor())
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

// setItems fills the list with one picker row per built-in theme, opening
// with the configured theme selected.
func (t *Themes) setItems() {
	t.buildItems()
	t.selectTheme(common.ThemeNameFromConfig(t.com.Config()))
	t.list.ScrollToSelected()
}

// refreshItems rebuilds every row after a preview replaced the styles the
// rows render through, keeping the active filter and selection.
func (t *Themes) refreshItems() {
	keep, _ := t.selectedTheme()
	t.buildItems()
	t.list.SetFilter(t.input.Value())
	t.selectTheme(keep)
	t.list.ScrollToSelected()
}

// buildItems fills the list with one picker row per built-in theme. The
// row's value is the theme identifier; its filter text covers both the
// display title and the identifier, since the identifier is what ends up
// in the config file.
func (t *Themes) buildItems() {
	names := styles.BuiltinThemeNames()
	items := make([]list.FilterableItem, len(names))
	for i, name := range names {
		title := themeDisplayName(name)
		items[i] = NewPickerItem(t.com.Styles, name, title, "", title+" "+name)
	}
	t.list.SetItems(items...)
}

// selectTheme moves the selection onto the named theme's row, keeping the
// current selection when the filter hides that theme.
func (t *Themes) selectTheme(name string) {
	for i, it := range t.list.FilteredItems() {
		item, ok := it.(PickerItem)
		if !ok || item == nil {
			continue
		}
		if n, ok := item.Value().(string); ok && n == name {
			t.list.SetSelected(i)
			return
		}
	}
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
