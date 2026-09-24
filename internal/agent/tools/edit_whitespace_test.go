package tools

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/filetracker"
)

func TestDiagnoseMismatch(t *testing.T) {
	t.Parallel()

	t.Run("tabs vs spaces", func(t *testing.T) {
		t.Parallel()
		content := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
		old := "func main() {\n    fmt.Println(\"hello\")\n}"
		hint := diagnoseMismatch(content, old)
		require.NotEmpty(t, hint)
		require.Contains(t, hint, "whitespace-normalized match")
		require.Contains(t, hint, "→")
		require.Contains(t, hint, "lines 1-3")
	})

	t.Run("wrong indent depth", func(t *testing.T) {
		t.Parallel()
		content := "func main() {\n\tif x {\n\t\tfmt.Println(\"deep\")\n\t}\n}\n"
		old := "if x {\n\tfmt.Println(\"deep\")\n}"
		hint := diagnoseMismatch(content, old)
		require.NotEmpty(t, hint)
		require.Contains(t, hint, "→")
	})

	t.Run("completely different text", func(t *testing.T) {
		t.Parallel()
		content := "package main\n\nfunc main() {}\n"
		old := "this text does not exist anywhere in the file at all"
		hint := diagnoseMismatch(content, old)
		require.Empty(t, hint)
	})

	t.Run("partial line match", func(t *testing.T) {
		t.Parallel()
		content := "func foo() {\n\tbar()\n\tbaz()\n}\n"
		old := "func foo() {\n\tbar()\n\tqux()\n}"
		hint := diagnoseMismatch(content, old)
		require.NotEmpty(t, hint)
		require.Contains(t, hint, "Closest match")
		require.Contains(t, hint, "×")
		require.Contains(t, hint, "your line: qux()")
	})

	t.Run("single-line typo anchors a fuzzy hint", func(t *testing.T) {
		t.Parallel()
		content := "import (\n\t\"github.com/stubbedev/harness/internal/ui/dialog\"\n)\n"
		old := "\t\"github.com/stubbeev/harness/internal/ui/dialog\""
		hint := diagnoseMismatch(content, old)
		require.NotEmpty(t, hint)
		require.Contains(t, hint, "Closest match")
		require.Contains(t, hint, "~")
		require.Contains(t, hint, "stubbedev")
	})

	t.Run("fuzzy hint for mangled multi-line window", func(t *testing.T) {
		t.Parallel()
		content := "func f() {\n\tif a {\n\t\treturn 1\n\t}\n\tif b {\n\t\treturn 2\n\t}\n\treturn 0\n}\n"
		old := "func f() {\n\tif a {\n\t\treturn 1\n\t}\n\tif b {\n\t\treturn 2\n\t}\n\treturn 3\n}"
		hint := diagnoseMismatch(content, old)
		require.NotEmpty(t, hint)
		require.Contains(t, hint, "Closest match")
	})

	t.Run("ambiguous error lists occurrence lines", func(t *testing.T) {
		t.Parallel()
		content := "x := 1\ny := 2\nx := 1\nz := 3\nx := 1\n"
		_, _, err := findAndReplace(nil, content, "x := 1", "x := 9", false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "appears multiple times")
		require.Contains(t, err.Error(), "line 1")
		require.Contains(t, err.Error(), "line 3")
		require.Contains(t, err.Error(), "line 5")
	})

	t.Run("visualize whitespace", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "····code", visualizeWS("    code"))
		require.Equal(t, "→code", visualizeWS("\tcode"))
		require.Equal(t, "→→code", visualizeWS("\t\tcode"))
		require.Equal(t, "code  more", visualizeWS("code  more"))
	})

	t.Run("spaces vs tabs multiline", func(t *testing.T) {
		t.Parallel()
		content := "class Foo:\n\tdef bar(self):\n\t\treturn 42\n"
		old := "class Foo:\n    def bar(self):\n        return 42"
		hint := diagnoseMismatch(content, old)
		require.NotEmpty(t, hint)
		require.Contains(t, hint, "whitespace-normalized match")
		require.Contains(t, hint, "→")
	})

	t.Run("extra trailing space", func(t *testing.T) {
		t.Parallel()
		content := "func main() {\n\tfmt.Println(\"hi\")\n}\n"
		old := "func main() {\n\tfmt.Println(\"hi\") \n}"
		hint := diagnoseMismatch(content, old)
		require.NotEmpty(t, hint)
		require.Contains(t, hint, "whitespace-normalized match")
	})

	t.Run("empty old string", func(t *testing.T) {
		t.Parallel()
		hint := diagnoseMismatch("some content", "")
		require.Empty(t, hint)
	})

	t.Run("normalizeWS", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "a b c", normalizeWS("a  b\t\tc"))
		require.Equal(t, "hello", normalizeWS("  hello  "))
		require.Equal(t, "", normalizeWS("   "))
	})
}

