package agent

import (
	"context"
	"errors"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestResilientToolConvertsErrorToResponse(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "memory", err: errors.New("id is required for read")}
	tool := &resilientTool{inner: inner}

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-1", Name: "memory"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "id is required for read")
	require.True(t, inner.called, "the failure must still reach the inner tool")
}

func TestResilientToolKeepsCancellationFatal(t *testing.T) {
	t.Parallel()

	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		tool := &resilientTool{inner: &fakeTool{name: "shell", err: cause}}

		_, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-2", Name: "shell"})
		require.ErrorIs(t, err, cause)
	}
}

func TestResilientToolPassesSuccessThrough(t *testing.T) {
	t.Parallel()

	tool := &resilientTool{inner: &fakeTool{name: "view", resp: fantasy.NewTextResponse("ok")}}

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-3", Name: "view"})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, "ok", resp.Content)
}

func TestWrapToolsResilientForwardsIdentity(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "view"}
	wrapped := wrapToolsResilient([]fantasy.AgentTool{inner})

	require.Len(t, wrapped, 1)
	require.Equal(t, "view", wrapped[0].Info().Name)
	require.Empty(t, wrapped[0].ProviderOptions())
}
