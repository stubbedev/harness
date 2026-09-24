package tools

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/stringext"
)

// whitespaceCorrectedNote tells the caller that old_string did not match
// byte-for-byte and that the edit was applied to whitespace-equivalent text
// found in the file, so it can verify the outcome.
const whitespaceCorrectedNote = "Note: old_string did not match exactly. The edit was applied to whitespace-equivalent text in the file and new_string was re-indented to match the file's style. Verify the result."

const (
	// nearLineThreshold is the per-line similarity that marks a line as a
	// near miss (~) instead of a difference (×) in the mismatch hint.
	nearLineThreshold = 0.75
	// fuzzyMatchThreshold is the average per-line similarity a window must
	// reach before it is reported as the closest match.
	fuzzyMatchThreshold = 0.5
)

// normMatch is a line range in the original (un-normalized) content that
// matches the search pattern after whitespace normalization.
type normMatch struct{ startLine, endLine int }

// normalizedContent is content split into lines next to the
// whitespace-normalized form of every line. An edit that falls back to
// whitespace-tolerant matching needs that form for the evidence check, the
// match search, the replacement, its verification and the mismatch hint;
// building it once per content, instead of once per step, is what keeps the
// fallback from normalizing a large file three or four times.
type normalizedContent struct {
	content string
	// lines is content split on "\n", and rawStarts the byte offset of each
	// line in content.
	lines     []string
	rawStarts []int
	// norm is the normalized lines joined with "\n", exactly as
	// joinNormalized(lines) would build it, and normStarts the offset of
	// each normalized line in norm.
	norm       string
	normStarts []int
}

// newNormalizedContent normalizes content line by line into one string.
func newNormalizedContent(content string) *normalizedContent {
	lines := strings.Split(content, "\n")
	nc := &normalizedContent{
		content:    content,
		lines:      lines,
		rawStarts:  make([]int, len(lines)),
		normStarts: make([]int, len(lines)),
	}
	var b strings.Builder
	b.Grow(len(content))
	raw := 0
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		nc.rawStarts[i], nc.normStarts[i] = raw, b.Len()
		writeNormalizedWS(&b, line)
		raw += len(line) + 1
	}
	nc.norm = b.String()
	return nc
}

// assembleNormalized builds the normalizedContent of lines joined with
// "\n", given each line's already normalized form, so a replacement can
// reuse the normalized text of every line it did not touch.
func assembleNormalized(lines, normLines []string) *normalizedContent {
	content := strings.Join(lines, "\n")
	nc := &normalizedContent{
		content:    content,
		lines:      lines,
		rawStarts:  make([]int, len(lines)),
		normStarts: make([]int, len(lines)),
	}
	var b strings.Builder
	b.Grow(len(content))
	raw := 0
	for i, norm := range normLines {
		if i > 0 {
			b.WriteByte('\n')
		}
		nc.rawStarts[i], nc.normStarts[i] = raw, b.Len()
		b.WriteString(norm)
		raw += len(lines[i]) + 1
	}
	nc.norm = b.String()
	return nc
}

// normLine returns line i of the normalized content.
func (nc *normalizedContent) normLine(i int) string {
	return nc.norm[nc.normStarts[i]:nc.normEnd(i)]
}

// normEnd returns the offset in norm just past normalized line i, where
// its "\n" separator (if any) sits.
func (nc *normalizedContent) normEnd(i int) int {
	if i+1 < len(nc.normStarts) {
		return nc.normStarts[i+1] - 1
	}
	return len(nc.norm)
}

// lineAtOffset returns the index of the first normalized line that ends
// at or after offset in norm, or the last line when none does.
func (nc *normalizedContent) lineAtOffset(offset int) int {
	i := sort.Search(len(nc.normStarts), func(i int) bool { return nc.normEnd(i) >= offset })
	return min(i, len(nc.normStarts)-1)
}

