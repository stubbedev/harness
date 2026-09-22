package model

import (
	"image"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
)

// TestLandingMarkHoldsPositionAcrossEditorGrowth pins the mark cache:
// growing the prompt to several lines must not re-center the mark -
// only a terminal resize recomputes its placement.
func TestLandingMarkHoldsPositionAcrossEditorGrowth(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.state = uiLanding
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}
	u.updateLayoutAndSize()

	markRow := func(view string) int {
		for i, line := range strings.Split(view, "\n") {
			if strings.Contains(line, "██╗") {
				return i
			}
		}
		t.Fatal("mark row not found")
		return -1
	}

	before := markRow(ansi.Strip(u.landingView()))

	for range 5 {
		u.textarea.InsertString("a line of input\n")
	}
	u.updateLayoutAndSize()
	after := markRow(ansi.Strip(u.landingView()))
	require.Equal(t, before, after, "typing must not re-center the mark")

	// A resize recomputes it.
	u.width += 10
	u.updateLayoutAndSize()
	require.NotNil(t, u.landingMarkView)
	resized := markRow(ansi.Strip(u.landingView()))
	_ = resized
	require.Equal(t, image.Pt(u.width, u.height), u.landingMarkSize, "a resize recomputes the cached placement")
}

// TestLandingMarkCentered pins the landing page: the block-letter mark is
// horizontally centered in the main area, replacing the old LSP/MCP/skills
// columns.
func TestLandingMarkCentered(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.state = uiLanding
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}
	u.updateLayoutAndSize()

	view := ansi.Strip(u.landingView())
	width := u.layout.main.Dx()
	for line := range strings.SplitSeq(view, "\n") {
		if !strings.Contains(line, "██╗") {
			continue
		}
		lead := ansi.StringWidth(line) - ansi.StringWidth(strings.TrimLeft(line, " "))
		markWidth := ansi.StringWidth(strings.TrimRight(line, " ")) - lead
		require.InDelta(t, (width-markWidth)/2, lead, 1,
			"the mark must be centered: lead=%d width=%d mark=%d", lead, width, markWidth)
	}
}
