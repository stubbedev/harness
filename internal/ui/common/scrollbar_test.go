package common

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func TestScrollbarRowsUseThemeGlyphs(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	rows := strings.Split(Scrollbar(&sty, 10, 100, 20, 40), "\n")
	require.Len(t, rows, 10)

	var marks strings.Builder
	for _, row := range rows {
		switch row {
		case sty.Dialog.ScrollbarThumb:
			marks.WriteByte('#')
		case sty.Dialog.ScrollbarTrack:
			marks.WriteByte('.')
		default:
			require.Failf(t, "unexpected scrollbar row", "%q", row)
		}
	}
	require.Equal(t, "....##....", marks.String())
	require.Equal(t, styles.ScrollbarThumb, ansi.Strip(sty.Dialog.ScrollbarThumb))
	require.Equal(t, styles.ScrollbarTrack, ansi.Strip(sty.Dialog.ScrollbarTrack))
}

func TestScrollbarEmptyWhenContentFits(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	require.Empty(t, Scrollbar(&sty, 10, 20, 20, 0))
	require.Empty(t, Scrollbar(&sty, 0, 100, 20, 0))
}

func TestScrollbarAllocatesOnlyTheResult(t *testing.T) {
	sty := styles.CharmtonePantera()
	allocs := testing.AllocsPerRun(100, func() {
		_ = Scrollbar(&sty, 40, 1000, 40, 300)
	})
	require.LessOrEqual(t, allocs, 1.0)
}

func BenchmarkScrollbar(b *testing.B) {
	sty := styles.CharmtonePantera()
	b.ReportAllocs()
	for b.Loop() {
		_ = Scrollbar(&sty, 40, 1000, 40, 300)
	}
}
