package filepathext

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SmartJoin joins two paths, treating the second path as absolute if it is an
// absolute path.
func SmartJoin(one, two string) string {
	if SmartIsAbs(two) {
		return two
	}
	return filepath.Join(one, two)
}

// SmartIsAbs checks if a path is absolute, considering both OS-specific and
// Unix-style paths.
func SmartIsAbs(path string) bool {
	switch runtime.GOOS {
	case "windows":
		return filepath.IsAbs(path) || strings.HasPrefix(filepath.ToSlash(path), "/")
	default:
		return filepath.IsAbs(path)
	}
}

// SplitGlobPrefix splits a glob pattern into the longest leading run of
// literal path segments and the remaining pattern. The prefix contains no
// glob metacharacters, so callers can safely use it as a directory to start
// a walk from. For "internal/agent/*.go" it returns ("internal/agent",
// "*.go"); for "**/foo.go" it returns ("", "**/foo.go").
func SplitGlobPrefix(pattern string) (prefix, rest string) {
	pattern = filepath.ToSlash(pattern)
	segments := strings.Split(pattern, "/")
	var literal []string
	for i, seg := range segments {
		if strings.ContainsAny(seg, "*?[{\\") {
			rest = strings.Join(segments[i:], "/")
			return strings.Join(literal, "/"), rest
		}
		literal = append(literal, seg)
	}
	// Whole pattern is literal (a plain path); walk its parent and match
	// the basename so the existing match logic still applies.
	if len(literal) == 0 {
		return "", pattern
	}
	parent := strings.Join(literal[:len(literal)-1], "/")
	return parent, literal[len(literal)-1]
}

// RelWithin computes rel of path against dir and reports whether path is
// dir itself or inside it. The returned rel is the filepath.Rel result
// when ok is true; callers that need to reject the directory itself can
// still test rel == "." on it.
func RelWithin(dir, path string) (rel string, ok bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// Within reports whether path is dir itself or lexically inside it. It
// does not resolve symlinks; use [SameOrInside] when either side may
// reach the directory through one.
func Within(dir, path string) bool {
	_, ok := RelWithin(dir, path)
	return ok
}

// SameOrInside reports whether path is dir itself or inside it, after
// resolving both sides to their canonical form. The resolution matters:
// a cwd can reach the same directory through a symlink (TMPDIR on macOS)
// or a short name (RUNNER~1 on Windows) while tools report the canonical
// path, and the raw comparison would misread repo-local paths as
// outside.
func SameOrInside(path, dir string) bool {
	path = Canonicalize(path)
	dir = Canonicalize(dir)
	if runtime.GOOS == "windows" {
		// Drive letters and 8.3 names differ in case alone.
		path = strings.ToLower(path)
		dir = strings.ToLower(dir)
	}
	_, ok := RelWithin(dir, path)
	return ok
}

// Canonicalize resolves symlinks in the longest existing prefix of path.
// The final elements usually do not exist yet (they are a worktree being
// created), which would make a plain EvalSymlinks fail and leave
// symlinked cwds unresolvable.
func Canonicalize(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}
	dir, rest := filepath.Split(path)
	if dir == path {
		return path
	}
	if rest == "" {
		return Canonicalize(filepath.Clean(dir))
	}
	return filepath.Join(Canonicalize(filepath.Clean(dir)), rest)
}

// Resolve is the strict form of [Canonicalize]: it resolves symlinks in
// the longest existing prefix of path, walking up only past elements
// that do not exist, and reports any other resolution failure (a
// permission error, a symlink loop) instead of guessing.
func Resolve(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return resolved, err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = Resolve(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}