// lineRange returns the byte range of lines startLine through endLine of
// content, including the newline that ends endLine when there is one. It
// is lineRange over content, answered from the line offset table.
func (nc *normalizedContent) lineRange(startLine, endLine int) filetracker.Range {
	end := len(nc.content)
	if endLine+1 < len(nc.rawStarts) {
		end = nc.rawStarts[endLine+1]
	}
	return filetracker.Range{Start: nc.rawStarts[startLine], End: end}
}

// matches searches the content for old after collapsing each line's
// whitespace runs to single spaces. It returns the line ranges of all
// non-overlapping matches, mapped back to the original content's line numbers.
// Only matches that span whole lines are reported: replacements happen at line
// granularity, so accepting a partial-line match would discard the rest of the
// line.
func (nc *normalizedContent) matches(old string) []normMatch {
	oldLines := strings.Split(old, "\n")

	normOld := joinNormalized(oldLines)
	if strings.TrimSpace(normOld) == "" {
		return nil
	}

	normContent := nc.norm
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
		startLine := nc.lineAtOffset(absIdx)
		endLine := startLine + len(oldLines) - 1
		if endLine >= len(nc.lines) {
			break
		}
		matches = append(matches, normMatch{startLine, endLine})
		// Whole-line matches consume every line they cover, so resuming after
		// the match keeps the reported ranges disjoint.
		searchFrom = end + 1
	}
	return matches
}

// normCache holds the normalizedContent of the content an edit call last
// needed it for. The evidence check and the first edit search the same
// content, and a whitespace-tolerant replacement leaves the normalized form
// of its result behind for the verification and the next edit, so each
// content is normalized at most once. A nil cache normalizes every time.
type normCache struct{ nc *normalizedContent }

// of returns the normalizedContent of content, reusing the cached one when
// it was built for the same text.
func (c *normCache) of(content string) *normalizedContent {
	if c == nil {
		return newNormalizedContent(content)
	}
	if c.nc == nil || c.nc.content != content {
		c.nc = newNormalizedContent(content)
	}
	return c.nc
}

// remember caches nc as the normalized form of nc.content.
func (c *normCache) remember(nc *normalizedContent) {
	if c != nil {
		c.nc = nc
	}
}

// replaceNormalized performs a whitespace-normalized find-and-replace, given
// the matches of old in nc, when an exact match fails. If a unique match is
// found (or replaceAll is set), it extracts the actual text from the file,
// adapts new's indentation to match the file's style, and performs the
// replacement. It returns the normalizedContent of the result and true on
// success, or (nil, false) if no safe match was found.
func replaceNormalized(nc *normalizedContent, matches []normMatch, old, new string, replaceAll bool) (*normalizedContent, bool) {
	if len(matches) == 0 {
		return nil, false
	}
	if !replaceAll && len(matches) > 1 {
		return nil, false // Ambiguous; let the model disambiguate.
	}

	contentLines := nc.lines
	fileUnit := detectIndentUnit(contentLines)

	// Replace in reverse order to preserve line indices. normResult tracks
	// the normalized form of every result line alongside it, so the lines
	// the replacement leaves alone are never normalized again.
	result := slices.Clone(contentLines)
	normResult := make([]string, len(contentLines))
	for i := range normResult {
		normResult[i] = nc.normLine(i)
	}
	for _, m := range slices.Backward(matches) {
		actual := strings.Join(contentLines[m.startLine:m.endLine+1], "\n")
		adapted := adaptIndentation(actual, old, new, fileUnit)
		adaptedLines := strings.Split(adapted, "\n")
		normAdapted := make([]string, len(adaptedLines))
		for i, l := range adaptedLines {
			normAdapted[i] = normalizeWS(l)
		}
		result = slices.Concat(result[:m.startLine], adaptedLines, result[m.endLine+1:])
		normResult = slices.Concat(normResult[:m.startLine], normAdapted, normResult[m.endLine+1:])
	}

	return assembleNormalized(result, normResult), true
}

// joinNormalized normalizes each line and joins with newlines.
func joinNormalized(lines []string) string {
	norm := make([]string, len(lines))
	for i, l := range lines {
		norm[i] = normalizeWS(l)
	}
	return strings.Join(norm, "\n")
}

