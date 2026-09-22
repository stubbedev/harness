package model

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
)

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
