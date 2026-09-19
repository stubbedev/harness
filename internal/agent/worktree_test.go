package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func worktreeTestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := worktreeGit(t.Context(), root, nil, args...)
	require.NoError(t, err)
	return string(out)
}

func worktreeTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	worktreeTestGit(t, root, "init")
	worktreeTestGit(t, root, "config", "user.name", "Test")
	worktreeTestGit(t, root, "config", "user.email", "test@example.invalid")
	worktreeTestWrite(t, root, "tracked", "committed\n")
	worktreeTestWrite(t, root, "deleted", "delete me\n")
	worktreeTestWrite(t, root, ".gitignore", "ignored\n")
	worktreeTestGit(t, root, "add", ".")
	worktreeTestGit(t, root, "commit", "-m", "fixture")
	return root
}

func worktreeTestWrite(t *testing.T, root, name, value string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(value), 0o644))
}

func TestWorktreeDirtyBaselineAndIndependentChildren(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	worktreeTestWrite(t, root, "tracked", "staged\n")
	worktreeTestGit(t, root, "add", "tracked")
	worktreeTestWrite(t, root, "tracked", "dirty\n")
	worktreeTestWrite(t, root, "untracked", "local\n")
	worktreeTestWrite(t, root, "ignored", "ignored local\n")
	require.NoError(t, os.Remove(filepath.Join(root, "deleted")))
	before, err := snapshotWorktree(t.Context(), root, "")
	require.NoError(t, err)
	indexBefore := worktreeTestGit(t, root, "diff", "--cached", "--binary")
	statusBefore := worktreeTestGit(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	first, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	second, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	require.NotEqual(t, first.Path, second.Path)
	require.NotEqual(t, first.Branch, second.Branch)
	for _, w := range []*AgentWorktree{first, second} {
		got, err := snapshotWorktree(t.Context(), w.Path, "")
		require.NoError(t, err)
		require.Equal(t, before, got)
	}
	worktreeTestWrite(t, first.Path, "tracked", "child\n")
	data, err := os.ReadFile(filepath.Join(second.Path, "tracked"))
	require.NoError(t, err)
	require.Equal(t, "dirty\n", string(data))
	changed, err := first.Finish(t.Context())
	require.NoError(t, err)
	require.True(t, changed.Changed)
	require.True(t, changed.Preserved)
	require.FileExists(t, changed.PatchPath)
	require.DirExists(t, changed.Path)
	unchanged, err := second.Finish(t.Context())
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.True(t, unchanged.Removed)
	require.NoDirExists(t, second.Path)
	require.NotContains(t, worktreeTestGit(t, root, "branch", "--list"), second.Branch)
	again, err := second.Finish(t.Context())
	require.NoError(t, err)
	require.Equal(t, unchanged, again)
	after, err := snapshotWorktree(t.Context(), root, "")
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, indexBefore, worktreeTestGit(t, root, "diff", "--cached", "--binary"))
	require.Equal(t, statusBefore, worktreeTestGit(t, root, "status", "--porcelain=v1", "--untracked-files=all"))
}

func TestWorktreeBinaryPatchIncludesUntrackedAndDirtyBaseline(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	worktreeTestWrite(t, root, "tracked", "dirty baseline\n")
	worktreeTestWrite(t, root, "existing.bin", "\x00\x01baseline")
	worktreeTestWrite(t, root, ".gitattributes", "*.bin -diff\ntracked text eol=crlf\n")
	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	worktreeTestWrite(t, w.Path, "tracked", "child\n")
	worktreeTestWrite(t, w.Path, "existing.bin", "\x00\x02child")
	worktreeTestWrite(t, w.Path, "new.bin", "\x00new\xffbinary")
	worktreeTestWrite(t, w.Path, "ignored", "ignored child\n")
	require.NoError(t, os.Remove(filepath.Join(w.Path, "deleted")))
	result, err := w.Finish(t.Context())
	require.NoError(t, err)
	patch, err := os.ReadFile(result.PatchPath)
	require.NoError(t, err)
	require.Contains(t, string(patch), "GIT binary patch")
	require.Contains(t, string(patch), "-dirty baseline")
	require.NotContains(t, string(patch), "-committed")
	inspection := t.TempDir()
	_, err = snapshotWorktree(t.Context(), filepath.Join(w.scratch, "baseline"), inspection)
	require.NoError(t, err)
	worktreeTestGit(t, inspection, "init")
	require.NoError(t, os.WriteFile(filepath.Join(inspection, ".git", "info", "attributes"), []byte("* -text\n"), 0o644))
	worktreeTestGit(t, inspection, "apply", "--binary", result.PatchPath)
	want, err := snapshotWorktree(t.Context(), w.Path, "")
	require.NoError(t, err)
	got, err := snapshotWorktree(t.Context(), inspection, "")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestWorktreeCancellationPreserves(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w, err := NewWorktree(ctx, root, t.TempDir())
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, w)
	w, err = NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	worktreeTestWrite(t, w.Path, "untracked", "keep\n")
	result, err := w.Finish(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, result.Preserved)
	require.FileExists(t, filepath.Join(w.Path, "untracked"))
	result, err = w.Finish(t.Context())
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.FileExists(t, result.PatchPath)
}

