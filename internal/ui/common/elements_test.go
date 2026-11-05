package common

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func TestFormatTokensAndCostPrefixesEstimatedUsage(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	rendered := formatTokensAndCost(&sty, 120, 1000, 0, true)
	actual := ansi.Strip(rendered)

	require.Contains(t, actual, "~12%")
	require.Contains(t, actual, "(120)")
	require.Contains(t, actual, "$0.00")
	require.True(t, strings.Contains(rendered, sty.ModelInfo.TokenPercentage.Render("~12%")))
}

func TestFormatTokensAndCostOmitsEstimatedPrefix(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	actual := ansi.Strip(formatTokensAndCost(&sty, 120, 1000, 0, false))

	require.Contains(t, actual, "12%")
	require.NotContains(t, actual, "~12%")
}

func TestFormatSubagentTokenCountIsCompact(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		prompt, completion int64
		want               string
	}{
		{0, 0, ""},
		{800, 40, "840 tok"},
		{999, 0, "999 tok"},
		{1_000, 0, "1.0k tok"},
		{12_000, 345, "12.3k tok"},
		{999_999, 0, "999.9k tok"},
		{1_000_000, 0, "1.0M tok"},
		{1_234_567, 0, "1.2M tok"},
	} {
		require.Equal(t, tc.want, FormatSubagentTokenCount(tc.prompt, tc.completion), "%d+%d", tc.prompt, tc.completion)
	}
}
