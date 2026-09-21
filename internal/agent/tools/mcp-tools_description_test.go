package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A loaded MCP tool's description is bounded: it rides in every request
// for the rest of the session.
func TestBoundedMCPDescription(t *testing.T) {
	t.Parallel()
	short := "Reads a ticket."
	require.Equal(t, short, boundedMCPDescription(short))

	long := strings.Repeat("Use this tool for tickets. ", 200)
	bounded := boundedMCPDescription(long)
	require.LessOrEqual(t, len(bounded), mcpToolDescriptionLimit+len("\n[description truncated]"))
	require.True(t, strings.HasSuffix(bounded, "[description truncated]"))
	require.False(t, strings.HasSuffix(strings.TrimSuffix(bounded, "\n[description truncated]"), " "), "cut lands on a word boundary")
}
