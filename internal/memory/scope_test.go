package memory

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func newScopedService(t *testing.T, store *Store, repoKey string, opts ...Option) Service {
	t.Helper()
	opts = append([]Option{WithScrubber(func(s string) (string, int) { return s, 0 })}, opts...)
	return NewService(store, repoKey, opts...)
}

func titles(t *testing.T, svc Service) []string {
	t.Helper()
	items, err := svc.List(t.Context())
	require.NoError(t, err)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, string(item.Scope)+":"+item.Title)
	}
	return out
}

func TestWorkspacesShareRepoMemoriesByUpstream(t *testing.T) {
	t.Parallel()
	requireGit(t)

	store := newTestStore(t, t.TempDir())

	// Two clones of one upstream, reached over different transports, and
	// a clone of another.
	cloneA, cloneB, other := newRepo(t), newRepo(t), newRepo(t)
	git(t, cloneA, "remote", "add", "origin", "git@github.com:stubbedev/harness.git")
	git(t, cloneB, "remote", "add", "origin", "https://github.com/stubbedev/harness")
	git(t, other, "remote", "add", "origin", "https://github.com/stubbedev/other")

	a := newScopedService(t, store, RepoKey(t.Context(), cloneA))
	b := newScopedService(t, store, RepoKey(t.Context(), cloneB))
	c := newScopedService(t, store, RepoKey(t.Context(), other))

	_, err := a.Save(t.Context(), SaveInput{Title: "Build with just", Content: "just build", Category: CategoryProject})
	require.NoError(t, err)
	_, err = b.Save(t.Context(), SaveInput{Title: "Prefers terse replies", Content: "keep it short", Category: CategoryUser})
	require.NoError(t, err)
	_, err = c.Save(t.Context(), SaveInput{Title: "Other repo fact", Content: "only here", Category: CategoryReference})
	require.NoError(t, err)

	require.Equal(t, []string{"global:Prefers terse replies", "repo:Build with just"}, titles(t, a))
	require.Equal(t, []string{"global:Prefers terse replies", "repo:Build with just"}, titles(t, b))
	require.Equal(t, []string{"global:Prefers terse replies", "repo:Other repo fact"}, titles(t, c))

	found, err := c.Search(t.Context(), "build just")
	require.NoError(t, err)
	for _, item := range found {
		require.NotEqual(t, "Build with just", item.Title, "search must not reach another repository")
	}
}

func TestSaveDefaultsScopeByCategory(t *testing.T) {
	t.Parallel()

	svc := newScopedService(t, newTestStore(t, t.TempDir()), testRepoKey)
	cases := []struct {
		category Category
		scope    Scope
		want     Scope
	}{
		{CategoryUser, "", ScopeGlobal},
		{CategoryFeedback, "", ScopeGlobal},
		{CategoryProject, "", ScopeRepo},
		{CategoryReference, "", ScopeRepo},
		{"", "", ScopeRepo},
		{CategoryUser, ScopeRepo, ScopeRepo},
		{CategoryProject, ScopeGlobal, ScopeGlobal},
	}
	for i, tc := range cases {
		result, err := svc.Save(t.Context(), SaveInput{
			Title:    fmt.Sprintf("note %d", i),
			Content:  "c",
			Category: tc.category,
			Scope:    tc.scope,
		})
		require.NoError(t, err)
		require.True(t, result.Created)
		require.Equal(t, tc.want, result.Item.Scope, "category %q scope %q", tc.category, tc.scope)
	}

	_, err := svc.Save(t.Context(), SaveInput{Title: "x", Content: "c", Scope: "everywhere"})
	require.ErrorContains(t, err, "invalid scope")
}

