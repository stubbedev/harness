package dialog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/keys"
)

// TestDialogKeysFollowUserOverrides pins that dialogs read their bindings
// from the installed keymap rather than declaring their own: rebinding
// dialog.select in options.tui.keybinds has to move the key in a dialog
// built afterwards.
func TestDialogKeysFollowUserOverrides(t *testing.T) {
	// Not parallel: this installs the process keymap.
	t.Cleanup(func() { keys.Install(nil) })

	keys.Install(map[string][]string{
		"dialog.select":         {"ctrl+space"},
		"dialog.close":          {"ctrl+q"},
		"dialog.models.connect": {"ctrl+w"},
	})

	themes := newTestThemes(t, "charmtone-pantera")
	require.Equal(t, []string{"ctrl+space"}, themes.keyMap.Select.Keys())
	assert.Equal(t, []string{"ctrl+q"}, themes.keyMap.Close.Keys())

	// Every dialog builds its bindings from this one accessor, so a
	// per-dialog key rebinds the same way a shared one does.
	assert.Equal(t, []string{"ctrl+w"}, dialogKeys().Models.Connect.Keys())

	// The shared binding keeps its own help description per dialog.
	assert.Equal(t, "confirm", themes.keyMap.Select.Help().Desc)
}
