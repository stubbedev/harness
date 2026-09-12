package tools

import (
	"fmt"
	"os"
	"path/filepath"
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
	dir := filepath.Join(scratchRoot(), session, sanitizePathSegment(kind))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create scratch directory: %w", err)
	}
	return dir, nil
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