// adaptIndentation rewrites newStr's leading whitespace to match the file's
// indentation style (fileUnit, detected from the whole file). It converts
// newStr's indentation from the style the caller wrote it in to the file's
// style, applying a depth offset so newStr lands at the same nesting level as
// the actual matched text from the file. The offset is applied even when both
// styles agree, because oldStr's nesting level may still differ from the
// matched text's.
func adaptIndentation(actualStr, oldStr, newStr, fileUnit string) string {
	if fileUnit == "" {
		return newStr
	}

	actualLines := strings.Split(actualStr, "\n")
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	// The unit the caller indented with. oldStr is the best signal, but it may
	// have no indentation at all, in which case newStr's own indentation (and
	// finally the file's) is the next best guess.
	sourceUnit := detectIndentUnit(oldLines)
	if sourceUnit == "" {
		sourceUnit = detectIndentUnit(newLines)
	}
	if sourceUnit == "" {
		sourceUnit = fileUnit
	}

	// Compute the base indent depth of the actual match and the old string
	// (from their first non-empty lines). The difference is applied as an
	// offset so that newStr lands at the correct nesting level.
	baseDepth := firstIndentDepth(actualLines, fileUnit)
	oldBaseDepth := firstIndentDepth(oldLines, sourceUnit)
	depthOffset := baseDepth - oldBaseDepth
	if sourceUnit == fileUnit && depthOffset == 0 {
		return newStr
	}

	result := make([]string, len(newLines))
	for i, line := range newLines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			result[i] = line
			continue
		}
		leading := line[:len(line)-len(trimmed)]
		depth := max(measureDepth(leading, sourceUnit)+depthOffset, 0)
		result[i] = strings.Repeat(fileUnit, depth) + trimmed
	}
	return strings.Join(result, "\n")
}

// firstIndentDepth returns the indent depth of the first non-empty line.
func firstIndentDepth(lines []string, unit string) int {
	for _, l := range lines {
		trimmed := strings.TrimLeft(l, " \t")
		if trimmed == "" {
			continue
		}
		leading := l[:len(l)-len(trimmed)]
		return measureDepth(leading, unit)
	}
	return 0
}

// detectIndentUnit returns the indentation unit used by the given lines:
// "\t" for tab-indented files, or a string of N spaces for space-indented
// files. Returns "" if indentation cannot be determined.
func detectIndentUnit(lines []string) string {
	minSpaces := 0
	hasTabs := false
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		leading := line[:len(line)-len(trimmed)]
		if leading == "" {
			continue
		}
		if strings.Contains(leading, "\t") {
			hasTabs = true
			break
		}
		n := len(leading)
		if n > 0 && (minSpaces == 0 || n < minSpaces) {
			minSpaces = n
		}
	}
	if hasTabs {
		return "\t"
	}
	if minSpaces > 0 {
		return strings.Repeat(" ", minSpaces)
	}
	return ""
}

// measureDepth returns how many indent units deep the leading whitespace
// represents.
func measureDepth(leading, unit string) int {
	if unit == "" {
		return 0
	}
	if unit == "\t" {
		return strings.Count(leading, "\t")
	}
	spaces := strings.Count(leading, " ")
	return spaces / len(unit)
}

// mismatchHint produces a diagnostic hint when old_string is not found in
// the content and normalized matching also failed; matches are the
// whitespace-normalized matches of old in nc. It helps models self-correct
// by showing what the file actually contains near the best match.
func mismatchHint(nc *normalizedContent, matches []normMatch, old string) string {
	oldLines := strings.Split(old, "\n")

	if len(oldLines) == 0 {
		return ""
	}

	// Strategy 1: whitespace-normalized search. Matches that exist but were
	// not used are ambiguous, so the hint reports the actual lines of the
	// first.
	if len(matches) > 0 {
		return formatWhitespaceHint(nc.lines, matches[0].startLine, matches[0].endLine)
	}

	// Strategy 2: line-similarity search.
	if hint := diagnoseBestLineMatch(nc.lines, oldLines); hint != "" {
		return hint
	}

	return ""
}

