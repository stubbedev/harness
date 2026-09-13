package tools

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/memory"
)

func newMemoryToolForTest(t *testing.T) fantasy.AgentTool {
	t.Helper()
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	return NewMemoryTool(memory.NewService(db.New(conn)))
}

func runMemoryTool(t *testing.T, tool fantasy.AgentTool, input string) fantasy.ToolResponse {
	t.Helper()
	resp, err := tool.Run(t.Context(), fantasy.ToolCall{Input: input})
	require.NoError(t, err)
	return resp
}

func TestMemoryToolRoundTrip(t *testing.T) {
	tool := newMemoryToolForTest(t)

	saved := runMemoryTool(t, tool, `{"action":"save","title":"Build commands","content":"just build","category":"project"}`)
	require.Contains(t, saved.Content, "Saved memory - [build-commands] (project) Build commands")

	// Saving the same title again updates instead of duplicating.
	updated := runMemoryTool(t, tool, `{"action":"save","title":"Build commands","content":"just build && just test"}`)
	require.Contains(t, updated.Content, "Updated memory")
	require.Contains(t, updated.Content, "just build && just test")

	listed := runMemoryTool(t, tool, `{"action":"list"}`)
	require.Contains(t, listed.Content, "1 memories")
	require.Contains(t, listed.Content, "[build-commands]")

	read := runMemoryTool(t, tool, `{"action":"read","id":"build-commands"}`)
	require.Contains(t, read.Content, "just build && just test")

	searched := runMemoryTool(t, tool, `{"action":"search","query":"build"}`)
	require.Contains(t, searched.Content, "[build-commands]")

	deleted := runMemoryTool(t, tool, `{"action":"delete","id":"build-commands"}`)
	require.Contains(t, deleted.Content, "Deleted memory")

	empty := runMemoryTool(t, tool, `{"action":"list"}`)
	require.Contains(t, empty.Content, "No memories found")
}

func TestMemoryToolSaveRedactsSecrets(t *testing.T) {
	tool := newMemoryToolForTest(t)

	resp := runMemoryTool(t, tool, `{"action":"save","title":"Creds","content":"token ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`)
	require.Contains(t, resp.Content, "redacted")
	require.NotContains(t, resp.Content, "ghp_")

	read := runMemoryTool(t, tool, `{"action":"read","id":"creds"}`)
	require.NotContains(t, read.Content, "ghp_")
	require.Contains(t, read.Content, memory.Redacted)
}

func TestMemoryToolValidation(t *testing.T) {
	tool := newMemoryToolForTest(t)

	_, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"flurb"}`})
	require.ErrorContains(t, err, "invalid action")

	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"save","content":"no title"}`})
	require.ErrorContains(t, err, "title is required")

	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"read"}`})
	require.ErrorContains(t, err, "id is required")

	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"search"}`})
	require.ErrorContains(t, err, "query is required")

	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"save","title":"t","content":"c","category":"bogus"}`})
	require.ErrorContains(t, err, "invalid category")

	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"read","id":"nope"}`})
	require.ErrorContains(t, err, "not found")
}