func TestFindAndReplaceWithDiagnostics(t *testing.T) {
	t.Parallel()

	t.Run("not found includes hint", func(t *testing.T) {
		t.Parallel()
		content := "package main\n\nfunc main() {}\n"
		old := "this does not exist"
		_, _, err := findAndReplace(nil, content, old, "new", false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "old_string not found")
	})

	t.Run("not found replaceAll includes hint", func(t *testing.T) {
		t.Parallel()
		content := "package main\n\nfunc main() {}\n"
		old := "this does not exist"
		_, _, err := findAndReplace(nil, content, old, "new", true)
		require.Error(t, err)
		require.Contains(t, err.Error(), "old_string not found")
	})

	t.Run("exact match still works", func(t *testing.T) {
		t.Parallel()
		content := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
		old := "func main() {\n\tfmt.Println(\"hello\")\n}"
		result, corrected, err := findAndReplace(nil, content, old, "replaced", false)
		require.NoError(t, err)
		require.False(t, corrected)
		require.Equal(t, "replaced\n", result)
	})

	t.Run("fuzzy whitespace match succeeds", func(t *testing.T) {
		t.Parallel()
		content := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
		old := "func main() {\n    fmt.Println(\"hello\")\n}"
		result, corrected, err := findAndReplace(nil, content, old, "func main() {\n    fmt.Println(\"goodbye\")\n}", false)
		require.NoError(t, err)
		require.True(t, corrected)
		require.Equal(t, "func main() {\n\tfmt.Println(\"goodbye\")\n}\n", result)
	})

	t.Run("no hint for totally wrong text", func(t *testing.T) {
		t.Parallel()
		content := "package main\n"
		old := "zzzzz nothing like this"
		_, _, err := findAndReplace(nil, content, old, "x", false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "old_string not found")
		require.NotContains(t, err.Error(), "whitespace-normalized")
		require.NotContains(t, err.Error(), "Closest match")
	})
}

func TestWithWhitespaceNote(t *testing.T) {
	t.Parallel()
	require.Equal(t, "done", withWhitespaceNote("done", false))
	require.Contains(t, withWhitespaceNote("done", true), whitespaceCorrectedNote)
}

func TestApplyEditToContentReportsWhitespaceCorrection(t *testing.T) {
	t.Parallel()

	content := "func main() {\n\tfoo()\n}\n"
	result, corrected, err := applyEditToContent(nil, content, EditOperation{
		OldString: "    foo()",
		NewString: "    bar()",
	})
	require.NoError(t, err)
	require.True(t, corrected)
	require.Equal(t, "func main() {\n\tbar()\n}\n", result)

	result, corrected, err = applyEditToContent(nil, content, EditOperation{
		OldString: "\tfoo()",
		NewString: "\tbar()",
	})
	require.NoError(t, err)
	require.False(t, corrected)
	require.Equal(t, "func main() {\n\tbar()\n}\n", result)
}

// benchmarkSource returns a Go-like file of n distinct lines for the edit
// benchmarks.
func benchmarkSource(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "\tvalue%d := compute(%d, \"item-%d\") // step %d\n", i, i*7, i%13, i%101)
	}
	return b.String()
}

// bigramSetReference, bigramSimilarityReference, lineSimilarityReference
// and bestSimilarLineReference are the original map-of-strings similarity
// search, kept as the specification the packed-bigram version must match
// score for score.
func bigramSetReference(r []rune) map[string]struct{} {
	set := make(map[string]struct{}, len(r))
	for i := 0; i+1 < len(r); i++ {
		set[string(r[i:i+2])] = struct{}{}
	}
	return set
}

