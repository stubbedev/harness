package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWorktreeSkipsIgnoredToolingDirectories(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	worktreeTestWrite(t, root, ".gitignore", "ignored\n.devenv/\n")
	worktreeTestGit(t, root, "add", ".gitignore")
	worktreeTestGit(t, root, "commit", "-m", "ignore devenv")
	worktreeTestWrite(t, root, filepath.Join(".devenv", "state", "go", "pkg", "mod", "x@v1", "x.go"), "package x\n")
	readOnly := filepath.Join(root, ".devenv", "state", "go", "pkg", "mod", "x@v1")
	require.NoError(t, os.Chmod(filepath.Join(readOnly, "x.go"), 0o444))
	require.NoError(t, os.Chmod(readOnly, 0o555))
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o755) })
	worktreeTestWrite(t, root, "ignored", "ignored file is seeded\n")

	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(w.Path, ".devenv"))
	require.NoDirExists(t, filepath.Join(w.scratch, "baseline", ".devenv"))
	require.FileExists(t, filepath.Join(w.Path, "ignored"))
	worktreeTestWrite(t, w.Path, filepath.Join(".devenv", "profile", "bin"), "regenerated\n")

	result, err := w.Finish(t.Context())
	require.NoError(t, err)
	require.False(t, result.Changed)
	require.True(t, result.Removed)
	require.Equal(t, []string{".devenv"}, result.Excluded)
	require.NoDirExists(t, w.scratch)
	require.FileExists(t, filepath.Join(readOnly, "x.go"))
}

func TestWorktreeIgnoresDirectoriesTheChildCreatesIgnored(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	worktreeTestWrite(t, root, ".gitignore", "ignored\n.direnv/\n")
	worktreeTestGit(t, root, "add", ".gitignore")
	worktreeTestGit(t, root, "commit", "-m", "ignore direnv")
	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	worktreeTestWrite(t, w.Path, filepath.Join(".direnv", "flake-profile.rc"), "export X=1\n")

	result, err := w.Finish(t.Context())
	require.NoError(t, err)
	require.False(t, result.Changed)
	require.True(t, result.Removed)
}

func TestWorktreeRemovesReadOnlyDirectories(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not restrict deletion on Windows")
	}
	root := worktreeTestRepo(t)
	worktreeTestWrite(t, root, filepath.Join("ro", "data.txt"), "read only\n")
	require.NoError(t, os.Chmod(filepath.Join(root, "ro", "data.txt"), 0o444))
	require.NoError(t, os.Chmod(filepath.Join(root, "ro"), 0o555))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "ro"), 0o755) })

	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(w.Path, "ro", "data.txt"))

	result, err := w.Finish(t.Context())
	require.NoError(t, err)
	require.True(t, result.Removed)
	require.NoDirExists(t, w.Path)
	require.NoDirExists(t, w.scratch)
	require.NotContains(t, worktreeTestGit(t, root, "worktree", "list"), w.Path)
}

func TestWorktreeHashesNamesThatNeedQuoting(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("file names cannot contain newlines or quotes on Windows")
	}
	root := worktreeTestRepo(t)
	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	names := []string{"line\nbreak.txt", "\"quoted\".txt", "back\\slash.txt", "tab\there.txt", "carriage\r"}
	for i, name := range names {
		worktreeTestWrite(t, w.Path, name, fmt.Sprintf("content %d\n", i))
	}
	result, err := w.Finish(t.Context())
	require.NoError(t, err)
	require.True(t, result.Changed)
	patch, err := os.ReadFile(result.PatchPath)
	require.NoError(t, err)
	for i := range names {
		require.Contains(t, string(patch), fmt.Sprintf("+content %d\n", i))
	}
}

func TestWorktreeFinalizationNeverTouchesTheSourceCheckout(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	worktreeTestWrite(t, root, "tracked", "staged\n")
	worktreeTestGit(t, root, "add", "tracked")
	worktreeTestWrite(t, root, "tracked", "dirty\n")
	worktreeTestWrite(t, root, "untracked", "local\n")
	indexPath := filepath.Join(root, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	headBefore := worktreeTestGit(t, root, "rev-parse", "HEAD")
	sourceBefore, _, err := snapshotWorktree(t.Context(), root, "", nil)
	require.NoError(t, err)

	committed, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	unchanged, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	timedOut, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)

	worktreeTestWrite(t, committed.Path, "added", "new file\n")
	worktreeTestGit(t, committed.Path, "add", "added", "tracked")
	worktreeTestGit(t, committed.Path, "commit", "-m", "child")
	worktreeTestWrite(t, timedOut.Path, "tracked", "child edit\n")

	expired, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-expired.Done()
	_, err = timedOut.Finish(expired)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	for _, w := range []*AgentWorktree{committed, unchanged, timedOut} {
		_, err := w.Finish(t.Context())
		require.NoError(t, err)
	}

	indexAfter, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	require.Equal(t, indexBefore, indexAfter, "the source index must be byte-identical")
	require.Equal(t, headBefore, worktreeTestGit(t, root, "rev-parse", "HEAD"))
	sourceAfter, _, err := snapshotWorktree(t.Context(), root, "", nil)
	require.NoError(t, err)
	require.Equal(t, sourceBefore, sourceAfter)
}

func TestWorktreeGitDoesNotOutliveItsContext(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell alias")
	}
	root := worktreeTestRepo(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := worktreeGit(ctx, root, nil, "-c", "alias.hang=!sleep 30", "hang")
	require.Error(t, err)
	require.Less(t, time.Since(start), 10*time.Second, "a child holding the output pipe must not keep the call alive past its context")
}

func BenchmarkWorktreeSnapshotTree(b *testing.B) {
	root := b.TempDir()
	for i := range 500 {
		target := filepath.Join(root, fmt.Sprintf("pkg%d", i%20), fmt.Sprintf("file%d.go", i))
		require.NoError(b, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(b, os.WriteFile(target, fmt.Appendf(nil, "package p\n\nconst N = %d\n", i), 0o644))
	}
	scratch := b.TempDir()
	_, err := worktreeGit(b.Context(), scratch, nil, "init", "--bare", filepath.Join(scratch, "snapshot.git"))
	require.NoError(b, err)
	w := &AgentWorktree{scratch: scratch}
	for b.Loop() {
		_, err := w.snapshotTree(b.Context(), root)
		require.NoError(b, err)
	}
}
