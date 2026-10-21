package dialog

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func newProviderFormForTest(t *testing.T) *ProviderForm {
	t.Helper()
	s := styles.CharmtonePantera()
	com := &common.Common{Workspace: &stubWorkspace{cfg: &config.Config{}}, Styles: &s}
	provider := catalog.Provider{ID: "anthropic", Name: "Anthropic", Type: catalog.TypeAnthropic}
	return NewProviderForm(com, NewProviderTarget(nil, []catalog.Provider{provider}, provider.ID, config.ProviderConfig{}))
}

func typeAPIKey(d *ProviderForm, text string) {
	for _, r := range text {
		d.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func requireCursorAfter(t *testing.T, cur *tea.Cursor, rows []string, typed string) {
	t.Helper()
	require.NotNil(t, cur)
	row := ansi.Strip(rows[cur.Y])
	end := strings.Index(row, typed)
	require.NotEqual(t, -1, end, "cursor row %d does not hold the typed text %q:\n%s", cur.Y, typed, strings.Join(rows, "\n"))
	require.Equal(t, ansi.StringWidth(row[:end])+ansi.StringWidth(typed), cur.X,
		"cursor must sit on the cell right after the typed text:\n%s", strings.Join(rows, "\n"))
}

func assertProviderFormCursorPlacement(t *testing.T) {
	t.Helper()

	t.Run("end of key", func(t *testing.T) {
		d := newProviderFormForTest(t)
		drawRows(t, d, 100, 30)
		typeAPIKey(d, "sk-test")
		cur, rows := drawRows(t, d, 100, 30)
		requireCursorAfter(t, cur, rows, "sk-test")
	})

	t.Run("after moving left", func(t *testing.T) {
		d := newProviderFormForTest(t)
		drawRows(t, d, 100, 30)
		typeAPIKey(d, "sk-test")
		for range 4 {
			d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyLeft})
		}
		cur, rows := drawRows(t, d, 100, 30)
		requireCursorAfter(t, cur, rows, "sk-")
	})

	t.Run("long key scrolled horizontally", func(t *testing.T) {
		d := newProviderFormForTest(t)
		drawRows(t, d, 100, 30)
		key := "sk-" + strings.Repeat("abcdefghij", 20) + "-END"
		typeAPIKey(d, key)
		cur, rows := drawRows(t, d, 100, 30)
		requireCursorAfter(t, cur, rows, "-END")
	})
}

func TestProviderFormCursorPlacementBottomAnchored(t *testing.T) {
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement(config.DialogPlacementBottom)
	assertProviderFormCursorPlacement(t)
}

func TestProviderFormCursorPlacementTopFloating(t *testing.T) {
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement(config.DialogPlacementTop)
	assertProviderFormCursorPlacement(t)
}

func assertArgumentsCursorPlacement(t *testing.T) {
	t.Helper()
	d := newTestArguments()
	drawRows(t, d, 100, 30)
	typeIntoArguments(d, "focusword")
	cur, rows := drawRows(t, d, 100, 30)
	requireCursorAfter(t, cur, rows, "focusword")
}

func TestArgumentsCursorPlacementBottomAnchored(t *testing.T) {
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement(config.DialogPlacementBottom)
	assertArgumentsCursorPlacement(t)
}

func TestArgumentsCursorPlacementTopFloating(t *testing.T) {
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement(config.DialogPlacementTop)
	assertArgumentsCursorPlacement(t)
}