func TestWorktreeRejectInvalidSource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, err := NewWorktree(t.Context(), root, t.TempDir())
	require.ErrorContains(t, err, "requires a Git checkout")
	worktreeTestGit(t, root, "init")
	_, err = NewWorktree(t.Context(), root, t.TempDir())
	require.ErrorContains(t, err, "requires an existing commit")
	root = worktreeTestRepo(t)
	_, err = NewWorktree(t.Context(), root, root)
	require.ErrorContains(t, err, "outside the source checkout")
}

func TestWorktreeSymlinksNotFollowed(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	external := t.TempDir()
	worktreeTestWrite(t, external, "secret", "untouched\n")
	require.NoError(t, os.Symlink(external, filepath.Join(root, "external")))
	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	target, err := os.Readlink(filepath.Join(w.Path, "external"))
	require.NoError(t, err)
	require.Equal(t, external, target)
	result, err := w.Finish(t.Context())
	require.NoError(t, err)
	require.True(t, result.Removed)
	data, err := os.ReadFile(filepath.Join(external, "secret"))
	require.NoError(t, err)
	require.Equal(t, "untouched\n", string(data))
}

func TestWorktreeUncertainIdentityPreserved(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	worktreeTestWrite(t, w.Path, ".git", "invalid\n")
	result, err := w.Finish(t.Context())
	require.Error(t, err)
	require.True(t, result.Preserved)
	require.DirExists(t, w.Path)
}

func TestWorktreeStagedOnlyChangesPreserved(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	worktreeTestWrite(t, w.Path, "tracked", "staged child\n")
	worktreeTestGit(t, w.Path, "add", "tracked")
	worktreeTestWrite(t, w.Path, "tracked", "committed\n")
	result, err := w.Finish(t.Context())
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.True(t, result.Preserved)
	require.Contains(t, worktreeTestGit(t, w.Path, "diff", "--cached"), "staged child")
	patch, err := os.ReadFile(result.IndexPatchPath)
	require.NoError(t, err)
	require.Contains(t, string(patch), "+staged child")
}

func TestWorktreeNestedRepositoryFailsClosed(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(nested, 0o755))
	worktreeTestGit(t, nested, "init")
	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.ErrorContains(t, err, "nested Git metadata")
	require.NotNil(t, w)
	result, err := w.Finish(t.Context())
	require.Error(t, err)
	require.True(t, result.Preserved)
	require.DirExists(t, filepath.Join(nested, ".git"))
}

func TestWorktreePinsRootAndIgnoresGitEnvironment(t *testing.T) {
	root := worktreeTestRepo(t)
	other := worktreeTestRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, "rogue-index"))
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	w, err := NewWorktree(t.Context(), sub, t.TempDir())
	require.NoError(t, err)
	require.Equal(t, root, w.SourceRoot)
	require.True(t, strings.HasPrefix(w.Branch, "harness/"))
	result, err := w.Finish(t.Context())
	require.NoError(t, err)
	require.True(t, result.Removed)
	require.NoFileExists(t, filepath.Join(other, "rogue-index"))
}

func TestWorktreeUntrackedOnlyChangePreserved(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	worktreeTestWrite(t, w.Path, "ignored", "must survive\n")
	result, err := w.Finish(t.Context())
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.True(t, result.Preserved)
	require.FileExists(t, filepath.Join(w.Path, "ignored"))
	patch, err := os.ReadFile(result.PatchPath)
	require.NoError(t, err)
	require.Contains(t, string(patch), "+must survive")
}

func TestWorktreeRejectedStorageDoesNotModifySource(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	parent := filepath.Join(root, "new", "scratch")
	_, err := NewWorktree(t.Context(), root, parent)
	require.ErrorContains(t, err, "outside the source checkout")
	require.NoDirExists(t, filepath.Join(root, "new"))
	external := t.TempDir()
	require.NoError(t, os.Symlink(root, filepath.Join(external, "link")))
	_, err = NewWorktree(t.Context(), root, filepath.Join(external, "link", "new"))
	require.ErrorContains(t, err, "outside the source checkout")
	require.NoDirExists(t, filepath.Join(root, "new"))
}

func TestWorktreeReplacedDirectoryPreserved(t *testing.T) {
	t.Parallel()
	root := worktreeTestRepo(t)
	w, err := NewWorktree(t.Context(), root, t.TempDir())
	require.NoError(t, err)
	moved := w.Path + "-moved"
	require.NoError(t, os.Rename(w.Path, moved))
	require.NoError(t, os.Symlink(moved, w.Path))
	result, err := w.Finish(t.Context())
	require.ErrorContains(t, err, "directory identity changed")
	require.True(t, result.Preserved)
	require.FileExists(t, filepath.Join(moved, "tracked"))
}
