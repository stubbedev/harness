package memory

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/db"
)

func newTestService(t *testing.T, reap func() int) Service {
	t.Helper()
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	opts := []Option{WithScrubber(func(s string) (string, int) { return s, 0 })}
	if reap != nil {
		opts = append(opts, WithReapLimit(reap))
	}
	return NewService(db.New(conn), conn, opts...)
}

func TestSaveCreatesThenUpdatesByTitle(t *testing.T) {
	svc := newTestService(t, nil)

	created, err := svc.Save(t.Context(), SaveInput{
		Title:   "Build commands",
		Content: "just build",
	})
	require.NoError(t, err)
	require.True(t, created.Created)
	require.Equal(t, "build-commands", created.Item.ID)
	require.Equal(t, CategoryProject, created.Item.Category)
	require.False(t, created.Item.Pinned)

	updated, err := svc.Save(t.Context(), SaveInput{
		Title:   "Build Commands", // case-insensitive slug match
		Content: "just build && just test",
	})
	require.NoError(t, err)
	require.False(t, updated.Created)
	require.Equal(t, created.Item.ID, updated.Item.ID)

	items, err := svc.List(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1, "re-saving the same title must not duplicate")
	require.Equal(t, "just build && just test", items[0].Content)
}

func TestSaveByExplicitIDKeepsPinnedWhenOmitted(t *testing.T) {
	svc := newTestService(t, nil)

	pin := true
	first, err := svc.Save(t.Context(), SaveInput{
		ID:      "custom-id",
		Title:   "First title",
		Content: "one",
		Pinned:  &pin,
	})
	require.NoError(t, err)
	require.True(t, first.Item.Pinned)

	second, err := svc.Save(t.Context(), SaveInput{
		ID:      "custom-id",
		Title:   "Renamed",
		Content: "two",
	})
	require.NoError(t, err)
	require.False(t, second.Created)
	require.True(t, second.Item.Pinned, "nil Pinned must keep the existing value")
	require.Equal(t, "Renamed", second.Item.Title)
}

func TestSaveValidatesInput(t *testing.T) {
	svc := newTestService(t, nil)

	_, err := svc.Save(t.Context(), SaveInput{Content: "x"})
	require.ErrorContains(t, err, "title")

	_, err = svc.Save(t.Context(), SaveInput{Title: "t", Content: "  "})
	require.ErrorContains(t, err, "content")

	_, err = svc.Save(t.Context(), SaveInput{Title: "t", Content: "x", Category: "nope"})
	require.ErrorContains(t, err, "invalid category")
}

