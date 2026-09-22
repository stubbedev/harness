package styles

import (
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/stretchr/testify/require"
)

// TestToolCallsRecedeBehindMessages locks the transcript's contrast
// hierarchy to its single point of enforcement: chat message text
// carries the base foreground, while tool-call text sits in the
// understated grey so the calls cannot outshine the messages between
// them. Failure states (error, partial) are exempt, and so are the
// selected variants: the entry the cursor is on, or a call expanded
// to its full view, says its status in color (green pending, blue
// done) while every unselected row stays grey.
func TestToolCallsRecedeBehindMessages(t *testing.T) {
	t.Parallel()

	s := quickStyle(charmtoneOpts())

	muted := lipgloss.NewStyle().Foreground(charmtone.Squid)
	require.Equal(t, muted.String(), s.Tool.NamePending.String())
	require.Equal(t, muted.String(), s.Tool.NameNormal.String())
	require.Equal(t, muted.String(), s.Tool.NameNested.String())
	require.Equal(t, muted.String(), s.Tool.Body.String())
	require.Equal(t, muted.String(), s.Tool.ResultItemName.String())

	require.Equal(t, lipgloss.NewStyle().Foreground(charmtone.Julep).String(),
		s.Tool.NamePendingSelected.String())
	require.Equal(t, lipgloss.NewStyle().Foreground(charmtone.Malibu).String(),
		s.Tool.NameNormalSelected.String())
	require.Equal(t, lipgloss.NewStyle().Foreground(charmtone.Malibu).String(),
		s.Tool.NameNestedSelected.String())

	require.NotEqual(t, lipgloss.NewStyle().Foreground(charmtone.Squid).String(),
		s.Tool.NameError.String())
	require.NotEqual(t, lipgloss.NewStyle().Foreground(charmtone.Squid).String(),
		s.Tool.NamePartial.String())

	require.Equal(t, hex(charmtone.Sash), s.Markdown.Document.Color)
}
