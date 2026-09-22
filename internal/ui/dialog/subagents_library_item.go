package dialog

import (
	"github.com/stubbedev/harness/internal/ui/styles"
)

// LibrarySubagentItemData holds the data for a library subagent list item.
type LibrarySubagentItemData struct {
	Name        string
	Description string
	Color       string
	FilePath    string
	Scope       string
	Disabled    bool
	// Deletable reports whether the workspace will honor a delete for this
	// definition (user-owned global dir only).
	Deletable bool
	// Error carries the discovery diagnostic for a definition file that
	// failed to parse or validate; such items are informational only.
	Error string
}

// NewLibrarySubagentItem creates the library tab's picker row through
// the shared picker item: the subagent's name is the label and its
// scope badge the right-hand info. The description (or, for broken
// definitions, the discovery diagnostic) is not part of the shared row
// but stays in the filter text, so a library entry can still be found
// by what it does. The data is the value a selection resolves to.
func NewLibrarySubagentItem(t *styles.Styles, data LibrarySubagentItemData) PickerItem {
	scope := data.Scope
	if scope == "" {
		scope = "user"
	}
	return NewPickerItem(t, data, data.Name, scope, data.Name+" "+data.Description)
}