func TestGetTouchesUsageAndReportsNotFound(t *testing.T) {
	svc := newTestService(t, nil)

	saved, err := svc.Save(t.Context(), SaveInput{Title: "t", Content: "c"})
	require.NoError(t, err)

	got, err := svc.Get(t.Context(), saved.Item.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, got.UseCount)

	got, err = svc.Get(t.Context(), saved.Item.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, got.UseCount)

	_, err = svc.Get(t.Context(), "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestGetByTitle(t *testing.T) {
	svc := newTestService(t, nil)

	_, err := svc.Save(t.Context(), SaveInput{Title: "Build commands", Content: "just build"})
	require.NoError(t, err)

	got, err := svc.GetByTitle(t.Context(), "build COMMANDS")
	require.NoError(t, err)
	require.Equal(t, "build-commands", got.ID)

	_, err = svc.GetByTitle(t.Context(), "nope")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSearchMatchesTitleAndContent(t *testing.T) {
	svc := newTestService(t, nil)

	_, err := svc.Save(t.Context(), SaveInput{Title: "Deploy flow", Content: "uses herdr"})
	require.NoError(t, err)
	_, err = svc.Save(t.Context(), SaveInput{Title: "Other", Content: "nothing here"})
	require.NoError(t, err)

	byContent, err := svc.Search(t.Context(), "herdr")
	require.NoError(t, err)
	require.Len(t, byContent, 1)
	require.Equal(t, "Deploy flow", byContent[0].Title)
	require.EqualValues(t, 1, byContent[0].UseCount, "search hits are touched")

	byTitle, err := svc.Search(t.Context(), "deploy")
	require.NoError(t, err)
	require.Len(t, byTitle, 1)

	_, err = svc.Search(t.Context(), "  ")
	require.ErrorContains(t, err, "query is required")
}

func TestDelete(t *testing.T) {
	svc := newTestService(t, nil)

	saved, err := svc.Save(t.Context(), SaveInput{Title: "t", Content: "c"})
	require.NoError(t, err)

	require.NoError(t, svc.Delete(t.Context(), saved.Item.ID))
	require.ErrorIs(t, svc.Delete(t.Context(), saved.Item.ID), ErrNotFound)
}

func TestIndexRespectsBudget(t *testing.T) {
	svc := newTestService(t, nil)

	for _, title := range []string{"alpha", "beta", "gamma"} {
		_, err := svc.Save(t.Context(), SaveInput{Title: title, Content: "c"})
		require.NoError(t, err)
	}

	full, err := svc.Index(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, strings.Split(full, "\n"), 3)

	// Fits "- (project) alpha" but not the second line plus its
	// newline separator.
	tight, err := svc.Index(t.Context(), len("- (project) alpha")+len("- (project) beta"))
	require.NoError(t, err)
	require.Contains(t, tight, "alpha")
	require.NotContains(t, tight, "gamma")
	require.Contains(t, tight, "(+2 more")
}

func TestReapEvictsLeastUsedUnpinned(t *testing.T) {
	svc := newTestService(t, func() int { return 3 })

	pin := true
	_, err := svc.Save(t.Context(), SaveInput{Title: "pinned", Content: "keep me", Pinned: &pin})
	require.NoError(t, err)
	_, err = svc.Save(t.Context(), SaveInput{Title: "used", Content: "read once"})
	require.NoError(t, err)
	_, err = svc.Save(t.Context(), SaveInput{Title: "stale", Content: "never read"})
	require.NoError(t, err)

	_, err = svc.Get(t.Context(), "used")
	require.NoError(t, err)

	// The fourth save exceeds the limit of 3; the never-read unpinned
	// memory is evicted and the pinned one kept.
	_, err = svc.Save(t.Context(), SaveInput{Title: "fresh", Content: "newest"})
	require.NoError(t, err)

	items, err := svc.List(t.Context())
	require.NoError(t, err)
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	require.ElementsMatch(t, []string{"pinned", "used", "fresh"}, ids)
}

func TestSaveScrubsSecrets(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	svc := NewService(db.New(conn), conn)

	result, err := svc.Save(t.Context(), SaveInput{
		Title:   "Creds",
		Content: "deploy with api_key = abcdefghijklmnop and sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.Redactions)
	require.NotContains(t, result.Item.Content, "abcdefghijklmnop")
	require.NotContains(t, result.Item.Content, "sk-ant-")
	require.Contains(t, result.Item.Content, Redacted)

	_, err = svc.Get(t.Context(), result.Item.ID)
	require.NoError(t, err)
	_, err = svc.Get(t.Context(), "nope")
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Build commands":       "build-commands",
		"  spaced   out  ":     "spaced-out",
		"Weird!@#$Characters":  "weird-characters",
		"Trailing hyphens ---": "trailing-hyphens",
		"":                     "",
		"UPPER lower 123":      "upper-lower-123",
	}
	for in, want := range cases {
		require.Equal(t, want, Slug(in), in)
	}

	long := strings.Repeat("a very long title ", 10)
	require.LessOrEqual(t, len(Slug(long)), MaxIDLen)

	// Titles whose letters the ASCII slug would drop must still get
	// distinct, stable, non-empty IDs.
	ja, zh := Slug("日本 notes"), Slug("中国 notes")
	require.NotEqual(t, ja, zh)
	require.Equal(t, ja, Slug("日本 notes"))
	require.True(t, strings.HasPrefix(ja, "notes-m-"), ja)
	require.NotEmpty(t, Slug("日本語メモ"))
	require.LessOrEqual(t, len(Slug(strings.Repeat("日本 notes ", 20))), MaxIDLen)
}

func TestTruncateKeepsRunes(t *testing.T) {
	t.Parallel()
	got := truncate("aé", 2)
	require.Equal(t, "a", got)
	require.True(t, utf8.ValidString(got))
}