func TestSameTitleInBothScopes(t *testing.T) {
	t.Parallel()

	svc := newScopedService(t, newTestStore(t, t.TempDir()), testRepoKey)
	global, err := svc.Save(t.Context(), SaveInput{Title: "Style", Content: "global style", Scope: ScopeGlobal})
	require.NoError(t, err)
	repo, err := svc.Save(t.Context(), SaveInput{Title: "Style", Content: "repo style", Scope: ScopeRepo})
	require.NoError(t, err)
	require.True(t, repo.Created, "the same title in another scope is another memory")
	require.Equal(t, global.Item.ID, repo.Item.ID)

	// Look-ups see both, the repository's first.
	found, err := svc.Lookup(t.Context(), "style")
	require.NoError(t, err)
	require.Len(t, found, 2)
	require.Equal(t, ScopeRepo, found[0].Scope)
	require.Equal(t, ScopeGlobal, found[1].Scope)
	found, err = svc.LookupTitle(t.Context(), "STYLE")
	require.NoError(t, err)
	require.Len(t, found, 2)

	// Without a scope a save updates the more specific memory.
	updated, err := svc.Save(t.Context(), SaveInput{Title: "Style", Content: "repo style v2"})
	require.NoError(t, err)
	require.False(t, updated.Created)
	require.Equal(t, ScopeRepo, updated.Item.Scope)

	got, err := svc.Get(t.Context(), ScopeGlobal, "style")
	require.NoError(t, err)
	require.Equal(t, "global style", got.Content)

	require.NoError(t, svc.Delete(t.Context(), ScopeGlobal, "style"))
	found, err = svc.Lookup(t.Context(), "style")
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "repo style v2", found[0].Content)
}

// A save that names no scope updates the memory the workspace already
// has under that title, wherever it lives, instead of minting a second
// one in the category's default scope.
func TestSaveWithoutScopeUpdatesExistingGlobal(t *testing.T) {
	t.Parallel()

	svc := newScopedService(t, newTestStore(t, t.TempDir()), testRepoKey)
	_, err := svc.Save(t.Context(), SaveInput{Title: "Editor", Content: "helix", Category: CategoryUser})
	require.NoError(t, err)

	result, err := svc.Save(t.Context(), SaveInput{Title: "Editor", Content: "helix, sometimes vim"})
	require.NoError(t, err)
	require.False(t, result.Created)
	require.Equal(t, ScopeGlobal, result.Item.Scope)
	require.Equal(t, CategoryUser, result.Item.Category, "an omitted category keeps the stored one")
	require.Equal(t, []string{"global:Editor"}, titles(t, svc))
}

func TestIndexCoversBothScopesGlobalFirst(t *testing.T) {
	t.Parallel()

	svc := newScopedService(t, newTestStore(t, t.TempDir()), testRepoKey)
	_, err := svc.Save(t.Context(), SaveInput{Title: "Repo fact", Content: "c", Category: CategoryProject})
	require.NoError(t, err)
	_, err = svc.Save(t.Context(), SaveInput{Title: "User fact", Content: "c", Category: CategoryUser})
	require.NoError(t, err)

	index, err := svc.Index(t.Context(), 0)
	require.NoError(t, err)
	require.Equal(t, "Global:\n- (user) User fact\nThis repository:\n- (project) Repo fact", index)
}

func TestIndexBudgetKeepsBothScopes(t *testing.T) {
	t.Parallel()

	const budget = 400
	cases := map[string]struct {
		flood Category
		other string
	}{
		"global flood": {CategoryUser, "Lone repo fact"},
		"repo flood":   {CategoryProject, "Lone user fact"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc := newScopedService(t, newTestStore(t, t.TempDir()), testRepoKey)
			for i := range 60 {
				_, err := svc.Save(t.Context(), SaveInput{Title: fmt.Sprintf("flood note number %02d", i), Content: "c", Category: tc.flood})
				require.NoError(t, err)
			}
			_, err := svc.Save(t.Context(), SaveInput{Title: "Lone user fact", Content: "c", Category: CategoryUser})
			require.NoError(t, err)
			_, err = svc.Save(t.Context(), SaveInput{Title: "Lone repo fact", Content: "c", Category: CategoryProject})
			require.NoError(t, err)

			index, err := svc.Index(t.Context(), budget)
			require.NoError(t, err)
			require.Contains(t, index, tc.other, "a flood in one scope must not push the other out")
			require.Less(t, strings.Index(index, "Global:"), strings.Index(index, "This repository:"))
			require.Contains(t, index, "more: use the memory tool")

			// The listed lines fit the budget; only the pointers at the
			// list action are extra.
			size := 0
			for line := range strings.SplitSeq(index, "\n") {
				if !strings.HasPrefix(line, "(+") {
					size += len(line) + 1
				}
			}
			require.LessOrEqual(t, size-1, budget)
		})
	}
}

