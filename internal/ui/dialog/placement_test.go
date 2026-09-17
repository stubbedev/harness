package dialog

import (
	"image"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

func TestAnchorRectBottomByDefault(t *testing.T) {
	t.Parallel()

	area := uv.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(100, 40)}
	got := AnchorRect(area, 40, 10)

	require.Equal(t, image.Rect(30, 30, 70, 40), got, "bottom-anchored: flush with the bottom edge, horizontally centered")
}

func TestAnchorRectTopWhenInstalled(t *testing.T) {
	// Not parallel: installs process-wide placement state.
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement(config.DialogPlacementTop)

	area := uv.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(100, 40)}
	got := AnchorRect(area, 40, 10)

	require.Equal(t, image.Rect(30, 0, 70, 10), got, "top-floating: flush with the top edge, horizontally centered")
}

func TestAnchorRectClampsOversizedViews(t *testing.T) {
	t.Parallel()

	area := uv.Rectangle{Min: image.Pt(5, 5), Max: image.Pt(45, 25)}
	got := AnchorRect(area, 80, 50)

	require.Equal(t, image.Rect(5, 5, 45, 25), got, "an oversized view fills the area regardless of placement")
}

func TestAnchorRectUnknownPlacementFallsBackToBottom(t *testing.T) {
	// Not parallel: installs process-wide placement state.
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement("diagonal")

	area := uv.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(100, 40)}
	got := AnchorRect(area, 40, 10)

	require.Equal(t, image.Rect(30, 30, 70, 40), got)
}
