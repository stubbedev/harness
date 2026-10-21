package dialog

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// ThemesID is the identifier for the theme picker dialog.
const ThemesID ID = "themes"

const (
	themesDialogMinHeight = 8
	themesDialogMaxHeight = 16
)

// Themes represents a dialog for selecting the TUI color theme. Moving
// through the list applies each theme to the whole UI immediately, so the
// user sees the colors before committing; closing without confirming
// restores the theme that was active when the dialog opened.
type Themes struct {
	*simplePicker[string]
}

var _ Dialog = (*Themes)(nil)

// NewThemes creates a new theme picker dialog.
func NewThemes(com *common.Common) *Themes {
	t := &Themes{}
	t.simplePicker = newSimplePicker(com, ThemesID, "Switch Theme", themesDialogMinHeight, themesDialogMaxHeight,
		func(name string) Action { return ActionSelectTheme{Name: name} }, t.previewAction)
	t.input.Prompt = "❯ "
	t.keyMap.UpDown = keys.WithDesc(t.keyMap.UpDown, "preview")

	t.setItems()
	return t
}

// previewAction returns the action that previews the currently selected
// theme, carrying cmd along so the caller doesn't have to choose between
// the preview and a pending input command.
func (t *Themes) previewAction(cmd tea.Cmd) Action {
	name, ok := t.selected()
	if !ok {
		return ActionCmd{cmd}
	}
	// The styles every row renders through are about to be replaced in
	// place, and the shared rows cache their renders, so rebuild them:
	// fresh rows re-render under the incoming theme.
	t.refreshItems()
	return ActionPreviewTheme{Name: name, Cmd: cmd}
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
	keep, _ := t.selected()
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
