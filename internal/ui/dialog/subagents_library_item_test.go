package dialog

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/ui/styles"
)

// TestNewLibrarySubagentItemPickerRow pins the shared row surface: the
// library tab's row is a PickerItem whose value is the item data, whose
// label is the name, and whose right label is the scope badge.
func TestNewLibrarySubagentItemPickerRow(t *testing.T) {
	t.Parallel()

	st := styles.CharmtonePantera()
	data := LibrarySubagentItemData{
		Name:        "reviewer",
		Description: "reviews diffs",
		Scope:       "project",
	}

	row := NewLibrarySubagentItem(&st, data)
	require.Equal(t, "reviewer", row.Label())
	require.Equal(t, data, row.Value().(LibrarySubagentItemData))
	require.Equal(t, "project", row.RightLabel())
	require.Equal(t, "reviewer reviews diffs", row.Filter(), "matching still includes the description")
}

// TestNewLibrarySubagentItemDefaultsScope pins the scope default: an
// entry without a scope badge reads as a user-scoped definition.
func TestNewLibrarySubagentItemDefaultsScope(t *testing.T) {
	t.Parallel()

	st := styles.CharmtonePantera()
	row := NewLibrarySubagentItem(&st, LibrarySubagentItemData{Name: "reviewer"})
	require.Equal(t, "user", row.RightLabel())
}