func TestIndexBudgetGivesUnusedShareToOtherScope(t *testing.T) {
	t.Parallel()

	svc := newScopedService(t, newTestStore(t, t.TempDir()), testRepoKey)
	_, err := svc.Save(t.Context(), SaveInput{Title: "Only global", Content: "c", Category: CategoryUser})
	require.NoError(t, err)
	for i := range 20 {
		_, err := svc.Save(t.Context(), SaveInput{Title: fmt.Sprintf("repo note %02d", i), Content: "c"})
		require.NoError(t, err)
	}
	full, err := svc.Index(t.Context(), 0)
	require.NoError(t, err)

	// The whole index fits once the small global section leaves its
	// half to the repository.
	index, err := svc.Index(t.Context(), len(full))
	require.NoError(t, err)
	require.Equal(t, full, index)
}

func TestReapIsPerScope(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, t.TempDir())
	limit := func() int { return 2 }
	a := newScopedService(t, store, "example.com/a", WithReapLimit(limit))
	b := newScopedService(t, store, "example.com/b", WithReapLimit(limit))

	for _, title := range []string{"a1", "a2", "a3"} {
		_, err := a.Save(t.Context(), SaveInput{Title: title, Content: "c"})
		require.NoError(t, err)
	}
	for _, title := range []string{"g1", "g2"} {
		_, err := a.Save(t.Context(), SaveInput{Title: title, Content: "c", Category: CategoryUser})
		require.NoError(t, err)
	}
	_, err := b.Save(t.Context(), SaveInput{Title: "b1", Content: "c"})
	require.NoError(t, err)

	// Repository a kept two of its three; the global memories and
	// repository b's are within their own limits.
	got := titles(t, a)
	require.Len(t, got, 4, got)
	require.Contains(t, got, "global:g1")
	require.Contains(t, got, "global:g2")
	require.ElementsMatch(t, []string{"global:g1", "global:g2", "repo:b1"}, titles(t, b))
}

// Harness processes on one machine share the store: each opens its own
// connection, and their saves, reads and look-ups interleave. Real
// processes, so nothing in-process (the connection pool, a mutex) can
// hide a SQLITE_BUSY.
func TestConcurrentProcessesShareStore(t *testing.T) {
	if os.Getenv(helperStoreDir) != "" {
		runStoreHelper(t)
		return
	}
	t.Parallel()

	dir := t.TempDir()
	const procs, saves = 4, 25
	var wg sync.WaitGroup
	errs := make(chan error, procs)
	for p := range procs {
		wg.Go(func() {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestConcurrentProcessesShareStore$", "-test.count=1")
			cmd.Env = append(os.Environ(),
				helperStoreDir+"="+dir,
				helperProc+"="+strconv.Itoa(p),
				helperSaves+"="+strconv.Itoa(saves),
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				errs <- fmt.Errorf("helper %d: %w\n%s", p, err, out)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	svc := newScopedService(t, newTestStore(t, dir), testRepoKey)
	items, err := svc.List(t.Context())
	require.NoError(t, err)
	// Each process saved its own notes and all of them upserted one
	// shared global note.
	require.Len(t, items, procs*saves+1)
	shared, err := svc.Get(t.Context(), ScopeGlobal, "shared-note")
	require.NoError(t, err)
	require.Positive(t, shared.UseCount)
}

const (
	helperStoreDir = "HARNESS_TEST_MEMORY_STORE_DIR"
	helperProc     = "HARNESS_TEST_MEMORY_PROC"
	helperSaves    = "HARNESS_TEST_MEMORY_SAVES"
)

func runStoreHelper(t *testing.T) {
	proc := os.Getenv(helperProc)
	saves, err := strconv.Atoi(os.Getenv(helperSaves))
	require.NoError(t, err)
	svc := newScopedService(t, newTestStore(t, os.Getenv(helperStoreDir)), testRepoKey)
	for i := range saves {
		_, err := svc.Save(t.Context(), SaveInput{Title: fmt.Sprintf("proc %s note %d", proc, i), Content: "c"})
		require.NoError(t, err)
		_, err = svc.Save(t.Context(), SaveInput{Title: "Shared note", Content: "from " + proc, Category: CategoryUser})
		require.NoError(t, err)
		_, err = svc.Get(t.Context(), ScopeGlobal, "shared-note")
		require.NoError(t, err)
		_, err = svc.Search(t.Context(), "note")
		require.NoError(t, err)
	}
}
