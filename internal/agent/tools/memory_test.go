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
	return NewMemoryTool(memory.NewService(db.New(conn), conn))
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

func TestMemoryToolEdit(t *testing.T) {
	tool := newMemoryToolForTest(t)

	runMemoryTool(t, tool, `{"action":"save","title":"Build commands","content":"just build","category":"project"}`)

	// Edit by exact title updates content without duplicating.
	edited := runMemoryTool(t, tool, `{"action":"edit","title":"Build commands","content":"just build && just test"}`)
	require.Contains(t, edited.Content, "Edited memory")
	require.Contains(t, edited.Content, "just build && just test")

	// Omitted category and pinned keep the stored values.
	read := runMemoryTool(t, tool, `{"action":"read","id":"build-commands"}`)
	require.Contains(t, read.Content, "(project)")
	require.NotContains(t, read.Content, "(feedback)")

	listed := runMemoryTool(t, tool, `{"action":"list"}`)
	require.Contains(t, listed.Content, "1 memories", "edit must not duplicate the memory")

	// Edit by id works too.
	byID := runMemoryTool(t, tool, `{"action":"edit","id":"build-commands","content":"just ci","category":"reference"}`)
	require.Contains(t, byID.Content, "just ci")
	require.Contains(t, byID.Content, "(reference)")

	// A title that does not exist is an error pointing at save.
	_, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"edit","title":"Typo Title","content":"x"}`})
	require.ErrorContains(t, err, "use save to create it")

	// Unknown id is an error as well.
	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"edit","id":"nope","content":"x"}`})
	require.ErrorContains(t, err, "no memory with id")

	// Content is required.
	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"edit","title":"Build commands"}`})
	require.ErrorContains(t, err, "content is required")

	// Neither id nor title is an error.
	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"edit","content":"x"}`})
	require.ErrorContains(t, err, "id or title is required")
}

func TestMemoryToolReadByQuery(t *testing.T) {
	tool := newMemoryToolForTest(t)

	runMemoryTool(t, tool, `{"action":"save","title":"Build commands","content":"just build"}`)
	runMemoryTool(t, tool, `{"action":"save","title":"Test commands","content":"just test"}`)

	// A query matching one memory reads it in full.
	one := runMemoryTool(t, tool, `{"action":"read","query":"build"}`)
	require.Contains(t, one.Content, "just build")

	// A query matching several memories returns the index with content
	// snippets, so the follow-up read can name the right id without
	// extra calls.
	many := runMemoryTool(t, tool, `{"action":"read","query":"commands"}`)
	require.Contains(t, many.Content, "2 memories")
	require.Contains(t, many.Content, "[build-commands]")
	require.Contains(t, many.Content, "[test-commands]")
	require.Contains(t, many.Content, "just build")

	// A title works the same way as a query.
	byTitle := runMemoryTool(t, tool, `{"action":"read","title":"Build commands"}`)
	require.Contains(t, byTitle.Content, "just build")

	// No match is an error result naming the query.
	none := runMemoryTool(t, tool, `{"action":"read","query":"nope"}`)
	require.True(t, none.IsError)
	require.Contains(t, none.Content, "no memory matching")
}

func TestMemoryToolValidation(t *testing.T) {
	tool := newMemoryToolForTest(t)

	_, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"flurb"}`})
	require.ErrorContains(t, err, "invalid action")

	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"save","content":"no title"}`})
	require.ErrorContains(t, err, "title is required")

	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"read"}`})
	require.ErrorContains(t, err, "id or query is required")

	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"search"}`})
	require.ErrorContains(t, err, "query is required")

	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"save","title":"t","content":"c","category":"bogus"}`})
	require.ErrorContains(t, err, "invalid category")

	_, err = tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"read","id":"nope"}`})
	require.ErrorContains(t, err, "not found")
}

func TestMemoryToolEditFuzzyTitle(t *testing.T) {
	tool := newMemoryToolForTest(t)

	runMemoryTool(t, tool, `{"action":"save","title":"Diagnostics relay","content":"the sweep runs at turn end"}`)

	// A slightly-off title still finds and updates the existing memory,
	// without renaming it and without duplicating it.
	edited := runMemoryTool(t, tool, `{"action":"edit","title":"Diagnostics relays","content":"sweep runs when a turn ends"}`)
	require.Contains(t, edited.Content, "Edited memory")
	require.Contains(t, edited.Content, "[diagnostics-relay]")
	require.Contains(t, edited.Content, "Matched by fuzzy title")

	listed := runMemoryTool(t, tool, `{"action":"list"}`)
	require.Contains(t, listed.Content, "1 memories", "fuzzy edit must not duplicate")

	read := runMemoryTool(t, tool, `{"action":"read","id":"diagnostics-relay"}`)
	require.Contains(t, read.Content, "(project) Diagnostics relay", "fuzzy edit must not rename")
	require.Contains(t, read.Content, "sweep runs when a turn ends")
}

func TestMemoryToolEditAmbiguousTitleAsks(t *testing.T) {
	tool := newMemoryToolForTest(t)

	runMemoryTool(t, tool, `{"action":"save","title":"Build commands","content":"just build"}`)
	runMemoryTool(t, tool, `{"action":"save","title":"Test commands","content":"just test"}`)

	// A title that plausibly matches several memories must not guess.
	_, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"action":"edit","title":"commands","content":"x"}`})
	require.ErrorContains(t, err, "closest matches")
	require.ErrorContains(t, err, "[build-commands]")
	require.ErrorContains(t, err, "[test-commands]")

	// Editing by id still works when the title is ambiguous.
	byID := runMemoryTool(t, tool, `{"action":"edit","id":"build-commands","content":"go build ./..."}`)
	require.Contains(t, byID.Content, "Edited memory")
}

func TestMemoryToolSaveWarnsOnNearDuplicate(t *testing.T) {
	tool := newMemoryToolForTest(t)

	runMemoryTool(t, tool, `{"action":"save","title":"Build commands","content":"just build"}`)

	dup := runMemoryTool(t, tool, `{"action":"save","title":"Build command setup","content":"go build ./..."}`)
	require.Contains(t, dup.Content, "near-duplicate")
	require.Contains(t, dup.Content, "[build-commands]")

	// Re-saving the same title upserts and must not warn.
	same := runMemoryTool(t, tool, `{"action":"save","title":"Build commands","content":"just build && just test"}`)
	require.Contains(t, same.Content, "Updated memory")
	require.NotContains(t, same.Content, "near-duplicate")
}
