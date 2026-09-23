package memory

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// searchCorpus saves the notes every recall case in TestSearchRecall
// runs against.
func searchCorpus(t *testing.T, svc Service) {
	t.Helper()
	for _, in := range []SaveInput{
		{Title: "Diagnostics relay", Content: "the sweep runs when a turn ends and reports land in the transcript"},
		{Title: "Startup memory profile", Content: "the goose buffer fix keeps allocations flat at startup"},
		{Title: "Commit as you go", Content: "the user prefers small commits landed as work progresses"},
		{Title: "TUI palette filter", Content: "the skills palette ranks candidates with an fzf matcher"},
		{Title: "Homebrew tap", Content: "releases publish to the stubbedev homebrew tap"},
		{Title: "Floor sweep policy", Content: "sweep the kitchen floor weekly"},
	} {
		_, err := svc.Save(t.Context(), in)
		require.NoError(t, err)
	}
}

// TestSearchRecall is the search regression gate: one query per
// failure mode observed in practice, each required to surface the
// right memory first. The subtests share one database-backed service
// and stay sequential because the db pool these tests use is global.
func TestSearchRecall(t *testing.T) {
	svc := newTestService(t, nil)
	searchCorpus(t, svc)

	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"exact words", "turn end sweep", "diagnostics-relay"},
		{"word order scrambled", "sweep diagnostics relay", "diagnostics-relay"},
		{"inflected forms", "running sweeps at the end", "diagnostics-relay"},
		{"term prefix", "diag", "diagnostics-relay"},
		{"content terms", "goose buffer", "startup-memory-profile"},
		{"paraphrase of the title", "memory profile at startup", "startup-memory-profile"},
		{"multi term disambiguation", "sweep diagnostics", "diagnostics-relay"},
		{"typo", "daignostics relay", "diagnostics-relay"},
		{"subsequence net", "gns tcs", "diagnostics-relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := svc.Search(t.Context(), tc.query)
			require.NoError(t, err)
			require.NotEmpty(t, hits, "query %q must match something", tc.query)
			require.Equal(t, tc.want, hits[0].ID, "query %q", tc.query)
		})
	}
}

func TestSearchReturnsSnippet(t *testing.T) {
	svc := newTestService(t, nil)
	searchCorpus(t, svc)

	hits, err := svc.Search(t.Context(), "goose buffer")
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Contains(t, hits[0].Snippet, "goose")
}

func TestSearchNoHitReturnsEmpty(t *testing.T) {
	svc := newTestService(t, nil)
	searchCorpus(t, svc)

	hits, err := svc.Search(t.Context(), "zzqqx quantum blockchain")
	require.NoError(t, err)
	require.Empty(t, hits)

	hits, err = svc.Search(t.Context(), "-----")
	require.NoError(t, err)
	require.Empty(t, hits, "a query with no searchable terms matches nothing")
}

func TestSearchOrdersTitleHitsAboveContentHits(t *testing.T) {
	svc := newTestService(t, nil)
	searchCorpus(t, svc)

	hits, err := svc.Search(t.Context(), "homebrew")
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, "homebrew-tap", hits[0].ID)
}
