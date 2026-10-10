package tools

import (
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/question"
)

// The tools that only ask questions run in parallel; the ones that hold the
// user or an ordered stream of effects stay sequential.
func TestToolParallelism(t *testing.T) {
	t.Parallel()

	require.True(t, NewMemoryTool(nil).Info().Parallel, "memory")
	require.False(t, NewQuestionTool(question.NewService()).Info().Parallel,
		"question: the service holds one pending question at a time")

	readOnly := &Tool{mcpName: "s", tool: &mcp.Tool{
		Name:        "lookup",
		Annotations: &gomcp.ToolAnnotations{ReadOnlyHint: true},
	}}
	require.True(t, readOnly.Info().Parallel, "read-only MCP tool")

	mutating := &Tool{mcpName: "s", tool: &mcp.Tool{
		Name:        "click",
		Annotations: &gomcp.ToolAnnotations{ReadOnlyHint: false},
	}}
	require.False(t, mutating.Info().Parallel, "MCP tool with side effects")

	unannotated := &Tool{mcpName: "s", tool: &mcp.Tool{Name: "anything"}}
	require.False(t, unannotated.Info().Parallel, "MCP tool without annotations")
}
