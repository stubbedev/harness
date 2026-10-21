package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
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

// TestSpliceSymbolUnreadableFileIsErrorResponse: a file that cannot be
// read once its symbol is found is an error result for the model rather
// than a Go error that aborts the turn.
func TestSpliceSymbolUnreadableFileIsErrorResponse(t *testing.T) {
	t.Parallel()
	locked := unreadableFixture(t, t.TempDir(), "locked.go")

	params := ReplaceSymbolParams{Symbol: "f", FilePath: locked, Action: "delete"}
	resp, err := spliceSymbol(t.Context(), nil, nil, nil, nil, "", params, "delete", replaceSymbolOps["delete"], protocol.Range{})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Cannot read")
	require.Contains(t, resp.Content, "permissions")
}
