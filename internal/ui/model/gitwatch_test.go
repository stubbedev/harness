package model

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// runGit runs git in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
}

func TestResolveGitDirs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	d := resolveGitDirs(dir)
	require.Equal(t, filepath.Join(dir, ".git"), d.gitDir)
	require.Equal(t, filepath.Join(dir, ".git"), d.commonDir)

	d = resolveGitDirs(t.TempDir())
	require.Empty(t, d.gitDir)
	require.Empty(t, d.commonDir)
}

func TestBranchFromPorcelain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "tracking branch", status: "## main...origin/main", want: "main"},
		{name: "no upstream", status: "## main", want: "main"},
		{name: "dotted branch name", status: "## release/1.0...origin/release/1.0", want: "release/1.0"},
		{name: "ahead and behind", status: "## main...origin/main [ahead 2, behind 3]", want: "main"},
		{name: "detached head", status: "## HEAD (no branch)", want: "HEAD"},
		{name: "unborn branch", status: "## No commits yet on main", want: "main"},
		{name: "no branch header", status: " M a.go", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, branchFromPorcelain(tt.status))
		})
	}
}

// TestGitWatcherReactive drives the git watcher end to end: the initial
// refresh, the poke path (an agent edit that never touches .git), the
// filesystem path (git add and commit writing the index and refs), and a
// branch switch writing HEAD. It must not run in parallel with other
// watcher tests: the watcher is a package-level singleton with one
// watched target at a time.
func TestGitWatcherReactive(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	write := func(name string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644))
	}
	write("a.txt")
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-m", "init")

	status := func() string {
		return gitWatch.status(dir)
	}
	await := func(substring string) {
		t.Helper()
		require.Eventually(t, func() bool {
			return strings.Contains(status(), substring)
		}, 10*time.Second, 50*time.Millisecond, "expected %q in %q", substring, status())
	}
	awaitAbsent := func(substring string) {
		t.Helper()
		require.Eventually(t, func() bool {
			return !strings.Contains(status(), substring)
		}, 10*time.Second, 50*time.Millisecond, "expected %q gone from %q", substring, status())
	}

	// The draw path returns the (empty) cached value and never blocks;
	// the initial refresh lands in the background.
	require.Empty(t, status())
	await("main")
	require.NotContains(t, status(), "[")

	// A worktree-only change is invisible to the .git watch: it is the
	// tool-result poke that must pick it up.
	write("untracked.txt")
	gitWatch.pokeSoon()
	await("?1")

	// git add rewrites the index: the filesystem watch alone must move
	// the count from untracked to staged.
	runGit(t, dir, "add", "untracked.txt")
	await("+1")
	require.NotContains(t, status(), "?1")

	// A commit rewrites index and refs; the summary goes quiet again.
	runGit(t, dir, "commit", "-m", "second")
	awaitAbsent("[")

	// Checking out a branch rewrites HEAD.
	runGit(t, dir, "checkout", "-b", "feature")
	await("feature")

	// Detached HEAD shows the short SHA, not the literal "HEAD".
	runGit(t, dir, "checkout", "--detach")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	require.NoError(t, err)
	await(strings.TrimSpace(string(out)))
	runGit(t, dir, "checkout", "feature")
	await("feature")
}
