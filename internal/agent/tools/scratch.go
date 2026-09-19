package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// scratchRoot is the directory tools write working files into:
// <temp>/harness/scratchpad/<session>/<kind>. Downloads and spilled
// pages live here rather than in the user's working directory, so a run
// cannot leave stray files in a repository or get them committed by
// accident.
// HARNESS_SCRATCH_DIR overrides the root, which tests use to keep their
// files out of the real temp directory.
func scratchRoot() string {
	if override := strings.TrimSpace(os.Getenv("HARNESS_SCRATCH_DIR")); override != "" {
		return override
	}
	return filepath.Join(os.TempDir(), "harness", "scratchpad")
}

// ScratchDir returns the per-session scratch directory for a kind of
// file ("downloads", "pages"), creating it if needed.
func ScratchDir(sessionID, kind string) (string, error) {
	session := sanitizePathSegment(sessionID)
	if session == "" {
		return "", fmt.Errorf("session ID is required")
	}
	root, err := filepath.Abs(scratchRoot())
	if err != nil {
		return "", fmt.Errorf("failed to resolve scratch directory: %w", err)
	}
	dir := filepath.Join(root, session, sanitizePathSegment(kind))
	for _, path := range []string{root, filepath.Join(root, session), dir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", fmt.Errorf("failed to create scratch directory: %w", err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("failed to inspect scratch directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("scratch directory must be a directory, not a symlink: %s", path)
		}
		// Unix permission bits do not exist on Windows: mode bits there
		// reflect only the read-only attribute, so the privacy check is a
		// no-op that would reject every directory.
		if runtime.GOOS != "windows" && !privateScratchMode(info.Mode().Perm(), path == root) {
			return "", fmt.Errorf("scratch directory must be private to the owner: %s", path)
		}
	}
	return dir, nil
}

func privateScratchMode(perm os.FileMode, isRoot bool) bool {
	if perm&0o022 != 0 {
		return false
	}
	return isRoot || perm&0o077 == 0
}

// ScratchFilePath places name inside a session's scratch directory. Only
// the base name is kept: a caller-supplied "../../etc/passwd" or an
// absolute path cannot escape the scratch directory.
func ScratchFilePath(sessionID, kind, name string) (string, error) {
	dir, err := ScratchDir(sessionID, kind)
	if err != nil {
		return "", err
	}
	base := sanitizePathSegment(filepath.Base(filepath.FromSlash(name)))
	if base == "" {
		return "", fmt.Errorf("a file name is required")
	}
	return filepath.Join(dir, base), nil
}

// sanitizePathSegment reduces a string to something safe to use as one
// path segment: no separators, no traversal, no leading dots.
func sanitizePathSegment(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', 0:
			return '-'
		}
		return r
	}, s)
	s = strings.TrimLeft(s, ".")
	return strings.TrimSpace(s)
}
