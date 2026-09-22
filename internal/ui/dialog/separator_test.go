package dialog

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// TestRenderContextDrawsSeparatorRule pins the dialog separator: a rule
// line spans the panel between the input row and the content, in both
// placements. RenderContext is the single source - every input dialog
// goes through it, so none can lack the rule.
func TestRenderContextDrawsSeparatorRule(t *testing.T) {
	// Not parallel: installs process-wide placement state.
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })

	for _, placement := range []string{config.DialogPlacementTop, config.DialogPlacementBottom} {
		InstallPlacement(placement)
		st := styles.CharmtonePantera()
		rc := NewRenderContext(&st, 40)
		rc.Title = "Switch Model"
		rc.AddInput("❯ query")
		rc.AddPart("  an item")
		lines := strings.Split(ansi.Strip(rc.Render()), "\n")

		inputRow := -1
		for i, line := range lines {
			if strings.Contains(line, "query") {
				inputRow = i
				break
			}
		}
		require.GreaterOrEqual(t, inputRow, 0, "placement %v: input row renders", placement)

		// The rule sits between the input row and the content, whichever
		// side of the panel the placement puts them on.
		neighbor := inputRow + 1
		if placement == config.DialogPlacementBottom {
			require.Greater(t, inputRow, 0, "bottom placement: content renders above the input")
			neighbor = inputRow - 1
		}
		rule := strings.Trim(lines[neighbor], " ")
		// The rule row may carry the floating frame's side borders; apart
		// from those it must be nothing but the rule.
		require.Empty(t, strings.Trim(rule, "─│"),
			"placement %v: the row between input and content must be a rule", placement)

		if placement == config.DialogPlacementBottom {
			// The input hugs the rule above it and the panel closes with a
			// rule beneath it: no margin row between, cursor math derives
			// from the same predicate.
			require.Contains(t, lines[inputRow+1], "─",
				"bottom placement: the panel closes with a rule under the input")
			cur := DialogCursor(&st, rc.Render(), &tea.Cursor{X: 0, Y: 0})
			require.Equal(t, inputRow, cur.Y, "bottom placement: the cursor lands on the input row above the closing rule")
		}
	}
}

// TestRenderContextOmitsRuleWithoutInput pins that an input-less dialog
// renders no rule: there is nothing to separate the title from.
func TestRenderContextOmitsRuleWithoutInput(t *testing.T) {
	// Not parallel: installs process-wide placement state.
	t.Cleanup(func() { InstallPlacement(config.DialogPlacementBottom) })

	st := styles.CharmtonePantera()
	rc := NewRenderContext(&st, 40)
	rc.Title = "Rewind"
	rc.AddPart("  an item")
	lines := strings.Split(ansi.Strip(rc.Render()), "\n")
	require.Len(t, lines, 3, "only the border, title and content render:\n%s", strings.Join(lines, "\n"))
	require.Contains(t, lines[2], "an item",
		"no rule renders between the title and content without an input row; the top border is the panel frame's own")
}