func bigramSimilarityReference(line string, want map[string]struct{}) float64 {
	r := []rune(line)
	if len(r) < 2 || len(want) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(r))
	matched := 0
	for i := 0; i+1 < len(r); i++ {
		gram := string(r[i : i+2])
		if _, dup := seen[gram]; dup {
			continue
		}
		seen[gram] = struct{}{}
		if _, ok := want[gram]; ok {
			matched++
		}
	}
	return 2 * float64(matched) / float64(len(want)+len(r)-1)
}

func lineSimilarityReference(a, b string) float64 {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return 1
	}
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	return bigramSimilarityReference(b, bigramSetReference([]rune(a)))
}

func bestSimilarLineReference(contentLines []string, pattern string) int {
	want := bigramSetReference([]rune(pattern))
	best, bestSim := -1, 0.0
	for i, line := range contentLines {
		sim := bigramSimilarityReference(strings.TrimSpace(line), want)
		if sim > bestSim {
			best, bestSim = i, sim
		}
	}
	return best
}

// similarityCorpus returns random short lines over an alphabet mixing
// ASCII, whitespace, multi-byte and astral runes, a combining mark, invalid
// UTF-8 and U+FFFD itself, so repeated and colliding bigrams are common.
func similarityCorpus(rng *rand.Rand, n int) []string {
	alphabet := []string{"a", "b", "c", "ab", " ", "\t", "é", "é", "世", "界", "🙂", "\xff", "\xe4\xb8", "�", "(", ")", "x := 1"}
	lines := make([]string, n)
	for i := range lines {
		var b strings.Builder
		for range rng.IntN(12) {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		lines[i] = b.String()
	}
	return lines
}

func TestLineSimilarityMatchesReference(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	lines := append(similarityCorpus(rng, 400), "", "a", "ab", "aa", "aaaa", "abab", "\xff\xff\xff", "��")
	for _, a := range lines {
		for _, b := range lines[:120] {
			require.Equal(t, lineSimilarityReference(a, b), lineSimilarity(a, b), "a=%q b=%q", a, b)
		}
	}
}

func TestBestSimilarLinesMatchesReference(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(3, 4))
	for range 300 {
		content := similarityCorpus(rng, rng.IntN(40))
		patterns := similarityCorpus(rng, 1+rng.IntN(3))
		// A pattern copied from the file, possibly padded, exercises ties
		// and perfect scores.
		if len(content) > 0 {
			patterns = append(patterns, "  "+content[rng.IntN(len(content))])
		}
		want := make([]int, len(patterns))
		for p, pattern := range patterns {
			want[p] = bestSimilarLineReference(content, pattern)
		}
		require.Equal(t, want, bestSimilarLines(content, patterns), "content=%q patterns=%q", content, patterns)
	}
}

// BenchmarkEditNotFound times an edit whose old_string matches nothing, not
// even after whitespace normalization, so the error carries the
// similarity-anchored closest-match hint.
func BenchmarkEditNotFound(b *testing.B) {
	old := "\tresult := computeAll(x, \"thing\")\n\tif result == nil {\n\t\treturn errMissing\n\t}"
	for _, size := range []int{5_000, 50_000} {
		b.Run(fmt.Sprintf("lines=%d", size), func(b *testing.B) {
			content := benchmarkSource(size)
			b.ReportAllocs()
			for b.Loop() {
				_, _, err := findAndReplace(nil, content, old, "replacement", false)
				require.Error(b, err)
			}
		})
	}
}

// benchmarkFileEdit times one edit tool call on a file of benchmarkSource
// lines. Each iteration restores the file and the session's evidence for
// all of it outside the timer, so every call edits the same content.
func benchmarkFileEdit(b *testing.B, lines int, edit EditOperation) {
	dir := b.TempDir()
	content := benchmarkSource(lines)
	path := writeViewFixture(b, dir, "file.go", content)
	ctx := context.WithValue(b.Context(), SessionIDContextKey, "s")
	tracker := filetracker.NewService(nil)
	tool := NewEditTool(nil, &mockHistoryService{}, tracker, dir)
	params := EditParams{FilePath: path, Edits: []EditOperation{edit}}
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		require.NoError(b, os.WriteFile(path, []byte(content), 0o644))
		filetracker.Observe(ctx, tracker, "s", path, []byte(content), []filetracker.Range{{Start: 0, End: len(content)}})
		b.StartTimer()
		resp := runFileTool(b, tool, ctx, params)
		require.False(b, resp.IsError, resp.Content)
	}
}

