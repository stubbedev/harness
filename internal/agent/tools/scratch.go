package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/stubbedev/harness/internal/filepathext"
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
	safeKind := sanitizePathSegment(kind)
	if safeKind == "" {
		return "", fmt.Errorf("scratch kind is required")
	}
	root, err := filepath.Abs(scratchRoot())
	if err != nil {
		return "", fmt.Errorf("failed to resolve scratch directory: %w", err)
	}
	dir := filepath.Join(root, session, safeKind)
	// The segments are sanitizePathSegment outputs, so this cannot trip;
	// checking it here keeps the directory-under-root invariant a fact
	// about this function rather than an assumption about the sanitizer.
	if dir == root || !filepathext.Within(root, dir) {
		return "", fmt.Errorf("scratch directory must stay inside the scratch root: %s", dir)
	}
	for _, path := range []string{root, filepath.Join(root, session), dir} {
		// codeql[go/path-injection] path is built from separator- and
		// traversal-free segments and verified to stay inside root.
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", fmt.Errorf("failed to create scratch directory: %w", err)
		}
		// codeql[go/path-injection] path is built from separator- and
		// traversal-free segments and verified to stay inside root.
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
