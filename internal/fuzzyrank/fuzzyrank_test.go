package fuzzyrank

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kontainerSkills mirrors the skill set of issue #59: fourteen skills,
// most named kontainer-*, where a description that starts with a
// matching word used to outrank the name match.
var kontainerSkills = []Fields{
	{Primary: "kontainer-architecture", Rest: "Use when deciding where new code belongs, navigating the Kontainer codebase"},
	{Primary: "kontainer-browser-test", Rest: "Use this skill to manually QA a Bitbucket PR in a real browser"},
	{Primary: "kontainer-cut-release", Rest: "Cut a Kontainer release (release/X.Y.Z branch off develop)"},
	{Primary: "kontainer-debugging", Rest: "Use when debugging a failure, investigating an exception in this Kontainer app"},
	{Primary: "kontainer-git-workflow", Rest: "Use whenever committing, branching, opening or updating a PR"},
	{Primary: "kontainer-php", Rest: "MANDATORY whenever writing, editing, or reviewing backend PHP in this Kontainer repo"},
	{Primary: "kontainer-pr-callstack-html", Rest: "Produce an interactive HTML call-stack map of a pull request's BACKEND paths"},
	{Primary: "kontainer-pr", Rest: "Drive the open PR for the current branch. Use when the user says '/pr'"},
	{Primary: "kontainer-prs", Rest: "Loop the kontainer-pr flow over every open PR you author or review"},
	{Primary: "kontainer-release-watch", Rest: "Watch Sentry for fallout after a Kontainer deploy"},
	{Primary: "kontainer-sync-branch", Rest: "Pull the base branch into the current branch and resolve the merge conflicts"},
	{Primary: "kontainer-testing", Rest: "MANDATORY whenever writing, editing, fixing, or reviewing a test"},
	{Primary: "kontainer-translation-review", Rest: "Review or correct a non-English locale in this Kontainer repo"},
	{Primary: "laravel-best-practices", Rest: "Apply this skill whenever writing, reviewing, or refactoring Laravel PHP code"},
}

// names ranks the skill set for query and returns the primary fields in
// match order.
func names(t *testing.T, query string) []string {
	t.Helper()
	type row struct {
		name  string
		score int
	}
	var rows []row
	for _, f := range kontainerSkills {
		res, ok := Match(query, f)
		if ok {
			rows = append(rows, row{f.Primary, res.Score})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].score != rows[j].score {
			return rows[i].score > rows[j].score
		}
		return len(rows[i].name) < len(rows[j].name)
	})
	assert.Greater(t, len(rows), 0, "query %q matched nothing", query)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.name
	}
	return out
}

// TestNameTierBeatsDescriptionTier pins the ranking shape: a candidate
// whose name contains the query ranks above one that matches only in its
// description, and within a tier the shorter name wins the tie.
func TestNameTierBeatsDescriptionTier(t *testing.T) {
	t.Parallel()

	got := names(t, "pr")
	require.NotEmpty(t, got)
	assert.Equal(t, "kontainer-pr", got[0], "got %v", got)
	assert.Equal(t, "kontainer-prs", got[1], "got %v", got)

	got = names(t, "sync")
	assert.Equal(t, "kontainer-sync-branch", got[0], "got %v", got)

	got = names(t, "lar")
	assert.Equal(t, "laravel-best-practices", got[0], "got %v", got)
}

// TestExactNameMatchRanksFirst pins the top tier: the query equal to the
// name beats every other candidate.
func TestExactNameMatchRanksFirst(t *testing.T) {
	t.Parallel()

	got := names(t, "kontainer-php")
	assert.Equal(t, "kontainer-php", got[0], "got %v", got)
}

// TestSubsequenceNameMatch pins the fzf subsequence path: a scrambled
// term still matches, and the consecutive name match ranks first.
func TestSubsequenceNameMatch(t *testing.T) {
	t.Parallel()

	got := names(t, "kpr")
	assert.Equal(t, "kontainer-pr", got[0], "got %v", got)
}

// TestCaseInsensitive pins matching across case: a term with uppercase
// runes still matches a lowercase name, the way the old always-lowercase
// matcher behaved.
func TestCaseInsensitive(t *testing.T) {
	t.Parallel()

	got := names(t, "PR")
	assert.Equal(t, "kontainer-pr", got[0], "got %v", got)

	res, ok := Match("merge PR", Fields{Primary: "pull_request_merge", Rest: "Merge a pull request"})
	require.True(t, ok)
	assert.NotEmpty(t, res.Primary, "matched the 'pr' in pull_request_merge")
}

// TestMultiTermAllMustMatch pins fzf's extended-search AND: a term with
// no match anywhere rejects the candidate, and both terms' offsets come
// back per field.
func TestMultiTermAllMustMatch(t *testing.T) {
	t.Parallel()

	got := names(t, "pr callstack")
	assert.Equal(t, []string{"kontainer-pr-callstack-html"}, got)

	_, ok := Match("pr nonexistent", Fields{Primary: "kontainer-pr", Rest: "Drive the open PR"})
	assert.False(t, ok)
}

// TestOffsets pins the returned offsets: byte positions into the field
// they matched, sorted.
func TestOffsets(t *testing.T) {
	t.Parallel()

	res, ok := Match("pr", Fields{Primary: "kontainer-pr", Rest: "Drive the open PR"})
	require.True(t, ok)
	assert.Equal(t, []int{10, 11}, res.Primary)
	assert.Empty(t, res.Rest)

	res, ok = Match("pr", Fields{Primary: "kontainer-php", Rest: "Drive the open PR"})
	require.True(t, ok)
	assert.Empty(t, res.Primary)
	assert.Equal(t, []int{15, 16}, res.Rest)
}

// TestOffsetsNonASCII pins byte offsets for text with multi-byte runes:
// fzf reports rune indexes, callers highlight by byte offset.
func TestOffsetsNonASCII(t *testing.T) {
	t.Parallel()

	res, ok := Match("pr", Fields{Primary: "køntainer-pr"})
	require.True(t, ok)
	// "kø" is 2 bytes wide, so the "pr" sits at byte 11.
	assert.Equal(t, []int{11, 12}, res.Primary)
}

// TestEmptyQueryMatchesNothing pins the guard: an empty query is not a
// match.
func TestEmptyQuery(t *testing.T) {
	t.Parallel()

	_, ok := Match("", Fields{Primary: "kontainer-pr", Rest: "text"})
	assert.False(t, ok)
	_, ok = Match("   ", Fields{Primary: "kontainer-pr", Rest: "text"})
	assert.False(t, ok)
}