// BenchmarkEditWhitespaceTolerant times an edit whose old_string only
// matches after whitespace normalization (spaces where the file has a
// tab), next to the same edit matching exactly.
func BenchmarkEditWhitespaceTolerant(b *testing.B) {
	for _, size := range []int{5_000, 50_000} {
		line := size / 2
		target := fmt.Sprintf("value%d := compute(%d, \"item-%d\") // step %d", line, line*7, line%13, line%101)
		b.Run(fmt.Sprintf("lines=%d/exact", size), func(b *testing.B) {
			benchmarkFileEdit(b, size, EditOperation{OldString: "\t" + target, NewString: "\tchanged := 1"})
		})
		b.Run(fmt.Sprintf("lines=%d/whitespace", size), func(b *testing.B) {
			benchmarkFileEdit(b, size, EditOperation{OldString: "    " + target, NewString: "    changed := 1"})
		})
	}
}

// diagnoseMismatch and normalizedReplace run the hint and the
// whitespace-tolerant replacement on plain strings, the way the tests call
// them.
func diagnoseMismatch(content, old string) string {
	nc := newNormalizedContent(content)
	return mismatchHint(nc, nc.matches(old), old)
}

func normalizedReplace(content, old, new string, replaceAll bool) (string, bool) {
	nc := newNormalizedContent(content)
	result, ok := replaceNormalized(nc, nc.matches(old), old, new, replaceAll)
	if !ok {
		return "", false
	}
	return result.content, true
}

// lineAtOffsetReference and findNormalizedMatchesReference are the
// original line lookup and match search, which normalized the whole
// content on every call and walked its lines from the top per match. They
// are kept as the specification normalizedContent must match.
func lineAtOffsetReference(lines []string, offset int) int {
	pos := 0
	for i, line := range lines {
		if pos+len(line) >= offset {
			return i
		}
		pos += len(line) + 1
	}
	return len(lines) - 1
}

func findNormalizedMatchesReference(content, old string) []normMatch {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(old, "\n")
	normOld := joinNormalized(oldLines)
	if strings.TrimSpace(normOld) == "" {
		return nil
	}
	normContentLines := make([]string, len(contentLines))
	for i, l := range contentLines {
		normContentLines[i] = normalizeWS(l)
	}
	normContent := strings.Join(normContentLines, "\n")
	var matches []normMatch
	searchFrom := 0
	for searchFrom <= len(normContent) {
		idx := strings.Index(normContent[searchFrom:], normOld)
		if idx == -1 {
			break
		}
		absIdx := searchFrom + idx
		end := absIdx + len(normOld)
		atLineStart := absIdx == 0 || normContent[absIdx-1] == '\n'
		atLineEnd := end == len(normContent) || normContent[end] == '\n'
		if !atLineStart || !atLineEnd {
			searchFrom = absIdx + 1
			continue
		}
		startLine := lineAtOffsetReference(normContentLines, absIdx)
		endLine := startLine + len(oldLines) - 1
		if endLine >= len(contentLines) {
			break
		}
		matches = append(matches, normMatch{startLine, endLine})
		searchFrom = end + 1
	}
	return matches
}

func TestLineAtOffset(t *testing.T) {
	t.Parallel()
	nc := newNormalizedContent("aaa\nbb\nccccc")
	require.Equal(t, 0, nc.lineAtOffset(0))
	require.Equal(t, 0, nc.lineAtOffset(2))
	require.Equal(t, 1, nc.lineAtOffset(4))
	require.Equal(t, 2, nc.lineAtOffset(7))
	require.Equal(t, 2, nc.lineAtOffset(100))
}

// whitespaceCorpus returns random multi-line texts built from fragments
// heavy in whitespace, including Unicode spaces strings.Fields knows
// (U+0085, U+00A0, U+3000) and invalid UTF-8, so the ASCII fast path and
// the Unicode fallback both get exercised.
func whitespaceCorpus(rng *rand.Rand, n int) []string {
	fragments := []string{"a", "bc", "x := 1", " ", "  ", "\t", "\t\t", "\v", "\f", "\r", "\n", "\n", "\u0085", " ", "　", "é", "\xff", "{", "}"}
	texts := make([]string, n)
	for i := range texts {
		var b strings.Builder
		for range rng.IntN(24) {
			b.WriteString(fragments[rng.IntN(len(fragments))])
		}
		texts[i] = b.String()
	}
	return texts
}

