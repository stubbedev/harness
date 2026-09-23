package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplaceSymbolOpsSpans(t *testing.T) {
	t.Parallel()
	lines := []string{"a", "b", "c", "d"}
	// The symbol spans lines 1..2 ("b", "c").
	tests := map[string]string{
		"replace":    "a|X|d",
		"add_before": "a|X|b|c|d",
		"add_after":  "a|b|c|X|d",
		"delete":     "a|d",
	}
	for action, want := range tests {
		op := replaceSymbolOps[action]
		from, to := op.span(1, 2)
		var inserted []string
		if op.insert {
			inserted = []string{"X"}
		}
		got := strings.Join(slices.Concat(lines[:from], inserted, lines[to:]), "|")
		require.Equal(t, want, got, action)
	}
}
