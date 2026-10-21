package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

func typePalette(c *Commands, s string) {
	for _, r := range s {
		c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func paletteIDs(t *testing.T, c *Commands) []string {
	t.Helper()
	var ids []string
	for _, it := range c.list.FilteredItems() {
		item, ok := it.(*CommandItem)
		require.True(t, ok)
		ids = append(ids, item.ID())
	}
	return ids
}

func commandIDs(items []*CommandItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID())
	}
	return ids
}

func TestPaletteSystemTabSearchFindsSkill(t *testing.T) {
	t.Parallel()

	c := newTestPalette(t, paletteTestCommands())
	require.Equal(t, SystemCommands, c.selected)

	typePalette(c, "rust-exp")
	require.Equal(t, []string{"custom_rust-expert"}, paletteIDs(t, c))
	item, ok := c.list.SelectedItem().(*CommandItem)
	require.True(t, ok)
	require.Equal(t, "Skills", item.InfoText(), "a result is tagged with its tab")
}

func TestPaletteEmptyQueryListsSelectedTab(t *testing.T) {
	t.Parallel()

	c := newTestPalette(t, paletteTestCommands())
	typePalette(c, "rust")
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyBackspace})
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyBackspace})
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyBackspace})
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyBackspace})

	ids := paletteIDs(t, c)
	require.NotContains(t, ids, "custom_rust-expert")
	require.NotContains(t, ids, "custom_review")
	for _, it := range c.list.FilteredItems() {
		item, ok := it.(*CommandItem)
		require.True(t, ok)
		require.NotContains(t, item.InfoText(), "System", "an unfiltered tab is not tagged")
	}
}

func TestPaletteTabSwitchKeepsQuery(t *testing.T) {
	t.Parallel()

	c := newTestPalette(t, paletteTestCommands())
	typePalette(c, "review")
	before := paletteIDs(t, c)
	require.Contains(t, before, "custom_review")

	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, SkillsCommands, c.selected)
	require.Equal(t, "review", c.input.Value())
	require.Equal(t, before, paletteIDs(t, c))
}

func TestPaletteTabSwitchWithoutQueryShowsTab(t *testing.T) {
	t.Parallel()

	c := newTestPalette(t, paletteTestCommands())
	c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, []string{"custom_rust-expert"}, paletteIDs(t, c))
}

func TestPaletteAndPickerRankIdentically(t *testing.T) {
	t.Parallel()

	cmds := append(paletteTestCommands(), kontainerPaletteCommands()...)
	for _, query := range []string{"pr", "re", "se", "sessions", "rust", "quit", "kontainer-p", "theme"} {
		c := newTestPalette(t, cmds)
		typePalette(c, query)
		want := commandIDs(RankCommands(c.catalog.Invocables(), query))
		require.NotEmpty(t, want, query)
		require.Equal(t, want, paletteIDs(t, c), query)
	}
}

func TestRankCommandsTiers(t *testing.T) {
	t.Parallel()

	c := newTestPalette(t, paletteTestCommands())
	items := c.catalog.Invocables()

	require.Equal(t, "quit", RankCommands(items, "exi")[0].ID(), "an alias prefix ranks first")
	require.Equal(t, "init", RankCommands(items, "project")[0].ID(), "the title is searched")
	require.Equal(t, "custom_rust-expert", RankCommands(items, "expert guidance")[0].ID(), "the description is searched")
	require.Equal(t, commandIDs(items), commandIDs(RankCommands(items, "")), "an empty query lists everything")
}

func TestRankCommandsPrefixBeforeFuzzy(t *testing.T) {
	t.Parallel()

	com := paletteTestCommon()
	items := []*CommandItem{
		NewCommandItem(com.Styles, "custom_x", "fooxbar", nil).WithSlashNames("fooxbar"),
		NewCommandItem(com.Styles, "custom_y", "barfoo", nil).WithSlashNames("barfoo", "fb"),
		NewCommandItem(com.Styles, "custom_z", "fb-long", nil).WithSlashNames("fb-long"),
	}
	require.Equal(t, []string{"custom_z", "custom_y", "custom_x"}, commandIDs(RankCommands(items, "fb")))
}
