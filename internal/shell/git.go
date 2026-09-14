package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"mvdan.cc/sh/v3/interp"
)

// handleGit is the `git` builtin. Everything passes straight through to the
// real git binary; the only interception is `git worktree add <path>`: when
// the requested path lies outside the repository (typically /tmp, which can
// exhaust a tmpfs), it is redirected into the repository's worktree
// directory so worktrees land where the repo keeps them.
//
// The directory is `<repoRoot>/.worktrees` by default, overridable per repo
// with `git config harness.worktreesDir <dir>` (relative to the repo root).
// A redirected destination inside the repo is added to .git/info/exclude so
// it never shows up as untracked noise.
func handleGit(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	hc := interp.HandlerCtx(ctx)

	args = redirectWorktreeAdd(ctx, hc, args, stderr)

	path, err := interp.LookPathDir(hc.Dir, hc.Env, "git")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return interp.ExitStatus(127)
	}

	cmd := exec.CommandContext(ctx, path, args[1:]...)
	cmd.Dir = hc.Dir
	cmd.Env = execEnvList(hc.Env)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	isolateProcess(cmd)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			code := exitErr.ExitCode()
			if code < 0 {
				code = 1
			}
			return interp.ExitStatus(uint8(code))
		}
		return err
	}
	return nil
}

// redirectWorktreeAdd rewrites the path argument of a `git worktree add`
// that targets a location outside the repository, returning the (possibly
// unchanged) args. Failures never block the command: on any error the
// original args run unchanged and git reports the problem itself.
func redirectWorktreeAdd(ctx context.Context, hc interp.HandlerContext, args []string, stderr io.Writer) []string {
	// args: git worktree add <options> <path> [<commit-ish>]
	if len(args) < 4 || args[1] != "worktree" || args[2] != "add" {
		return args
	}
	idx, ok := worktreeAddPathIndex(args[3:])
	if !ok {
		return args
	}
	idx += 3

	dir := hc.Dir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return args
		}
	}

	given := args[idx]
	abs := given
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(dir, abs)
	}
	abs = filepath.Clean(abs)

	root, ok := gitOutputValue(ctx, hc, "rev-parse", "--show-toplevel")
	if !ok {
		return args
	}
	root = filepath.Clean(root)
	if sameOrInside(abs, root) {
		// Already repo-local: the caller chose a deliberate spot.
		return args
	}

	dest := worktreesDir(ctx, hc, root, stderr)
	if dest == "" {
		return args
	}
	base := filepath.Base(abs)
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = fmt.Sprintf("worktree-%d", time.Now().Unix())
	}
	newPath := filepath.Join(dest, base)

	out := slices.Clone(args)
	out[idx] = newPath
	fmt.Fprintf(stderr, "harness: worktree path %s is outside the repository; placing it at %s\n", given, newPath)
	return out
}

// worktreeAddPathIndex returns the position of the <path> operand in the
// arguments following `git worktree add`, skipping flags and the values of
// flags that take one (-b, -B).
func worktreeAddPathIndex(sub []string) (int, bool) {
	for i := 0; i < len(sub); i++ {
		tok := sub[i]
		switch {
		case tok == "-b" || tok == "-B":
			i++
		case strings.HasPrefix(tok, "-"):
			// Other worktree-add flags (--detach, --force, ...) take no
			// separate value; combined short forms like -fb are not valid
			// git syntax here.
		default:
			return i, true
		}
	}
	return 0, false
}

// worktreesDir resolves the repository's worktree directory: the
// harness.worktreesDir git config when set (relative values resolve against
// the repo root), otherwise <root>/.worktrees. The directory is created,
// and the default location is recorded in .git/info/exclude. Returns "" on
// failure.
func worktreesDir(ctx context.Context, hc interp.HandlerContext, root string, stderr io.Writer) string {
	dest := ""
	if cfg, ok := gitOutputValue(ctx, hc, "config", "--get", "harness.worktreesDir"); ok && strings.TrimSpace(cfg) != "" {
		cfg = strings.TrimSpace(cfg)
		if filepath.IsAbs(cfg) {
			dest = filepath.Clean(cfg)
		} else {
			dest = filepath.Join(root, cfg)
		}
	}
	configured := dest != ""
	if !configured {
		dest = filepath.Join(root, ".worktrees")
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		fmt.Fprintf(stderr, "harness: cannot create worktree directory %s: %v\n", dest, err)
		return ""
	}
	if !configured {
		excludeWorktreesDir(root, dest)
	}
	return dest
}

// excludeWorktreesDir appends the directory to .git/info/exclude so the
// repo's worktree folder does not show up as untracked files. Best effort:
// .git may be a file (linked worktrees, submodules), in which case there is
// nothing to do here.
func excludeWorktreesDir(root, dir string) {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	exclude := filepath.Join(root, ".git", "info", "exclude")
	info, err := os.Stat(filepath.Dir(exclude))
	if err != nil || !info.IsDir() {
		return
	}
	line := filepath.ToSlash(rel) + "/"
	data, _ := os.ReadFile(exclude)
	for l := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return
		}
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(line + "\n")
}

// gitOutputValue runs a read-only git query in the handler's directory and
// environment, returning its trimmed stdout. Returns false when git is
// missing or the query fails (e.g. outside a repository).
func gitOutputValue(ctx context.Context, hc interp.HandlerContext, gitArgs ...string) (string, bool) {
	path, err := interp.LookPathDir(hc.Dir, hc.Env, "git")
	if err != nil {
		return "", false
	}
	cmd := exec.CommandContext(ctx, path, gitArgs...)
	cmd.Dir = hc.Dir
	cmd.Env = execEnvList(hc.Env)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// sameOrInside reports whether path is dir itself or inside it, after
// resolving both sides to their canonical form. The resolution matters:
// the shell's cwd can reach the same directory through a symlink (TMPDIR
// on macOS) or a short name (RUNNER~1 on Windows) while git reports the
// canonical path, and the raw comparison would misread repo-local paths
// as outside.
func sameOrInside(path, dir string) bool {
	path = canonicalize(path)
	dir = canonicalize(dir)
	if runtime.GOOS == "windows" {
		// Drive letters and 8.3 names differ in case alone.
		path = strings.ToLower(path)
		dir = strings.ToLower(dir)
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// canonicalize resolves symlinks in the longest existing prefix of path.
// The final elements usually do not exist yet (they are the worktree being
// created), which would make a plain EvalSymlinks fail and leave symlinked
// cwds (TMPDIR on macOS) unresolvable.
func canonicalize(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}
	dir, rest := filepath.Split(path)
	if dir == path {
		return path
	}
	if rest == "" {
		return canonicalize(filepath.Clean(dir))
	}
	return filepath.Join(canonicalize(filepath.Clean(dir)), rest)
}
