package prompt

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// initGitRepo makes dir a git repository with one commit, the smallest
// state every part of the prompt summary can be read from.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	if runtime.GOOS == "windows" {
		t.Skip("git command lines below assume a POSIX shell")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "seed")
}

func TestGetGitStatusCachesWithinTTL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepo(t, dir)

	first := getGitStatus(t.Context(), dir)
	require.Contains(t, first, "Current branch:")
	require.Contains(t, first, "Recent commits:")

	// A change the cache cannot see: while the entry is fresh the
	// summary stays what it was, which is what makes a dispatch fan-out
	// one build instead of one per prompt.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte("x\n"), 0o644))
	require.Equal(t, first, getGitStatus(t.Context(), dir))
}

func TestGetGitStatusRebuildsAfterTTL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepo(t, dir)

	require.NotEmpty(t, getGitStatus(t.Context(), dir))

	// Backdate the entry past the TTL: the next build sees the working
	// tree as it is now.
	gitStatusCache.Store(dir, gitStatusEntry{
		at:     time.Now().Add(-gitStatusCacheTTL - time.Second),
		status: "STALE",
	})
	fresh := getGitStatus(t.Context(), dir)
	require.NotEqual(t, "STALE", fresh)
	require.Contains(t, fresh, "Current branch:")
}
