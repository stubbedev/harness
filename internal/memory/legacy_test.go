package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/db"
)

// oldMemory is a row of a per-workspace memories table.
type oldMemory struct {
	id, category, title, content          string
	pinned, useCount, lastUsed, createdAt int64
	updatedAt                             int64
}

// newOldWorkspace builds a per-workspace database the way earlier
// versions did, with the real workspace migrations, holding memories.
func newOldWorkspace(t *testing.T, dataDir string, memories ...oldMemory) {
	t.Helper()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	for _, m := range memories {
		_, err := conn.ExecContext(t.Context(), `INSERT INTO memories
(id, category, title, content, pinned, use_count, last_used_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.id, m.category, m.title, m.content, m.pinned, m.useCount, m.lastUsed, m.createdAt, m.updatedAt)
		require.NoError(t, err)
	}
	// Closed cleanly, as an idle workspace's database is: no WAL or
	// shared-memory file is left for the read-only open to lean on.
	require.NoError(t, db.Release(dataDir))
}

// storedMemory is a row of the shared store, as the import left it.
type storedMemory struct {
	scope, repoKey, id, title, content             string
	pinned, useCount, lastUsed, createdAt, updated int64
	hasEmbedding                                   bool
}

func storedMemories(t *testing.T, store *Store) map[string]storedMemory {
	t.Helper()
	rows, err := store.conn.QueryContext(t.Context(), `SELECT scope, repo_key, id, title, content, pinned,
       use_count, last_used_at, created_at, updated_at, length(embedding) > 0
FROM memories`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]storedMemory{}
	for rows.Next() {
		var m storedMemory
		require.NoError(t, rows.Scan(&m.scope, &m.repoKey, &m.id, &m.title, &m.content, &m.pinned,
			&m.useCount, &m.lastUsed, &m.createdAt, &m.updated, &m.hasEmbedding))
		out[m.scope+"|"+m.repoKey+"|"+m.id] = m
	}
	require.NoError(t, rows.Err())
	return out
}

func TestImportLegacyMergesWorkspaces(t *testing.T) {
	requireGit(t)

	root := t.TempDir()
	workspaces := filepath.Join(root, "workspaces")

	// Two clones of one upstream, each with its own workspace database,
	// and a third workspace in a repository of its own.
	cloneA, cloneB, other := newRepo(t), newRepo(t), newRepo(t)
	git(t, cloneA, "remote", "add", "origin", "git@github.com:team/shared.git")
	git(t, cloneB, "remote", "add", "origin", "https://github.com/team/shared")
	git(t, other, "remote", "add", "origin", "https://example.com/team/other")

	newOldWorkspace(t, filepath.Join(workspaces, "aaa-shared"),
		oldMemory{
			id: "prefers-terse-replies", category: "user", title: "Prefers terse replies", content: "old wording",
			useCount: 2, lastUsed: 90, createdAt: 50, updatedAt: 100,
		},
		oldMemory{
			id: "run-tests-with-race", category: "feedback", title: "Run tests with race", content: "always -race",
			useCount: 1, lastUsed: 10, createdAt: 10, updatedAt: 10,
		},
		oldMemory{
			id: "build", category: "project", title: "Build", content: "just build",
			pinned: 1, useCount: 4, lastUsed: 70, createdAt: 30, updatedAt: 300,
		},
	)
	newOldWorkspace(t, filepath.Join(workspaces, "bbb-shared"),
		oldMemory{
			id: "prefers-terse-replies", category: "user", title: "Prefers terse replies", content: "new wording",
			pinned: 1, useCount: 3, lastUsed: 80, createdAt: 60, updatedAt: 200,
		},
		oldMemory{
			id: "build", category: "project", title: "Build", content: "make",
			useCount: 1, lastUsed: 99, createdAt: 20, updatedAt: 250,
		},
		oldMemory{
			id: "docs", category: "reference", title: "Docs", content: "see wiki",
			createdAt: 5, updatedAt: 5,
		},
	)
	newOldWorkspace(t, filepath.Join(workspaces, "ccc-other"),
		oldMemory{id: "build", category: "project", title: "Build", content: "cargo build", createdAt: 1, updatedAt: 1},
	)
	// A workspace whose project directory is gone, one nothing records
	// a project for, one that never had a database, and a corrupt one.
	newOldWorkspace(t, filepath.Join(workspaces, "ddd-gone"),
		oldMemory{id: "gone", category: "project", title: "Gone", content: "c", createdAt: 1, updatedAt: 1})
	newOldWorkspace(t, filepath.Join(workspaces, "eee-unknown"),
		oldMemory{id: "orphan", category: "reference", title: "Orphan", content: "c", createdAt: 1, updatedAt: 1})
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "fff-nodb", "logs"), 0o755))
	corrupt := filepath.Join(workspaces, "ggg-corrupt")
	require.NoError(t, os.MkdirAll(corrupt, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(corrupt, "harness.db"), []byte("not a database at all, just bytes"), 0o644))

	// projects.json as the real data root would hold it: the data
	// directories under a root the data has since moved from.
	moved := filepath.Join("/elsewhere", "harness", "workspaces")
	projects := map[string]string{
		filepath.Join(moved, "aaa-shared"):  cloneA,
		filepath.Join(moved, "bbb-shared"):  filepath.Join(cloneB, "sub", "dir"),
		filepath.Join(moved, "ccc-other"):   other,
		filepath.Join(moved, "ddd-gone"):    filepath.Join(root, "deleted-project"),
		filepath.Join(moved, "ggg-corrupt"): other,
	}
	require.NoError(t, os.MkdirAll(filepath.Join(cloneB, "sub", "dir"), 0o755))

	store := newTestStore(t, filepath.Join(root, "memory"))
	sources := LegacySources(workspaces, projects)
	require.Len(t, sources, 7)

	report, err := store.ImportLegacy(t.Context(), sources)
	require.NoError(t, err)
	require.Equal(t, ImportReport{Sources: 6, Memories: 9, Failed: 1}, report)

	got := storedMemories(t, store)
	require.Len(t, got, 7)

	terse := got["global||prefers-terse-replies"]
	require.Equal(t, "new wording", terse.content, "the most recently updated content wins")
	require.EqualValues(t, 1, terse.pinned, "pinned in either is pinned")
	require.EqualValues(t, 5, terse.useCount, "use counts add up")
	require.EqualValues(t, 90, terse.lastUsed, "the latest use is kept")
	require.EqualValues(t, 50, terse.createdAt, "the earliest creation is kept")
	require.EqualValues(t, 200, terse.updated)
	require.True(t, terse.hasEmbedding)

	require.Contains(t, got, "global||run-tests-with-race")

	shared := "github.com/team/shared"
	build := got["repo|"+shared+"|build"]
	require.Equal(t, "just build", build.content, "both clones' project memories merge under their upstream")
	require.EqualValues(t, 1, build.pinned)
	require.EqualValues(t, 5, build.useCount)
	require.EqualValues(t, 99, build.lastUsed)
	require.EqualValues(t, 20, build.createdAt)
	require.Contains(t, got, "repo|"+shared+"|docs")

	require.Equal(t, "cargo build", got["repo|example.com/team/other|build"].content)
	require.Contains(t, got, "repo|path:"+filepath.Join(root, "deleted-project")+"|gone")
	require.Contains(t, got, "repo|workspace:eee-unknown|orphan")

	// A second open imports nothing more, and retries only the corrupt
	// workspace.
	again, err := store.ImportLegacy(t.Context(), sources)
	require.NoError(t, err)
	require.Equal(t, ImportReport{Failed: 1}, again)
	require.Equal(t, got, storedMemories(t, store))

	// The old databases are left as they were.
	conn, err := db.ConnectReadOnly(t.Context(), filepath.Join(workspaces, "aaa-shared", "harness.db"))
	require.NoError(t, err)
	defer conn.Close()
	var count int
	require.NoError(t, conn.QueryRowContext(t.Context(), "SELECT count(*) FROM memories").Scan(&count))
	require.Equal(t, 3, count)

	// The imported memories serve a workspace of the shared upstream.
	svc := newScopedService(t, store, RepoKey(t.Context(), cloneB))
	index, err := svc.Index(t.Context(), 0)
	require.NoError(t, err)
	require.Equal(t, "Global:\n"+
		"- (user, pinned) Prefers terse replies\n"+
		"- (feedback) Run tests with race\n"+
		"This repository:\n"+
		"- (project, pinned) Build\n"+
		"- (reference) Docs", index)
}

func TestImportLegacyKeepsNewerStoreContent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newTestStore(t, filepath.Join(root, "memory"))
	svc := newScopedService(t, store, testRepoKey)
	_, err := svc.Save(t.Context(), SaveInput{Title: "Editor", Content: "helix", Category: CategoryUser})
	require.NoError(t, err)

	newOldWorkspace(t, filepath.Join(root, "workspaces", "old"),
		oldMemory{id: "editor", category: "user", title: "Editor", content: "vim", useCount: 7, createdAt: 1, updatedAt: 1})
	_, err = store.ImportLegacy(t.Context(), LegacySources(filepath.Join(root, "workspaces"), nil))
	require.NoError(t, err)

	got, err := svc.Get(t.Context(), ScopeGlobal, "editor")
	require.NoError(t, err)
	require.Equal(t, "helix", got.Content, "a memory saved since outranks the imported one")
	require.EqualValues(t, 8, got.UseCount)
	require.EqualValues(t, 1, got.CreatedAt)
}

func TestLegacySourcesIncludeDataDirectoriesElsewhere(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspaces := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(filepath.Join(workspaces, "abc-proj"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaces, "stray-file"), nil, 0o644))
	custom := filepath.Join(root, "repo", ".harness")

	sources := LegacySources(workspaces, map[string]string{
		filepath.Join(workspaces, "abc-proj"): "/src/proj",
		custom:                                filepath.Join(root, "repo"),
	})
	require.Equal(t, []LegacySource{
		{Name: "abc-proj", DBPath: filepath.Join(workspaces, "abc-proj", "harness.db"), ProjectPath: "/src/proj"},
		{Name: custom, DBPath: filepath.Join(custom, "harness.db"), ProjectPath: filepath.Join(root, "repo")},
	}, sources)

	require.Empty(t, LegacySources(filepath.Join(root, "missing"), nil))
}
