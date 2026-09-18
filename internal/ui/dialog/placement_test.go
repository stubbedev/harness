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

	require.Equal(t, image.Rect(30, 29, 70, 39), got, "bottom-anchored: one line off the bottom edge so the status hints stay visible, horizontally centered")
}

func TestAnchorRectTopWhenInstalled(t *testing.T) {
	// Not parallel: installs process-wide placement state.
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement(config.DialogPlacementTop)

	area := uv.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(100, 40)}
	got := AnchorRect(area, 40, 10)

	// noice.nvim style: floating, about 30% down from the top.
	require.Equal(t, image.Rect(30, 12, 70, 22), got)
}

func TestAnchorRectTopClampsTallViews(t *testing.T) {
	// Not parallel: installs process-wide placement state.
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })
	InstallPlacement(config.DialogPlacementTop)

	area := uv.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(100, 40)}
	got := AnchorRect(area, 40, 35)

	require.Equal(t, image.Rect(30, 5, 70, 40), got, "a view taller than 70% of the area stays inside it")
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

	require.Equal(t, image.Rect(30, 29, 70, 39), got)
}

func TestAnchorRectBottomClampsInTinyAreas(t *testing.T) {
	t.Parallel()

	area := uv.Rectangle{Min: image.Pt(0, 0), Max: image.Pt(100, 3)}
	got := AnchorRect(area, 40, 3)

	require.Equal(t, image.Rect(30, 0, 70, 3), got, "a view filling the area starts at its top, never above it")
}
