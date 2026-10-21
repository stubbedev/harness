package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func manyMatchesFixture(tb testing.TB, files, perFile int) string {
	tb.Helper()
	root := tb.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := range files {
		var b strings.Builder
		for j := range perFile {
			fmt.Fprintf(&b, "func f%d(ctx context.Context) {}\n// filler line %d\n", j, j)
		}
		path := filepath.Join(root, fmt.Sprintf("f%03d.go", i))
		require.NoError(tb, os.WriteFile(path, []byte(b.String()), 0o644))
		at := base.Add(time.Duration(i) * time.Second)
		require.NoError(tb, os.Chtimes(path, at, at))
	}
	return root
}

type matchKey struct {
	path string
	line int
	char int
}

func matchKeys(matches []grepMatch) []matchKey {
	keys := make([]matchKey, len(matches))
	for i, m := range matches {
		keys[i] = matchKey{m.path, m.lineNum, m.charNum}
	}
	return keys
}

func TestRipgrepSearchKeepsTheNewestMatchesUpToTheLimit(t *testing.T) {
	t.Parallel()
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := manyMatchesFixture(t, 20, 7)

	want, err := searchFilesWithRegex(`\bctx\b`, root, "")
	require.NoError(t, err)
	sort.SliceStable(want, func(i, j int) bool { return want[i].modTime.After(want[j].modTime) })

	for _, limit := range []int{1, 7, 100, len(want), len(want) + 10} {
		got, err := searchWithRipgrep(t.Context(), rg, `\bctx\b`, root, "", limit)
		require.NoError(t, err)
		require.Equal(t, matchKeys(want[:min(limit, len(want))]), matchKeys(got), "limit %d", limit)
	}
}

func TestRipgrepSearchWithNoMatchesIsEmpty(t *testing.T) {
	t.Parallel()
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := manyMatchesFixture(t, 3, 2)
	got, err := searchWithRipgrep(t.Context(), rg, "nothing_matches_this", root, "", 100)
	require.NoError(t, err)
	require.Empty(t, got)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read past the limit") }

func TestReadRipgrepMatchesStopsAtTheLimit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("ctx\nctx\nctx\n"), 0o644))

	match := func(line int) string {
		return fmt.Sprintf(`{"type":"match","data":{"path":{"text":"a.go"},"lines":{"text":"ctx\n"},"line_number":%d,"submatches":[{"match":{"text":"ctx"},"start":0,"end":3}]}}`+"\n", line)
	}
	stream := `{"type":"begin","data":{"path":{"text":"a.go"}}}` + "\n" +
		match(1) +
		`{"type":"context","data":{"path":{"text":"a.go"}}}` + "\n" +
		match(2)

	got, err := readRipgrepMatches(io.MultiReader(strings.NewReader(stream), failingReader{}), root, 2)
	require.NoError(t, err)
	require.Equal(t, []matchKey{
		{filepath.Join(root, "a.go"), 1, 1},
		{filepath.Join(root, "a.go"), 2, 1},
	}, matchKeys(got))
}

func TestReadRipgrepMatchesSkipsNonMatchEvents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("  ctx \n"), 0o644))

	stream := `{"type":"begin","data":{"path":{"text":"a.go"}}}` + "\n" +
		`{"type":"match","data":{"path":{"text":"a.go"},"lines":{"text":"  ctx \n"},"line_number":1,"submatches":[{"match":{"text":"ctx"},"start":2,"end":5},{"match":{"text":"ctx"},"start":9,"end":12}]}}` + "\n" +
		`{"type":"end","data":{"path":{"text":"a.go"}}}` + "\n" +
		`{"type":"summary","data":{}}` + "\n"

	got, err := readRipgrepMatches(strings.NewReader(stream), root, 100)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 3, got[0].charNum)
	require.Equal(t, "ctx", got[0].lineText)
	require.Equal(t, "  ctx ", got[0].rawLine)
}

func BenchmarkRipgrepSymbolSearch(b *testing.B) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		b.Skip("ripgrep is not installed")
	}
	root := manyMatchesFixture(b, 300, 15)
	b.ReportAllocs()
	for b.Loop() {
		matches, err := searchWithRipgrep(b.Context(), rg, `\bctx\b`, root, "", 100)
		require.NoError(b, err)
		require.Len(b, matches, 100)
	}
}
