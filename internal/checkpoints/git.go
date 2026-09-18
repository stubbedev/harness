package checkpoints

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stubbedev/harness/internal/filepathext"
)

// shadowRepoName is the directory, inside the workspace data
// directory's checkpoints root, holding a session's shadow git
// repository.
const shadowRepoName = "shadow.git"

// idPattern is the shape of the identifiers that reach git: session and
// message IDs, which Harness generates. They name a directory under the
// data directory and a ref in the shadow repository, so anything else --
// a path separator that would walk out of the data directory, a leading
// dash git would read as a flag -- is refused here rather than passed
// on.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// commitPattern is the shape of a git object name. Commits come back
// from git itself, and go back to it in the next command; validating
// the round trip keeps a stored value that is not one out of an
// argument list.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// checkID reports whether an identifier is safe to pass to git or to
// join into a path.
func checkID(kind, id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid %s %q", kind, id)
	}
	return nil
}

// shadowDir returns the shadow GIT_DIR for a session. One repository
// per session keeps concurrent sessions from contending for a shared
// index while blob storage still dedupes within each session.
//
// The session ID is validated by every caller that reaches git (see
// checkID), so the join cannot be walked out of the data directory.
func (s *Service) shadowDir(sessionID string) string {
	return filepath.Join(s.dataDir, "checkpoints", sessionID, shadowRepoName)
}

// commitTree snapshots the working tree into the session's shadow
// repository and returns the created commit SHA. It uses only git
// plumbing commands, which never trigger automatic gc, and parks each
// commit behind a ref so the objects stay reachable forever - or until
// the session is deleted.
func (s *Service) commitTree(ctx context.Context, sessionID, messageID string) (string, error) {
	// The message ID becomes a ref name and part of the commit message,
	// so it is checked before either is built.
	if err := checkID("message id", messageID); err != nil {
		return "", err
	}
	if err := s.ensureShadow(ctx, sessionID); err != nil {
		return "", err
	}
	addArgs := []string{"add", "-A", "--", "."}
	if exclude := s.dataDirExclude(); exclude != "" {
		addArgs = append(addArgs, exclude)
	}
	if _, err := s.git(ctx, sessionID, addArgs...); err != nil {
		return "", fmt.Errorf("staging working tree: %w", err)
	}
	treeOut, err := s.git(ctx, sessionID, "write-tree")
	if err != nil {
		return "", fmt.Errorf("writing tree: %w", err)
	}
	tree := strings.TrimSpace(treeOut)
	if tree == "" {
		return "", fmt.Errorf("git write-tree returned no tree")
	}
	commitOut, err := s.git(ctx, sessionID, "commit-tree", tree, "-m", "checkpoint "+messageID)
	if err != nil {
		return "", fmt.Errorf("committing tree: %w", err)
	}
	commit := strings.TrimSpace(commitOut)
	if commit == "" {
		return "", fmt.Errorf("git commit-tree returned no commit")
	}
	if _, err := s.git(ctx, sessionID, "update-ref", "refs/checkpoints/"+messageID, commit); err != nil {
		return "", fmt.Errorf("pinning checkpoint ref: %w", err)
	}
	return commit, nil
}

// restore makes the working tree match the given snapshot commit: it
// resets the shadow index to the commit's tree and updates the files
// on disk accordingly - modified files are reverted, files deleted
// since the snapshot come back, and files added after the snapshot
// (and tracked in a later one) are removed. Files that no snapshot
// ever tracked - ignored files, and untracked files created after the
// newest snapshot - are left alone.
func (s *Service) restore(ctx context.Context, sessionID, commit string) error {
	if !commitPattern.MatchString(commit) {
		return fmt.Errorf("invalid checkpoint commit %q", commit)
	}
	if err := s.ensureShadow(ctx, sessionID); err != nil {
		return err
	}
	if _, err := s.git(ctx, sessionID, "read-tree", "-u", "--reset", commit); err != nil {
		return fmt.Errorf("git read-tree: %w", err)
	}
	return nil
}

// ensureShadow creates the session's shadow repository on first use.
func (s *Service) ensureShadow(ctx context.Context, sessionID string) error {
	if err := checkID("session id", sessionID); err != nil {
		return err
	}
	dir := s.shadowDir(sessionID)
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "init", "--bare", dir)
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// dataDirExclude returns a git pathspec excluding the workspace data
// directory from snapshots when it lives inside the working tree (a
// legacy in-repo .harness layout), so machine-owned state is never
// snapshotted. Empty when there is nothing to exclude.
func (s *Service) dataDirExclude() string {
	if s.dataDir == "" {
		return ""
	}
	rel, ok := filepathext.RelWithin(s.workingDir, s.dataDir)
	if !ok || rel == "." {
		return ""
	}
	return ":(exclude)" + rel
}

// removeShadow deletes a session's shadow repository directory.
func removeShadow(dataDir, sessionID string) error {
	if err := checkID("session id", sessionID); err != nil {
		return err
	}
	dir := filepath.Join(dataDir, "checkpoints", sessionID)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.RemoveAll(dir)
}

// git runs a git command against the session's shadow repository over
// the service's working tree and returns its stdout.
func (s *Service) git(ctx context.Context, sessionID string, args ...string) (string, error) {
	if err := checkID("session id", sessionID); err != nil {
		return "", err
	}
	full := append([]string{
		"--git-dir", s.shadowDir(sessionID),
		"--work-tree", s.workingDir,
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = s.workingDir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

// gitEnv builds the environment for shadow git commands: the inherited
// environment minus any GIT_* variables (a user's GIT_DIR or
// GIT_INDEX_FILE must not leak into the shadow repository), plus a
// fixed identity so plumbing commands never depend on user-level git
// configuration.
func gitEnv() []string {
	const (
		authorName    = "harness"
		authorEmail   = "harness@localhost"
		committerName = "harness"
		committerMail = "harness@localhost"
	)
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+committerName,
		"GIT_COMMITTER_EMAIL="+committerMail,
	)
}