// normalizeWS collapses all whitespace runs to a single space.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// writeNormalizedWS writes normalizeWS(s) to b. ASCII lines, nearly every
// line of source code, are normalized in place without splitting them into
// fields; strings.Fields treats exactly these six bytes as ASCII
// whitespace, so the output is the same. Other lines take normalizeWS,
// which knows the Unicode spaces.
func writeNormalizedWS(b *strings.Builder, s string) {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			b.WriteString(normalizeWS(s))
			return
		}
	}
	isSpace := func(c byte) bool {
		return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
	}
	wrote := false
	for i := 0; i < len(s); {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		start := i
		for i < len(s) && !isSpace(s[i]) {
			i++
		}
		if start == i {
			break
		}
		if wrote {
			b.WriteByte(' ')
		}
		b.WriteString(s[start:i])
		wrote = true
	}
}

func formatWhitespaceHint(contentLines []string, startLine, endLine int) string {
	var b strings.Builder
	b.WriteString("A whitespace-normalized match was found, so the text exists but with different whitespace (tabs vs spaces, or different indentation).\n")
	fmt.Fprintf(&b, "Actual file content (lines %d-%d):\n", startLine+1, endLine+1)
	for i := startLine; i <= endLine && i < len(contentLines); i++ {
		fmt.Fprintf(&b, "%6d|%s\n", i+1, visualizeWS(contentLines[i]))
	}
	b.WriteString("Use the exact whitespace shown above (→ = tab, · = space).")
	return b.String()
}

// diagnoseBestLineMatch finds the window of lines in contentLines that best
// matches the search pattern and reports where the caller's text differs.
// The exact pass scores windows by trimmed-equal lines; when fewer than half
// the lines match, the best window is rescored by line similarity and, if
// still nothing, a similarity-anchored scan searches the whole file.
func diagnoseBestLineMatch(contentLines, oldLines []string) string {
	if len(contentLines) == 0 {
		return ""
	}
	trimmedOld := trimmedSearchLines(oldLines)
	if len(trimmedOld) == 0 {
		return ""
	}

	start, matched := bestExactWindow(contentLines, trimmedOld)
	if start >= 0 && matched >= (len(trimmedOld)+1)/2 {
		return formatLineMatchHint(contentLines, trimmedOld, start, matched, false)
	}
	if start >= 0 && windowSimilarity(contentLines, trimmedOld, start) >= fuzzyMatchThreshold {
		return formatLineMatchHint(contentLines, trimmedOld, start,
			nearMatchedCount(contentLines, trimmedOld, start), true)
	}
	if start, near := bestFuzzyWindow(contentLines, trimmedOld); start >= 0 {
		return formatLineMatchHint(contentLines, trimmedOld, start, near, true)
	}
	return ""
}

