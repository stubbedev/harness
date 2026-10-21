package tools

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/memory"
)

func newMemoryToolForTest(t *testing.T) fantasy.AgentTool {
	t.Helper()
	store, err := memory.OpenStore(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return NewMemoryTool(memory.NewService(store, "example.com/team/tool"))
}

func runMemoryTool(t *testing.T, tool fantasy.AgentTool, input string) fantasy.ToolResponse {
	t.Helper()
	resp, err := tool.Run(t.Context(), fantasy.ToolCall{Input: input})
	require.NoError(t, err)
	return resp
}

// requireMemoryToolError runs input and requires an error result
// containing want, with no Go error: fantasy aborts the turn on one.
func requireMemoryToolError(t *testing.T, tool fantasy.AgentTool, input, want string) fantasy.ToolResponse {
	t.Helper()
	resp := runMemoryTool(t, tool, input)
	require.True(t, resp.IsError, "%s: %q", input, resp.Content)
	require.Contains(t, resp.Content, want, input)
	return resp
}

func TestMemoryToolRoundTrip(t *testing.T) {
	tool := newMemoryToolForTest(t)

	saved := runMemoryTool(t, tool, `{"action":"save","title":"Build commands","content":"just build","category":"project"}`)
	require.Contains(t, saved.Content, "Saved memory - [build-commands] (repo, project) Build commands")

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
	require.Contains(t, read.Content, "(repo, project)")
	require.NotContains(t, read.Content, "feedback")

	listed := runMemoryTool(t, tool, `{"action":"list"}`)
	require.Contains(t, listed.Content, "1 memories", "edit must not duplicate the memory")

	// Edit by id works too.
	byID := runMemoryTool(t, tool, `{"action":"edit","id":"build-commands","content":"just ci","category":"reference"}`)
	require.Contains(t, byID.Content, "just ci")
	require.Contains(t, byID.Content, "(repo, reference)")

	// A title that does not exist is an error result pointing at save.
	requireMemoryToolError(t, tool, `{"action":"edit","title":"Typo Title","content":"x"}`, "use save to create it")

	// Unknown id is an error result as well.
	requireMemoryToolError(t, tool, `{"action":"edit","id":"nope","content":"x"}`, `memory "nope" not found`)

	// Content is required.
	requireMemoryToolError(t, tool, `{"action":"edit","title":"Build commands"}`, "content is required")

	// Neither id nor title is an error result.
	requireMemoryToolError(t, tool, `{"action":"edit","content":"x"}`, "id or title is required")
	requireMemoryToolError(t, tool, `{"action":"edit","title":"  ","content":"x"}`, "id or title is required")
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

// TestMemoryToolValidation holds every input the model can get wrong to
// an error result rather than a Go error, which would abort the turn
// instead of letting the model retry.
func TestMemoryToolValidation(t *testing.T) {
	tool := newMemoryToolForTest(t)

	cases := map[string]string{
		`{"action":"flurb"}`:                                             `unknown action "flurb"; available: delete, edit, list, read, save, search`,
		`{"action":"save","content":"no title"}`:                         "title is required",
		`{"action":"save","title":"  ","content":"c"}`:                   "title is required",
		`{"action":"save","title":"t"}`:                                  "content is required",
		`{"action":"save","title":"t","content":"  "}`:                   "content is required",
		`{"action":"read"}`:                                              "id or query is required",
		`{"action":"search"}`:                                            "query is required",
		`{"action":"search","query":"  "}`:                               "query is required",
		`{"action":"delete"}`:                                            "id is required",
		`{"action":"save","title":"t","content":"c","category":"bogus"}`: "invalid category",
		`{"action":"list","scope":"galaxy"}`:                             "invalid scope",
		`{"action":"read","id":"nope"}`:                                  "not found",
		`{"action":"delete","id":"nope"}`:                                "not found",
	}
	for input, want := range cases {
		requireMemoryToolError(t, tool, input, want)
	}
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
	require.Contains(t, read.Content, "(repo, project) Diagnostics relay", "fuzzy edit must not rename")
	require.Contains(t, read.Content, "sweep runs when a turn ends")
}

func TestMemoryToolEditAmbiguousTitleAsks(t *testing.T) {
	tool := newMemoryToolForTest(t)

	runMemoryTool(t, tool, `{"action":"save","title":"Build commands","content":"just build"}`)
	runMemoryTool(t, tool, `{"action":"save","title":"Test commands","content":"just test"}`)

	// A title that plausibly matches several memories must not guess.
	ambiguous := requireMemoryToolError(t, tool, `{"action":"edit","title":"commands","content":"x"}`, "closest matches")
	require.Contains(t, ambiguous.Content, "[build-commands]")
	require.Contains(t, ambiguous.Content, "[test-commands]")

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

func TestMemoryToolScopes(t *testing.T) {
	tool := newMemoryToolForTest(t)

	// The category picks the scope of a new memory; scope overrides it.
	user := runMemoryTool(t, tool, `{"action":"save","title":"Style","content":"terse","category":"user"}`)
	require.Contains(t, user.Content, "[style] (global, user) Style")
	repo := runMemoryTool(t, tool, `{"action":"save","title":"Style","content":"gofumpt","category":"user","scope":"repo"}`)
	require.Contains(t, repo.Content, "Saved memory - [style] (repo, user) Style", "the same title in another scope is another memory")
	require.Contains(t, repo.Content, "near-duplicate memory exists: - [style] (global, user) Style")

	listed := runMemoryTool(t, tool, `{"action":"list"}`)
	require.Contains(t, listed.Content, "2 memories")
	globalOnly := runMemoryTool(t, tool, `{"action":"list","scope":"global"}`)
	require.Contains(t, globalOnly.Content, "1 memories")
	require.Contains(t, globalOnly.Content, "(global, user)")

	// Without a scope the repository's memory wins and the response
	// points at the global one.
	read := runMemoryTool(t, tool, `{"action":"read","id":"style"}`)
	require.Contains(t, read.Content, "(repo, user) Style")
	require.Contains(t, read.Content, "gofumpt")
	require.Contains(t, read.Content, `a global memory of the same name also exists; pass scope "global"`)
	read = runMemoryTool(t, tool, `{"action":"read","id":"style","scope":"global"}`)
	require.Contains(t, read.Content, "terse")
	require.NotContains(t, read.Content, "also exists")

	edited := runMemoryTool(t, tool, `{"action":"edit","title":"Style","scope":"global","content":"terse, no emojis"}`)
	require.Contains(t, edited.Content, "Edited memory - [style] (global, user) Style")

	deleted := runMemoryTool(t, tool, `{"action":"delete","id":"style"}`)
	require.Contains(t, deleted.Content, "Deleted memory - [style] (repo, user) Style")
	require.Contains(t, deleted.Content, "also exists")
	read = runMemoryTool(t, tool, `{"action":"read","id":"style"}`)
	require.Contains(t, read.Content, "terse, no emojis")

	requireMemoryToolError(t, tool, `{"action":"delete","id":"style","scope":"repo"}`, "not found")
	requireMemoryToolError(t, tool, `{"action":"list","scope":"galaxy"}`, "invalid scope")
}
