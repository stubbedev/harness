package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/workspace"
)

// themesWorkspace stubs the single workspace method the theme picker
// reaches for: the config it reads the current theme from.
type themesWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *themesWorkspace) Config() *config.Config { return w.cfg }

func newTestThemes(t *testing.T, currentTheme string) *Themes {
	t.Helper()
	st := styles.CharmtonePantera()
	cfg := &config.Config{Options: &config.Options{TUI: &config.TUIOptions{Theme: currentTheme}}}
	com := &common.Common{Styles: &st, Workspace: &themesWorkspace{cfg: cfg}}
	return NewThemes(com)
}

// typeText feeds a string to the dialog one key press at a time, the way
// a user filtering the list would.
func typeText(t *testing.T, d *Themes, s string) Action {
	t.Helper()
	var action Action
	for _, r := range s {
		action = d.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return action
}

// TestThemesListsBuiltins verifies every built-in theme is offered.
func TestThemesListsBuiltins(t *testing.T) {
	t.Parallel()

	d := newTestThemes(t, "")
	items := d.list.FilteredItems()
	require.Len(t, items, len(styles.BuiltinThemeNames()))

	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.(*ThemeItem).name)
	}
	require.Equal(t, styles.BuiltinThemeNames(), names)
}

// TestThemesStartsOnConfiguredTheme verifies the picker opens with the
// configured theme selected, so arrowing away is an explicit choice.
func TestThemesStartsOnConfiguredTheme(t *testing.T) {
	t.Parallel()

	d := newTestThemes(t, "gruvbox-dark")
	item := d.selectedItem()
	require.NotNil(t, item)
	require.Equal(t, "gruvbox-dark", item.name)
	require.True(t, item.isCurrent)
}

// TestThemesPreviewsOnMove verifies moving through the list asks the UI to
// apply the newly highlighted theme, which is what makes the colors change
// live, and that the moved-to item is re-rendered rather than served from
// the stale cache.
func TestThemesPreviewsOnMove(t *testing.T) {
	t.Parallel()

	d := newTestThemes(t, "charmtone")
	before := d.selectedItem()
	require.NotNil(t, before)
	versionBefore := before.Version()

	action := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	preview, ok := action.(ActionPreviewTheme)
	require.True(t, ok, "moving down should preview a theme, got %T", action)

	after := d.selectedItem()
	require.NotNil(t, after)
	require.Equal(t, after.name, preview.Name)
	require.NotEqual(t, before.name, after.name)
	require.Greater(t, before.Version(), versionBefore, "items must be invalidated so they re-render under the new theme")
}

// TestThemesWrapsAndPreviews verifies the wrap-around at the list edges
// still previews, rather than leaving the UI on the previous theme.
func TestThemesWrapsAndPreviews(t *testing.T) {
	t.Parallel()

	d := newTestThemes(t, "")
	d.list.SelectFirst()

	action := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	preview, ok := action.(ActionPreviewTheme)
	require.True(t, ok, "wrapping up should preview a theme, got %T", action)

	names := styles.BuiltinThemeNames()
	require.Equal(t, names[len(names)-1], preview.Name)
}

// TestThemesSelectConfirms verifies enter confirms the highlighted theme.
func TestThemesSelectConfirms(t *testing.T) {
	t.Parallel()

	d := newTestThemes(t, "charmtone")
	d.list.SelectLast()

	action := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	selected, ok := action.(ActionSelectTheme)
	require.True(t, ok, "enter should select a theme, got %T", action)

	names := styles.BuiltinThemeNames()
	require.Equal(t, names[len(names)-1], selected.Name)
}

// TestThemesCloseDoesNotSelect verifies esc leaves the dialog without
// confirming, so the caller knows to restore the previous theme.
func TestThemesCloseDoesNotSelect(t *testing.T) {
	t.Parallel()

	d := newTestThemes(t, "charmtone")
	action := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.IsType(t, ActionClose{}, action)
}

// TestThemesFilterPreviews verifies typing a filter previews whatever the
// narrowed list lands on, and that a filter matching nothing previews
// nothing instead of panicking on an empty list.
func TestThemesFilterPreviews(t *testing.T) {
	t.Parallel()

	d := newTestThemes(t, "charmtone")

	action := typeText(t, d, "gruvbox")
	preview, ok := action.(ActionPreviewTheme)
	require.True(t, ok, "filtering should preview the first match, got %T", action)
	require.Equal(t, "gruvbox-dark", preview.Name)

	d = newTestThemes(t, "charmtone")
	action = typeText(t, d, "zzzz")
	require.IsType(t, ActionCmd{}, action, "a filter matching nothing must not preview")
}

// TestThemeDisplayName verifies identifiers become readable titles.
func TestThemeDisplayName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Charmtone", themeDisplayName("charmtone"))
	require.Equal(t, "Catppuccin Mocha", themeDisplayName("catppuccin-mocha"))
	require.Equal(t, "Gruvbox Dark", themeDisplayName("gruvbox_dark"))
}

// TestThemeSwatchUsesOwnPalette verifies each entry's swatch is painted
// from that theme's colors, so two themes never look alike in the list.
func TestThemeSwatchUsesOwnPalette(t *testing.T) {
	t.Parallel()

	charmtone := themeSwatch("charmtone")
	gruvbox := themeSwatch("gruvbox-dark")
	require.NotEmpty(t, charmtone)
	require.NotEmpty(t, gruvbox)
	require.NotEqual(t, charmtone, gruvbox)
	require.Empty(t, themeSwatch("no-such-theme"))
}