// trimmedSearchLines trims each line and strips empty edge lines, which no
// file line should be required to match.
func trimmedSearchLines(oldLines []string) []string {
	trimmed := make([]string, len(oldLines))
	for i, l := range oldLines {
		trimmed[i] = strings.TrimSpace(l)
	}
	for len(trimmed) > 0 && trimmed[0] == "" {
		trimmed = trimmed[1:]
	}
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == "" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

// bestExactWindow returns the window of contentLines most closely matching
// trimmedOld, scored by trimmed-equal lines. Returns -1 when no line matches.
func bestExactWindow(contentLines, trimmedOld []string) (int, int) {
	bestScore := 0
	bestStart := -1
	for start := range len(contentLines) - len(trimmedOld) + 1 {
		score := 0
		for j, want := range trimmedOld {
			if strings.TrimSpace(contentLines[start+j]) == want {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestStart = start
		}
	}
	return bestStart, bestScore
}

// bestFuzzyWindow searches contentLines for the window most similar to the
// search pattern when no line matches exactly. It anchors on representative
// pattern lines, evaluates the aligned window around each anchor's best hit,
// and returns the window with the most closely matching lines.
func bestFuzzyWindow(contentLines, trimmedOld []string) (int, int) {
	if len(contentLines) < len(trimmedOld) {
		return -1, 0
	}
	best, bestNear := -1, 0
	anchors := anchorIndices(trimmedOld)
	patterns := make([]string, len(anchors))
	for i, a := range anchors {
		patterns[i] = trimmedOld[a]
	}
	for i, c := range bestSimilarLines(contentLines, patterns) {
		a := anchors[i]
		if c < 0 {
			continue
		}
		start := min(max(c-a, 0), len(contentLines)-len(trimmedOld))
		near := nearMatchedCount(contentLines, trimmedOld, start)
		if near > bestNear {
			best, bestNear = start, near
		}
	}
	if best < 0 || 2*bestNear < len(trimmedOld) {
		return -1, 0
	}
	return best, bestNear
}

// anchorIndices picks up to three representative lines of the search pattern
// to anchor the fuzzy scan: the first, middle and last non-empty lines.
func anchorIndices(lines []string) []int {
	var anchors []int
	seen := make(map[int]struct{}, 3)
	for _, i := range []int{0, len(lines) / 2, len(lines) - 1} {
		if lines[i] == "" {
			continue
		}
		if _, dup := seen[i]; dup {
			continue
		}
		seen[i] = struct{}{}
		anchors = append(anchors, i)
	}
	return anchors
}

// bestSimilarLines returns, for each pattern, the index of the first content
// line most similar to it, or -1 when no line shares any bigrams with it.
// All patterns are scored in one pass over the file, each line's bigrams
// are computed once for all of them, and a line is skipped outright when
// its length alone rules out beating every pattern's best so far.
func bestSimilarLines(contentLines, patterns []string) []int {
	wants := make([][]uint64, len(patterns))
	best := make([]int, len(patterns))
	bestSim := make([]float64, len(patterns))
	for p, pattern := range patterns {
		wants[p], _ = lineBigrams(nil, pattern)
		best[p] = -1
	}
	var got []uint64
	for i, line := range contentLines {
		line = strings.TrimSpace(line)
		pairs := utf8.RuneCountInString(line) - 1
		if pairs < 1 || !canBeatBest(wants, bestSim, pairs) {
			continue
		}
		got, _ = lineBigrams(got, line)
		for p, want := range wants {
			if sim := bigramDice(want, got, pairs); sim > bestSim[p] {
				best[p], bestSim[p] = i, sim
			}
		}
	}
	return best
}

// canBeatBest reports whether a line with the given number of bigrams could
// score above any pattern's best so far. A line shares at most
// min(len(want), pairs) distinct bigrams with a pattern, and the bound is
// computed with the same denominator bigramDice divides by, so a line that
// fails it can never have scored strictly higher.
func canBeatBest(wants [][]uint64, bestSim []float64, pairs int) bool {
	for p, want := range wants {
		if len(want) == 0 {
			continue
		}
		if 2*float64(min(len(want), pairs))/float64(len(want)+pairs) > bestSim[p] {
			return true
		}
	}
	return false
}

// nearMatchedCount counts the aligned lines of the window at start that
// equal the caller's line or are near misses of it.
func nearMatchedCount(contentLines, trimmedOld []string, start int) int {
	n := 0
	for j, want := range trimmedOld {
		cand := strings.TrimSpace(contentLines[start+j])
		if cand == want || lineSimilarity(cand, want) >= nearLineThreshold {
			n++
		}
	}
	return n
}

// windowSimilarity averages line similarity over the aligned window at
// start. It returns 0 for a window that extends past the end of content.
func windowSimilarity(contentLines, trimmedOld []string, start int) float64 {
	if start < 0 || start+len(trimmedOld) > len(contentLines) {
		return 0
	}
	sum := 0.0
	for j, want := range trimmedOld {
		sum += lineSimilarity(strings.TrimSpace(contentLines[start+j]), want)
	}
	return sum / float64(len(trimmedOld))
}

// lineSimilarity scores two lines' resemblance as a 0..1 value: 1 for equal
// trimmed text, otherwise the character-bigram overlap of the trimmed lines.
// It only needs to rank candidates in the mismatch hint, not measure truth.
func lineSimilarity(a, b string) float64 {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return 1
	}
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	want, _ := lineBigrams(nil, a)
	got, pairs := lineBigrams(nil, b)
	return bigramDice(want, got, pairs)
}

// bigramDice scores a line against a pattern as a Sorensen-Dice
// coefficient: twice the distinct bigrams they share over the pattern's
// distinct bigrams plus the line's bigrams counting repeats. want and got
// are the sorted distinct bigrams of the pattern and the line, and pairs is
// the line's bigram count. Empty or single-rune lines score 0.
func bigramDice(want, got []uint64, pairs int) float64 {
	if pairs < 1 || len(want) == 0 {
		return 0
	}
	matched := 0
	for i, j := 0, 0; i < len(want) && j < len(got); {
		switch {
		case want[i] < got[j]:
			i++
		case want[i] > got[j]:
			j++
		default:
			matched++
			i++
			j++
		}
	}
	return 2 * float64(matched) / float64(len(want)+pairs)
}

// lineBigrams returns the distinct adjacent rune pairs of s, sorted, in
// buf's storage, and the number of pairs s has counting repeats. Each pair
// is packed into one integer, first rune in the high half, so comparing
// pairs costs no string allocation. Decoding yields only valid runes and
// utf8.RuneError, so no two different pairs pack to the same integer.
func lineBigrams(buf []uint64, s string) ([]uint64, int) {
	buf, pairs := buf[:0], 0
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			buf = append(buf, uint64(prev)<<32|uint64(r))
			pairs++
		}
		prev = r
	}
	slices.Sort(buf)
	return slices.Compact(buf), pairs
}

