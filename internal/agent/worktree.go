package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/stubbedev/harness/internal/filepathext"
	"github.com/stubbedev/harness/internal/fsext"
)

type AgentWorktree struct {
	SourceRoot string
	Path       string
	Branch     string

	mu           sync.Mutex
	scratch      string
	gitDir       string
	head         string
	baseline     map[string]worktreeEntry
	exclusions   *worktreeExclusions
	excluded     []string
	baselineGit  string
	baselineTree string
	gitFile      string
	pathInfo     os.FileInfo
	ready        bool
	removed      bool
}

type WorktreeResult struct {
	Path           string   `json:"path"`
	Branch         string   `json:"branch"`
	PatchPath      string   `json:"patch_path,omitempty"`
	IndexPatchPath string   `json:"index_patch_path,omitempty"`
	Changed        bool     `json:"changed"`
	Preserved      bool     `json:"preserved"`
	Removed        bool     `json:"removed"`
	Excluded       []string `json:"excluded,omitempty"`
}

func NewWorktree(ctx context.Context, sourceRoot, parentDir string) (result *AgentWorktree, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(sourceRoot)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	out, err := worktreeGit(ctx, root, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("worktree isolation requires a Git checkout: %w", err)
	}
	root, err = filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, err
	}
	out, err = worktreeGit(ctx, root, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	gitDir := strings.TrimSpace(string(out))
	pin := []string{"--git-dir=" + gitDir, "--work-tree=" + root}
	out, err = worktreeGit(ctx, root, pin, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("worktree isolation requires an existing commit: %w", err)
	}
	head := strings.TrimSpace(string(out))
	tracked, err := worktreeGit(ctx, root, pin, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	for entry := range bytes.SplitSeq(tracked, []byte{0}) {
		if bytes.HasPrefix(entry, []byte("160000 ")) {
			return nil, errors.New("worktree isolation does not support submodules; source preserved")
		}
	}
	ignoredDirs, err := worktreeIgnoredDirs(ctx, root, pin)
	if err != nil {
		return nil, err
	}
	exclusions := &worktreeExclusions{trackedDirs: worktreeTrackedDirs(tracked), ignoredDirs: ignoredDirs}
	if parentDir == "" {
		parentDir = os.TempDir()
	}
	parentDir, err = filepath.Abs(parentDir)
	if err != nil {
		return nil, err
	}
	parentDir, err = filepathext.Resolve(parentDir)
	if err != nil {
		return nil, err
	}
	if _, inside := filepathext.RelWithin(root, parentDir); inside {
		return nil, errors.New("worktree storage must be outside the source checkout")
	}
	if err = os.MkdirAll(parentDir, 0o700); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp(parentDir, "harness-worktree-")
	if err != nil {
		return nil, err
	}
	w := &AgentWorktree{SourceRoot: root, Path: filepath.Join(scratch, "worktree"), Branch: "harness/" + filepath.Base(scratch), scratch: scratch, gitDir: gitDir, head: head, exclusions: exclusions}
	defer func() {
		if err != nil {
			err = fmt.Errorf("worktree creation failed; preserved storage %s, checkout %s, branch %s: %w", scratch, w.Path, w.Branch, err)
		}
	}()
	baselinePath := filepath.Join(scratch, "baseline")
	w.baseline, w.excluded, err = snapshotWorktree(ctx, root, baselinePath, exclusions)
	if err != nil {
		return w, fmt.Errorf("snapshot source; preserved %s: %w", scratch, err)
	}
	verify, _, err := snapshotWorktree(ctx, root, "", exclusions)
	if err != nil || !equalWorktreeEntries(w.baseline, verify) {
		return w, fmt.Errorf("source changed during snapshot; preserved %s: %w", scratch, errors.Join(err, errors.New("snapshot is not stable")))
	}
	if _, err = worktreeGit(ctx, scratch, nil, "init", "--bare", filepath.Join(scratch, "snapshot.git")); err != nil {
		return w, err
	}
	w.baselineTree, err = w.snapshotTree(ctx, baselinePath)
	if err != nil {
		return w, err
	}
	if _, err = worktreeGit(ctx, root, pin, "worktree", "add", "--no-checkout", "-b", w.Branch, w.Path, head); err != nil {
		return w, fmt.Errorf("create worktree; preserved %s: %w", w.Path, err)
	}
	if _, err = w.git(ctx, "read-tree", head); err != nil {
		return w, err
	}
	if _, _, err = snapshotWorktree(ctx, baselinePath, w.Path, exclusions); err != nil {
		return w, err
	}
	actual, _, err := snapshotWorktree(ctx, w.Path, "", exclusions)
	if err != nil || !equalWorktreeEntries(w.baseline, actual) {
		return w, errors.Join(err, errors.New("worktree overlay differs from baseline; preserving worktree"))
	}
	gitFile, err := os.ReadFile(filepath.Join(w.Path, ".git"))
	if err != nil {
		return w, err
	}
	w.gitFile = string(gitFile)
	w.baselineGit, err = w.gitState(ctx)
	if err != nil {
		return w, err
	}
	w.pathInfo, err = os.Lstat(w.Path)
	if err != nil {
		return w, err
	}
	w.ready = true
	return w, nil
}

func (w *AgentWorktree) Finish(ctx context.Context) (WorktreeResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := WorktreeResult{Path: w.Path, Branch: w.Branch, Excluded: w.excluded, Preserved: !w.removed, Removed: w.removed}
	if w.removed {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !w.ready {
		return result, fmt.Errorf("worktree creation incomplete; preserving %s", w.scratch)
	}
	info, err := os.Lstat(w.Path)
	if err != nil || !info.IsDir() || !os.SameFile(w.pathInfo, info) {
		return result, errors.Join(err, errors.New("worktree directory identity changed; preserving worktree"))
	}
	metadata, err := os.Lstat(filepath.Join(w.Path, ".git"))
	if err != nil || !metadata.Mode().IsRegular() {
		return result, errors.Join(err, errors.New("worktree Git metadata is not a regular file; preserving worktree"))
	}
	gitFile, err := os.ReadFile(filepath.Join(w.Path, ".git"))
	if err != nil || string(gitFile) != w.gitFile {
		return result, errors.Join(err, errors.New("worktree Git identity changed; preserving worktree"))
	}
	childIgnored, err := worktreeIgnoredDirs(ctx, w.Path, w.gitPin())
	if err != nil {
		return result, err
	}
	w.exclusions.addIgnored(childIgnored)
	baseline := w.exclusions.filter(w.baseline)
	currentPath, err := os.MkdirTemp(w.scratch, "inspection-")
	if err != nil {
		return result, err
	}
	current, _, err := snapshotWorktree(ctx, w.Path, currentPath, w.exclusions)
	if err != nil {
		return result, err
	}
	state, err := w.gitState(ctx)
	if err != nil {
		return result, err
	}
	result.Changed = !equalWorktreeEntries(baseline, current) || state != w.baselineGit
	if result.Changed {
		tree, treeErr := w.snapshotTree(ctx, currentPath)
		if treeErr != nil {
			return result, treeErr
		}
		patch, patchErr := worktreeGit(ctx, w.scratch, []string{"--git-dir=" + filepath.Join(w.scratch, "snapshot.git")}, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", w.baselineTree, tree, "--")
		if patchErr != nil {
			return result, patchErr
		}
		artifact, artifactErr := os.CreateTemp(w.scratch, "changes-*.patch")
		if artifactErr != nil {
			return result, artifactErr
		}
		_, writeErr := artifact.Write(patch)
		closeErr := artifact.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return result, err
		}
		result.PatchPath = artifact.Name()
		indexPatch, indexErr := w.git(ctx, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", w.head, "--")
		if indexErr != nil {
			return result, indexErr
		}
		if len(indexPatch) != 0 {
			indexArtifact, indexErr := os.CreateTemp(w.scratch, "index-*.patch")
			if indexErr != nil {
				return result, indexErr
			}
			_, writeErr := indexArtifact.Write(indexPatch)
			if err := errors.Join(writeErr, indexArtifact.Close()); err != nil {
				return result, err
			}
			result.IndexPatchPath = indexArtifact.Name()
		}
		return result, nil
	}
	verify, _, err := snapshotWorktree(ctx, w.Path, "", w.exclusions)
	if err != nil || !equalWorktreeEntries(current, verify) {
		return result, errors.Join(err, errors.New("worktree changed during inspection; preserving worktree"))
	}
	verifiedState, err := w.gitState(ctx)
	if err != nil || verifiedState != state {
		return result, errors.Join(err, errors.New("worktree Git state changed during inspection; preserving worktree"))
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := fsext.MakeDirsWritable(w.Path); err != nil {
		return result, fmt.Errorf("make unchanged worktree removable: %w", err)
	}
	pin := []string{"--git-dir=" + w.gitDir, "--work-tree=" + w.SourceRoot}
	if _, err = worktreeGit(ctx, w.SourceRoot, pin, "worktree", "remove", "--force", w.Path); err != nil {
		return result, fmt.Errorf("remove unchanged worktree: %w", err)
	}
	w.removed = true
	result.Preserved, result.Removed = false, true
	if _, err = worktreeGit(ctx, w.SourceRoot, pin, "update-ref", "-d", "refs/heads/"+w.Branch, w.head); err != nil {
		return result, fmt.Errorf("remove unchanged branch: %w", err)
	}
	return result, fsext.RemoveAllWritable(w.scratch)
}

func (w *AgentWorktree) gitPin() []string {
	return []string{"--git-dir=" + filepath.Join(w.Path, ".git"), "--work-tree=" + w.Path}
}

func (w *AgentWorktree) git(ctx context.Context, args ...string) ([]byte, error) {
	return worktreeGit(ctx, w.Path, w.gitPin(), args...)
}

func (w *AgentWorktree) gitState(ctx context.Context) (string, error) {
	var state strings.Builder
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"symbolic-ref", "HEAD"}, {"diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", w.head, "--"}} {
		out, err := w.git(ctx, args...)
		if err != nil {
			return "", err
		}
		state.Write(out)
		state.WriteByte(0)
	}
	return state.String(), nil
}

func (w *AgentWorktree) snapshotTree(ctx context.Context, path string) (string, error) {
	pin := []string{"--git-dir=" + filepath.Join(w.scratch, "snapshot.git"), "--work-tree=" + path}
	if _, err := worktreeGit(ctx, path, pin, "read-tree", "--empty"); err != nil {
		return "", err
	}
	entries, _, err := snapshotWorktree(ctx, path, "", w.exclusions)
	if err != nil {
		return "", err
	}
	var index bytes.Buffer
	var paths bytes.Buffer
	var files []string
	for name, entry := range entries {
		if entry.Mode.IsDir() {
			continue
		}
		if entry.Mode&os.ModeSymlink != 0 {
			object, err := worktreeGitInput(ctx, path, pin, []byte(entry.Link), "hash-object", "-w", "--no-filters", "--stdin")
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&index, "120000 %s\t%s\x00", strings.TrimSpace(string(object)), name)
			continue
		}
		files = append(files, name)
		paths.WriteString(quoteStdinPath(name))
		paths.WriteByte('\n')
	}
	if len(files) > 0 {
		out, err := worktreeGitInput(ctx, path, pin, paths.Bytes(), "hash-object", "-w", "--no-filters", "--stdin-paths")
		if err != nil {
			return "", err
		}
		objects := strings.Fields(string(out))
		if len(objects) != len(files) {
			return "", fmt.Errorf("git hash-object returned %d objects for %d files", len(objects), len(files))
		}
		for i, name := range files {
			mode := "100644"
			if entries[name].Mode&0o111 != 0 {
				mode = "100755"
			}
			fmt.Fprintf(&index, "%s %s\t%s\x00", mode, objects[i], name)
		}
	}
	if _, err := worktreeGitInput(ctx, path, pin, index.Bytes(), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	out, err := worktreeGit(ctx, path, pin, "write-tree")
	return strings.TrimSpace(string(out)), err
}

func worktreeGit(ctx context.Context, root string, pin []string, args ...string) ([]byte, error) {
	return worktreeGitInput(ctx, root, pin, nil, args...)
}

func quoteStdinPath(name string) string {
	if !strings.ContainsAny(name, "\"\\\n\r\t") {
		return name
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := range len(name) {
		switch c := name[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func worktreeGitInput(ctx context.Context, root string, pin []string, input []byte, args ...string) ([]byte, error) {
	all := append([]string{"-C", root, "-c", "core.hooksPath=", "-c", "core.fsmonitor=false"}, pin...)
	all = append(all, args...)
	cmd := exec.CommandContext(ctx, "git", all...)
	cmd.Dir = root
	cmd.Stdin = bytes.NewReader(input)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), errors.Join(ctx.Err(), err), strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
