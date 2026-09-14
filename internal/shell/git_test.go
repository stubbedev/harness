package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestGitRepo creates a real git repo with one commit on main so
// worktree commands have something to check out. Skips the test when git
// is unavailable.
func initTestGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return repo
}

func runShellCommand(t *testing.T, dir, command string) (string, string) {
	t.Helper()
	var out, errOut strings.Builder
	err := Run(context.Background(), RunOptions{
		Command: command,
		Cwd:     dir,
		Env:     os.Environ(),
		Stdout:  &out,
		Stderr:  &errOut,
	})
	if err != nil {
		t.Fatalf("Run(%q): %v\nstdout:\n%s\nstderr:\n%s", command, err, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

// TestGitBuiltinRedirectsOutsideWorktrees pins the fix for worktrees
// landing in /tmp: a `git worktree add` targeting a path outside the
// repository is redirected into the repo's .worktrees directory.
func TestGitBuiltinRedirectsOutsideWorktrees(t *testing.T) {
	t.Parallel()
	repo := initTestGitRepo(t)

	outside := filepath.Join(t.TempDir(), "agent-wt")
	out, errOut := runShellCommand(t, repo, "git worktree add -b feature-x \""+outside+"\"")

	redirected := filepath.Join(repo, ".worktrees", "agent-wt")
	if _, err := os.Stat(filepath.Join(redirected, ".git")); err != nil {
		t.Fatalf("worktree not created at %s: %v\nstderr:\n%s", redirected, err, errOut)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("worktree was created outside the repo at %s", outside)
	}
	// The redirect note goes to stderr; stdout carries git's own output.
	if !strings.Contains(out, "Preparing worktree") && !strings.Contains(errOut, "Preparing worktree") {
		t.Errorf("git's own output missing; stdout: %q stderr: %q", out, errOut)
	}

	// The worktree dir must not pollute git status.
	statusOut, _ := runShellCommand(t, repo, "git status --porcelain")
	if strings.Contains(statusOut, ".worktrees") {
		t.Errorf(".worktrees shows as untracked:\n%s", statusOut)
	}

	// Ordinary git commands still work through the builtin.
	runShellCommand(t, repo, "git log --oneline -1")
}

// TestGitBuiltinKeepsRepoLocalPaths pins that a worktree path already
// inside the repository is left exactly where the caller put it.
func TestGitBuiltinKeepsRepoLocalPaths(t *testing.T) {
	t.Parallel()
	repo := initTestGitRepo(t)

	inRepo := filepath.Join(repo, "local-wt")
	_, _ = runShellCommand(t, repo, "git worktree add \""+inRepo+"\"")

	if _, err := os.Stat(filepath.Join(inRepo, ".git")); err != nil {
		t.Fatalf("repo-local worktree not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".worktrees")); !os.IsNotExist(err) {
		t.Errorf(".worktrees was created even though the path was repo-local")
	}
}

// TestGitBuiltinHonorsConfiguredWorktreesDir pins the per-repo override:
// harness.worktreesDir relocates redirected worktrees.
func TestGitBuiltinHonorsConfiguredWorktreesDir(t *testing.T) {
	t.Parallel()
	repo := initTestGitRepo(t)
	runShellCommand(t, repo, "git config harness.worktreesDir ../wt-garden")

	outside := filepath.Join(t.TempDir(), "cfg-wt")
	runShellCommand(t, repo, "git worktree add -b feature-z \""+outside+"\"")

	want := filepath.Join(filepath.Dir(repo), "wt-garden", "cfg-wt")
	if _, err := os.Stat(filepath.Join(want, ".git")); err != nil {
		t.Fatalf("worktree not created at configured dir %s: %v", want, err)
	}
}

// TestGitBuiltinSymlinkedCwd pins the canonicalization fix: when the
// shell's cwd reaches the repo through a symlink (TMPDIR on macOS) while
// git reports the resolved path, a repo-local worktree must still be
// recognized as repo-local and left alone.
func TestGitBuiltinSymlinkedCwd(t *testing.T) {
	t.Parallel()
	repo := initTestGitRepo(t)

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	inRepo := filepath.Join(link, "local-wt")
	out, errOut := runShellCommand(t, link, "git worktree add \""+inRepo+"\"")

	if _, err := os.Stat(filepath.Join(inRepo, ".git")); err != nil {
		t.Fatalf("repo-local worktree not created via symlinked cwd: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(repo, ".worktrees")); !os.IsNotExist(err) {
		t.Errorf(".worktrees was created even though the path was repo-local")
	}
}

// TestGitBuiltinNotARepo passes through outside a repository: the command
// fails with git's own error rather than ours.
func TestGitBuiltinNotARepo(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	err := Run(context.Background(), RunOptions{
		Command: "git worktree add /tmp/nope-wt",
		Cwd:     dir,
		Env:     os.Environ(),
	})
	if err == nil {
		t.Fatal("expected git to fail outside a repository")
	}
}