// formatLineMatchHint renders the closest-match hint. Lines of the window
// that differ from the caller's old_string are marked (~ near miss, ×
// different) and the caller's line shown, so the next attempt can copy the
// file's text directly.
func formatLineMatchHint(contentLines, trimmedOld []string, start, matched int, fuzzy bool) string {
	endLine := start + len(trimmedOld) - 1
	ctxStart := max(0, start-1)
	ctxEnd := min(len(contentLines)-1, endLine+1)

	kind := "lines match after trimming whitespace"
	if fuzzy {
		kind = "lines match closely"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "No exact match found. Closest match at lines %d-%d (%d/%d %s):\n",
		start+1, endLine+1, matched, len(trimmedOld), kind)
	for i := ctxStart; i <= ctxEnd; i++ {
		j := i - start
		cand := strings.TrimSpace(contentLines[i])
		if j < 0 || j >= len(trimmedOld) || cand == trimmedOld[j] {
			fmt.Fprintf(&b, "%6d|%s\n", i+1, visualizeWS(contentLines[i]))
			continue
		}
		mark := "×"
		if lineSimilarity(cand, trimmedOld[j]) >= nearLineThreshold {
			mark = "~"
		}
		fmt.Fprintf(&b, "%s %5d|%s\n         your line: %s\n", mark, i+1,
			visualizeWS(contentLines[i]), elideLine(trimmedOld[j], 100))
	}
	if fuzzy {
		b.WriteString("Copy the file's lines above into old_string (→ = tab, · = space); marked lines differ from your old_string. If the text is genuinely absent, re-read the region.")
	} else {
		b.WriteString("Use the exact text shown above (→ = tab, · = space).")
	}
	return b.String()
}

// elideLine shortens a line for display inside the mismatch hint, keeping
// whole runes.
func elideLine(s string, maxRunes int) string {
	return stringext.Truncate(s, maxRunes, "...")
}

// visualizeWS replaces tabs and leading spaces with visible markers so
// models can distinguish them. Interior spaces are left as-is to keep
// the output readable.
func visualizeWS(s string) string {
	s = strings.ReplaceAll(s, "\t", "→")
	trimmed := strings.TrimLeft(s, " ")
	leading := len(s) - len(trimmed)
	if leading > 0 {
		s = strings.Repeat("·", leading) + trimmed
	}
	return s
}