func TestNormalizedContentMatchesReference(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(5, 6))
	for _, content := range whitespaceCorpus(rng, 2000) {
		nc := newNormalizedContent(content)
		lines := strings.Split(content, "\n")
		require.Equal(t, lines, nc.lines)
		require.Equal(t, normalizeText(content), nc.norm, "content %q", content)
		normLines := make([]string, len(lines))
		for i := range lines {
			normLines[i] = normalizeWS(lines[i])
			require.Equal(t, normLines[i], nc.normLine(i))
			for j := i; j < len(lines); j++ {
				require.Equal(t, lineRange([]byte(content), i, j-i+1), nc.lineRange(i, j), "content %q lines %d-%d", content, i, j)
			}
		}
		for offset := 0; offset <= len(nc.norm)+1; offset++ {
			require.Equal(t, lineAtOffsetReference(normLines, offset), nc.lineAtOffset(offset))
		}
		assembled := assembleNormalized(lines, normLines)
		require.Equal(t, content, assembled.content)
		require.Equal(t, nc.norm, assembled.norm)
		require.Equal(t, nc.normStarts, assembled.normStarts)
		require.Equal(t, nc.rawStarts, assembled.rawStarts)
	}
}

func TestNormalizedMatchesAndReplaceMatchReference(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(7, 8))
	contents := whitespaceCorpus(rng, 300)
	olds := append(whitespaceCorpus(rng, 40), "a", "bc", "x := 1", "{\na\n}", " a ", "\ta\n\tbc")
	for _, content := range contents {
		nc := newNormalizedContent(content)
		for _, old := range olds {
			matches := nc.matches(old)
			require.Equal(t, findNormalizedMatchesReference(content, old), matches, "content %q old %q", content, old)
			for _, replaceAll := range []bool{false, true} {
				result, ok := replaceNormalized(nc, matches, old, "  y\n\tz", replaceAll)
				if !ok {
					continue
				}
				// The normalized form a replacement leaves behind is what
				// verification and the next edit use in place of
				// normalizing the result again.
				require.Equal(t, newNormalizedContent(result.content), result)
			}
		}
	}
}

func TestWhitespaceTolerantEditsShareNormalization(t *testing.T) {
	t.Parallel()
	content := "func a() {\n\tx := 1\n}\n\nfunc b() {\n\ty := 2\n}\n"
	norm := &normCache{}
	result, failed, corrected, err := applyEditsToContent(norm, content, []EditOperation{
		{OldString: "    x := 1", NewString: "    x := 10"},
		{OldString: "    y := 2", NewString: "    y := 20"},
	}, 0)
	require.NoError(t, err)
	require.Empty(t, failed)
	require.True(t, corrected)
	require.Equal(t, "func a() {\n\tx := 10\n}\n\nfunc b() {\n\ty := 20\n}\n", result)
	require.Equal(t, newNormalizedContent(result), norm.nc, "the cache holds the normalized final content")
}

func TestVisualizeWSPreservesInterior(t *testing.T) {
	t.Parallel()
	s := visualizeWS("\t\tif x > 0 {")
	require.True(t, strings.HasPrefix(s, "→→"))
	require.Contains(t, s, "if x > 0 {")
}

