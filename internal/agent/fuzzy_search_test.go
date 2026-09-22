package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kontainerCandidates mirrors the skill set of issue #59: a description
// that starts with the query word used to outrank the name that contains
// it.
var kontainerCandidates = []searchCandidate{
	{name: "kontainer-architecture", desc: "Use when deciding where new code belongs, navigating the Kontainer codebase"},
	{name: "kontainer-browser-test", desc: "Use this skill to manually QA a Bitbucket PR in a real browser"},
	{name: "kontainer-cut-release", desc: "Cut a Kontainer release (release/X.Y.Z branch off develop)"},
	{name: "kontainer-debugging", desc: "Use when debugging a failure, investigating an exception"},
	{name: "kontainer-git-workflow", desc: "Use whenever committing, branching, opening or updating a PR"},
	{name: "kontainer-php", desc: "MANDATORY whenever writing, editing, or reviewing backend PHP"},
	{name: "kontainer-pr-callstack-html", desc: "Produce an interactive HTML call-stack map of a pull request"},
	{name: "kontainer-pr", desc: "Drive the open PR for the current branch. Use when the user says '/pr'"},
	{name: "kontainer-prs", desc: "Loop the kontainer-pr flow over every open PR you author or review"},
	{name: "kontainer-release-watch", desc: "Watch Sentry for fallout after a Kontainer deploy"},
	{name: "kontainer-sync-branch", desc: "Pull the base branch into the current branch and resolve conflicts"},
	{name: "kontainer-testing", desc: "MANDATORY whenever writing, editing, or reviewing a test"},
	{name: "kontainer-translation-review", desc: "Review or correct a non-English locale in this repo"},
	{name: "laravel-best-practices", desc: "Apply this skill whenever writing or reviewing Laravel PHP code"},
}

func rankNames(query string, limit int) []string {
	names, _ := rankCandidates(query, kontainerCandidates, limit)
	return names
}

// TestRankCandidatesNameAboveDescription pins the shape: the candidate
// whose name contains the query ranks first, over candidates that only
// match in their description.
func TestRankCandidatesNameAboveDescription(t *testing.T) {
	t.Parallel()

	got := rankNames("pr", 5)
	require.NotEmpty(t, got)
	assert.Equal(t, "kontainer-pr", got[0], "got %v", got)
	assert.Equal(t, "kontainer-prs", got[1], "got %v", got)

	got = rankNames("sync", 5)
	assert.Equal(t, "kontainer-sync-branch", got[0], "got %v", got)
}

// TestRankCandidatesAllTermsRequired pins the AND: a candidate missing
// one term stays out.
func TestRankCandidatesAllTermsRequired(t *testing.T) {
	t.Parallel()

	got := rankNames("pr callstack", 20)
	assert.Equal(t, []string{"kontainer-pr-callstack-html"}, got)

	got = rankNames("pr nonexistent", 20)
	assert.Empty(t, got)
}

// TestRankCandidatesCaseInsensitive pins matching across case.
func TestRankCandidatesCaseInsensitive(t *testing.T) {
	t.Parallel()

	got := rankNames("PR", 5)
	assert.Equal(t, "kontainer-pr", got[0], "got %v", got)
}

// TestRankCandidatesLimit pins the cap and the matched total.
func TestRankCandidatesLimit(t *testing.T) {
	t.Parallel()

	names, matched := rankCandidates("pr", kontainerCandidates, 2)
	require.Len(t, names, 2)
	assert.Equal(t, "kontainer-pr", names[0])
	assert.Greater(t, matched, 2, "matched counts every match, not just the top")
}
