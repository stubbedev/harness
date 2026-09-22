package dialog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// TestCommandItemIsPickerItem pins the shared picker row contract: a
// command exposes its action as the value, its title as the label and
// its shortcut hint as the right column.
func TestCommandItemIsPickerItem(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	var item PickerItem = NewCommandItem(&sty, "new_session", "New Session", "ctrl+n", ActionNewSession{})

	assert.Equal(t, "New Session", item.Label())
	assert.Equal(t, "ctrl+n", item.RightLabel())
	_, ok := item.Value().(ActionNewSession)
	require.True(t, ok, "a command's value is its action, got %T", item.Value())
}