func TestNormalizedReplace(t *testing.T) {
	t.Parallel()

	t.Run("tabs to spaces conversion", func(t *testing.T) {
		t.Parallel()
		content := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
		old := "func main() {\n    fmt.Println(\"hello\")\n}"
		new := "func main() {\n    fmt.Println(\"goodbye\")\n}"
		result, ok := normalizedReplace(content, old, new, false)
		require.True(t, ok)
		require.Equal(t, "func main() {\n\tfmt.Println(\"goodbye\")\n}\n", result)
	})

	t.Run("spaces to tabs conversion", func(t *testing.T) {
		t.Parallel()
		content := "func main() {\n    fmt.Println(\"hello\")\n}\n"
		old := "func main() {\n\tfmt.Println(\"hello\")\n}"
		new := "func main() {\n\tfmt.Println(\"goodbye\")\n}"
		result, ok := normalizedReplace(content, old, new, false)
		require.True(t, ok)
		require.Equal(t, "func main() {\n    fmt.Println(\"goodbye\")\n}\n", result)
	})

	t.Run("ambiguous match fails", func(t *testing.T) {
		t.Parallel()
		content := "func a() {\n\tfoo()\n}\nfunc b() {\n\tfoo()\n}\n"
		old := "func x() {\n    foo()\n}"
		new := "func x() {\n    bar()\n}"
		_, ok := normalizedReplace(content, old, new, false)
		require.False(t, ok)
	})

	t.Run("replaceAll with multiple matches", func(t *testing.T) {
		t.Parallel()
		content := "func a() {\n\tfoo()\n}\nfunc b() {\n\tfoo()\n}\n"
		old := "    foo()"
		new := "    bar()"
		result, ok := normalizedReplace(content, old, new, true)
		require.True(t, ok)
		require.Equal(t, "func a() {\n\tbar()\n}\nfunc b() {\n\tbar()\n}\n", result)
	})

	t.Run("partial line match rejected", func(t *testing.T) {
		t.Parallel()
		// The pattern starts mid-line, so replacing at line granularity would
		// discard "func a() " and "func b() ".
		content := "func a() {\n\tfoo()\n}\nfunc b() {\n\tfoo()\n}\n"
		old := "{\n    foo()\n}"
		new := "{\n    bar()\n}"
		_, ok := normalizedReplace(content, old, new, true)
		require.False(t, ok)
	})

	t.Run("surrounding text on the line is preserved", func(t *testing.T) {
		t.Parallel()
		content := "before foo = 1 after\n"
		_, ok := normalizedReplace(content, "foo  =  1", "foo  =  2", false)
		require.False(t, ok)
	})

	t.Run("repeated matches on one line rejected", func(t *testing.T) {
		t.Parallel()
		content := "a  b   c   a  b\n"
		_, ok := normalizedReplace(content, "a b", "Z", true)
		require.False(t, ok)
	})

	t.Run("no match returns false", func(t *testing.T) {
		t.Parallel()
		content := "package main\n"
		old := "does not exist"
		new := "replacement"
		_, ok := normalizedReplace(content, old, new, false)
		require.False(t, ok)
	})

	t.Run("same indent unit but wrong depth", func(t *testing.T) {
		t.Parallel()
		content := "func main() {\n\tif ok {\n\t\told()\n\t}\n}\n"
		old := "if ok {\n\told()\n}"
		new := "if ok {\n\tnew()\n}"
		result, ok := normalizedReplace(content, old, new, false)
		require.True(t, ok)
		require.Equal(t, "func main() {\n\tif ok {\n\t\tnew()\n\t}\n}\n", result)
	})

	t.Run("unindented old string in indented context", func(t *testing.T) {
		t.Parallel()
		content := "func f() {\n\tfoo := 1\n\tbar := 2\n}\n"
		result, ok := normalizedReplace(content, "foo :=  1", "foo := 99", false)
		require.True(t, ok)
		require.Equal(t, "func f() {\n\tfoo := 99\n\tbar := 2\n}\n", result)
	})

	t.Run("deep indentation preserved", func(t *testing.T) {
		t.Parallel()
		content := "func main() {\n\tif x {\n\t\tif y {\n\t\t\tfoo()\n\t\t}\n\t}\n}\n"
		old := "if x {\n    if y {\n        foo()\n    }\n}"
		new := "if x {\n    if y {\n        bar()\n    }\n}"
		result, ok := normalizedReplace(content, old, new, false)
		require.True(t, ok)
		require.Contains(t, result, "\t\t\tbar()")
	})
}

