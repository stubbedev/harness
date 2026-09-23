package fsext

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stubbedev/harness/internal/home"
)

// LookupClosestBounded searches for target starting from dir and walking
// up to stopDir, returning the closest match. The walk inspects dir, then
// each ancestor up to and including stopDir, then terminates regardless
// of whether the target was found, so matches from outside a project
// boundary (a sibling worktree, a parent project) are never adopted.
//
// If stopDir is empty, only dir itself is searched. If stopDir is not an
// ancestor of dir, the walk still terminates at the filesystem root. A
// match in $HOME is ignored, and the search does not cross ownership
// boundaries. Returns the full path and true when found.
func LookupClosestBounded(dir, stopDir, target string) (string, bool) {
	var found string

	err := traverseUpBounded(dir, stopDir, func(cwd string, owner int) error {
		fpath := filepath.Join(cwd, target)

		err := probeEnt(fpath, owner)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("error probing file %s: %w", fpath, err)
		}

		if cwd == home.Dir() {
			return filepath.SkipAll
		}

		found = fpath
		return filepath.SkipAll
	})

	return found, err == nil && found != ""
}

// LookupBounded returns the full path of every target found in dir and
// each ancestor up to and including stopDir. If stopDir is empty, only
// dir itself is searched. Files owned by someone other than dir's owner
// are skipped without error.
func LookupBounded(dir, stopDir string, targets ...string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}

	var found []string

	err := traverseUpBounded(dir, stopDir, func(cwd string, owner int) error {
		for _, target := range targets {
			fpath := filepath.Join(cwd, target)
			err := probeEnt(fpath, owner)

			// skip to the next file on permission denied
			if errors.Is(err, os.ErrNotExist) ||
				errors.Is(err, os.ErrPermission) {
				continue
			}

			if err != nil {
				return fmt.Errorf("error probing file %s: %w", fpath, err)
			}

			found = append(found, fpath)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

// traverseUpBounded walks up from dir, visiting each ancestor up to and
// including stopDir, then terminates. If stopDir is empty, only dir
// itself is visited. It passes the absolute path of each directory and
// the starting directory's owner ID to walkFn, which checks ownership
// itself. If stopDir is set but is not an ancestor of dir
// the walk still stops at the filesystem root, so callers cannot
// accidentally produce an infinite walk by passing a sibling path.
//
// Boundary comparison is performed against symlink-resolved paths so
// that callers passing logically equivalent paths (a symlinked /var vs
// the underlying /private/var, for example) still terminate at the
// expected directory.
func traverseUpBounded(dir, stopDir string, walkFn func(dir string, owner int) error) error {
	cwd, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("cannot convert CWD to absolute path: %w", err)
	}

	stop := cwd
	if stopDir != "" {
		stop, err = filepath.Abs(stopDir)
		if err != nil {
			return fmt.Errorf("cannot convert stop dir to absolute path: %w", err)
		}
	}
	canonStop := canonicalize(stop)

	owner, err := Owner(dir)
	if err != nil {
		return fmt.Errorf("cannot get ownership: %w", err)
	}

	for {
		err := walkFn(cwd, owner)
		if err == nil || errors.Is(err, filepath.SkipDir) {
			if canonicalize(cwd) == canonStop {
				return nil
			}

			parent := filepath.Dir(cwd)
			if parent == cwd {
				return nil
			}

			cwd = parent
			continue
		}

		if errors.Is(err, filepath.SkipAll) {
			return nil
		}

		return err
	}
}

// ResolveConfigPath expands a config-supplied path: home-directory
// references first, then $VAR references through resolver when the
// result still starts with "$". Best effort: a $VAR that the resolver
// cannot resolve is returned as-is. Shared by the skill and subagent
// discovery configs.
func ResolveConfigPath(path string, resolver func(string) (string, error)) string {
	expanded := home.Long(path)
	if strings.HasPrefix(expanded, "$") && resolver != nil {
		if resolved, err := resolver(expanded); err == nil {
			return resolved
		}
	}
	return expanded
}

// ResolveConfigPaths applies ResolveConfigPath to each of paths. It
// returns nil for an empty list.
func ResolveConfigPaths(paths []string, resolver func(string) (string, error)) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, ResolveConfigPath(path, resolver))
	}
	return out
}

// Canonicalize returns the absolute form of path with symlinks
// resolved. Resolution is best effort: when EvalSymlinks fails
// (typically a non-existent path) the absolute path is returned as-is,
// so callers can still perform stable equality checks and derive names
// from the result. Abs failures propagate. Single source for the
// workspace directory-name and dedup-key derivations.
func Canonicalize(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// canonicalize resolves any symbolic links in path. If resolution fails
// (typically because path does not exist yet) the original path is
// returned cleaned, so callers can still perform stable equality checks.
func canonicalize(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// probeEnt checks if entity at given path exists and belongs to given owner
func probeEnt(fspath string, owner int) error {
	_, err := os.Stat(fspath)
	if err != nil {
		return fmt.Errorf("cannot stat %s: %w", fspath, err)
	}

	// special case for ownership check bypass
	if owner == -1 {
		return nil
	}

	fowner, err := Owner(fspath)
	if err != nil {
		return fmt.Errorf("cannot get ownership for %s: %w", fspath, err)
	}

	if fowner != owner {
		return os.ErrPermission
	}

	return nil
}
