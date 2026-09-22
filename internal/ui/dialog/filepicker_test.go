package dialog

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func newFilePickerTestCommon() *common.Common {
	st := styles.CharmtonePantera()
	return &common.Common{
		Styles:    &st,
		Workspace: &serversWorkspace{cfg: &config.Config{}},
	}
}

// TestFilePickerDrawsWithoutPreviewPlaceholder pins the placeholder
// removal: with no image on screen the picker renders just the framed
// title and the file rows - no solid-block preview panel that reads as
// a stray background.
func TestFilePickerDrawsWithoutPreviewPlaceholder(t *testing.T) {
	t.Parallel()

	f, _ := NewFilePicker(newFilePickerTestCommon())
	f.SetImageCapabilities(nil)

	scr := uv.NewScreenBuffer(120, 30)
	f.Draw(scr, scr.Bounds())
	view := ansi.Strip(scr.String())

	require.NotContains(t, view, "█", "no preview placeholder renders without an image on screen")
	require.Contains(t, view, "Add Image")
}

// TestFilePickerRowsAlignWithDialogItems pins the normalized rows: the
// selected row and the plain rows share the dialog item tokens, so all
// rows indent identically (one-cell padding) and the selected row uses
// the primary selection background like every other picker.
func TestFilePickerRowsAlignWithDialogItems(t *testing.T) {
	t.Parallel()

	st := styles.CharmtonePantera()
	require.Equal(t, st.Dialog.SelectedItem.GetPaddingLeft(), st.FilePicker.Selected.GetPaddingLeft(),
		"the selected file row pads like the shared dialog items")
	require.Equal(t, st.Dialog.NormalItem.GetPaddingLeft(), st.FilePicker.File.GetPaddingLeft(),
		"plain file rows pad like the shared dialog items")
	require.Equal(t, st.Dialog.SelectedItem.GetBackground(), st.FilePicker.Selected.GetBackground(),
		"the selected file row uses the shared selection background")
}