func TestAdaptIndentation(t *testing.T) {
	t.Parallel()

	t.Run("spaces to tabs", func(t *testing.T) {
		t.Parallel()
		actual := "func main() {\n\tfmt.Println(\"hello\")\n}"
		old := "func main() {\n    fmt.Println(\"hello\")\n}"
		new := "func main() {\n    fmt.Println(\"goodbye\")\n}"
		result := adaptIndentation(actual, old, new, "\t")
		require.Equal(t, "func main() {\n\tfmt.Println(\"goodbye\")\n}", result)
	})

	t.Run("tabs to spaces", func(t *testing.T) {
		t.Parallel()
		actual := "func main() {\n    fmt.Println(\"hello\")\n}"
		old := "func main() {\n\tfmt.Println(\"hello\")\n}"
		new := "func main() {\n\tfmt.Println(\"goodbye\")\n}"
		result := adaptIndentation(actual, old, new, "    ")
		require.Equal(t, "func main() {\n    fmt.Println(\"goodbye\")\n}", result)
	})

	t.Run("same style unchanged", func(t *testing.T) {
		t.Parallel()
		actual := "func main() {\n\tfmt.Println(\"hello\")\n}"
		old := "func main() {\n\tfmt.Println(\"hello\")\n}"
		new := "func main() {\n\tfmt.Println(\"goodbye\")\n}"
		result := adaptIndentation(actual, old, new, "\t")
		require.Equal(t, new, result)
	})

	t.Run("same style shifted deeper", func(t *testing.T) {
		t.Parallel()
		actual := "\tif ok {\n\t\told()\n\t}"
		old := "if ok {\n\told()\n}"
		new := "if ok {\n\tnew()\n}"
		result := adaptIndentation(actual, old, new, "\t")
		require.Equal(t, "\tif ok {\n\t\tnew()\n\t}", result)
	})

	t.Run("same style shifted shallower", func(t *testing.T) {
		t.Parallel()
		actual := "\tif ok {\n\t\told()\n\t}"
		old := "\t\tif ok {\n\t\t\told()\n\t\t}"
		new := "\t\tif ok {\n\t\t\tnew()\n\t\t}"
		result := adaptIndentation(actual, old, new, "\t")
		require.Equal(t, "\tif ok {\n\t\tnew()\n\t}", result)
	})

	t.Run("unindented old string uses new string style", func(t *testing.T) {
		t.Parallel()
		actual := "\tif ok {\n\t\told()\n\t}"
		old := "if ok { old() }"
		new := "if ok {\n    new()\n}"
		result := adaptIndentation(actual, old, new, "\t")
		require.Equal(t, "\tif ok {\n\t\tnew()\n\t}", result)
	})

	t.Run("2-space to 4-space", func(t *testing.T) {
		t.Parallel()
		actual := "func main() {\n    fmt.Println(\"hello\")\n}"
		old := "func main() {\n  fmt.Println(\"hello\")\n}"
		new := "func main() {\n  fmt.Println(\"goodbye\")\n}"
		result := adaptIndentation(actual, old, new, "    ")
		require.Equal(t, "func main() {\n    fmt.Println(\"goodbye\")\n}", result)
	})
}

func TestDetectIndentUnit(t *testing.T) {
	t.Parallel()

	t.Run("tabs", func(t *testing.T) {
		t.Parallel()
		lines := []string{"func main() {", "\tfmt.Println()", "}"}
		require.Equal(t, "\t", detectIndentUnit(lines))
	})

	t.Run("4 spaces", func(t *testing.T) {
		t.Parallel()
		lines := []string{"func main() {", "    fmt.Println()", "}"}
		require.Equal(t, "    ", detectIndentUnit(lines))
	})

	t.Run("2 spaces", func(t *testing.T) {
		t.Parallel()
		lines := []string{"func main() {", "  fmt.Println()", "}"}
		require.Equal(t, "  ", detectIndentUnit(lines))
	})

	t.Run("mixed uses minimum", func(t *testing.T) {
		t.Parallel()
		lines := []string{"func main() {", "  x()", "    y()", "}"}
		require.Equal(t, "  ", detectIndentUnit(lines))
	})

	t.Run("no indentation", func(t *testing.T) {
		t.Parallel()
		lines := []string{"package main", "func main() {}"}
		require.Equal(t, "", detectIndentUnit(lines))
	})
}

func TestMeasureDepth(t *testing.T) {
	t.Parallel()

	require.Equal(t, 2, measureDepth("\t\t", "\t"))
	require.Equal(t, 3, measureDepth("\t\t\t", "\t"))
	require.Equal(t, 2, measureDepth("    ", "  "))
	require.Equal(t, 1, measureDepth("    ", "    "))
	require.Equal(t, 0, measureDepth("", "  "))
	require.Equal(t, 0, measureDepth("  ", ""))
}
