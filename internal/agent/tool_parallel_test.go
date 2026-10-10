package agent

import (
	"fmt"
	"sync"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/csync"
)

// The search tools only read, or serialise their one write, so they run in
// parallel; send_message appends to an inbox in order and stays sequential.
func TestSearchToolsAreParallel(t *testing.T) {
	t.Parallel()

	coord := &coordinator{}
	require.True(t, (&skillSearchTool{coord: coord}).Info().Parallel, "skill_search")
	require.True(t, (&toolSearchTool{coord: coord}).Info().Parallel, "tool_search")
	require.True(t, (&mcpSearchTool{server: "parallel-flag-server", coord: coord}).Info().Parallel, "mcp search")
	require.False(t, (&sendMessageTool{coord: coord}).Info().Parallel, "send_message")
}

// Concurrent loads from one server must all land: each is a
// read-modify-write of the server's expanded set, and without expandMu one
// load overwrites another's.
func TestConcurrentMCPLoadsAllLand(t *testing.T) {
	t.Parallel()

	const server = "concurrent-load-server"
	const n = 32
	registry := make([]*mcp.Tool, n)
	for i := range registry {
		registry[i] = &mcp.Tool{Name: fmt.Sprintf("tool_%02d", i), Description: "a tool"}
	}
	t.Cleanup(mcp.RegisterToolsForTest(server, registry))
	tool := &mcpSearchTool{
		server: server,
		coord:  &coordinator{expandedMCPTools: csync.NewMap[string, map[string]bool]()},
	}

	var wg sync.WaitGroup
	errs := make(chan string, n)
	for _, entry := range registry {
		wg.Go(func() {
			resp, err := tool.Run(t.Context(), fantasy.ToolCall{Input: fmt.Sprintf(`{"load":[%q]}`, entry.Name)})
			switch {
			case err != nil:
				errs <- err.Error()
			case resp.IsError:
				errs <- resp.Content
			}
		})
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}

	for _, entry := range registry {
		require.True(t, tool.coord.mcpToolExpanded(server, entry.Name), "load of %s was lost", entry.Name)
	}
}
