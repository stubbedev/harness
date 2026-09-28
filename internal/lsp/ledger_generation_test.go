package lsp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLedgerDiffAtSkipsAnUnchangedGeneration pins the short-circuit the
// tool reports rely on: a second report at the same generation neither
// rebuilds the diagnostics nor reports anything, a new generation does,
// and Forget makes the next report start over.
func TestLedgerDiffAtSkipsAnUnchangedGeneration(t *testing.T) {
	t.Parallel()

	l := NewLedger()
	calls := 0
	current := func(lines map[string]string) func() map[string]string {
		return func() map[string]string {
			calls++
			return lines
		}
	}

	added, _ := l.DiffAt("s", "g1", current(map[string]string{"a": "A"}))
	require.Equal(t, []string{"A"}, added)
	added, resolved := l.DiffAt("s", "g1", current(map[string]string{"a": "A"}))
	require.Empty(t, added)
	require.Empty(t, resolved)
	require.Equal(t, 1, calls, "an unchanged generation is not rebuilt")

	_, resolved = l.DiffAt("s", "g2", current(map[string]string{}))
	require.Len(t, resolved, 1)

	l.Forget("s")
	added, _ = l.DiffAt("s", "g2", current(map[string]string{"a": "A"}))
	require.Equal(t, []string{"A"}, added, "after Forget the same generation is reported again")
}
