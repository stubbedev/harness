package config

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/stubbedev/harness/internal/fsext"
	"github.com/zeebo/xxh3"
)

// workspacesDirName is the directory under the global data root that
// holds one machine-owned data directory per workspace.
const workspacesDirName = "workspaces"

// catalogDirName is the directory under the global data root that
// holds the shared model catalog cache.
const catalogDirName = "catalog"

// GlobalCatalogDir returns the directory holding the provider and model
// catalog cache. The catalog describes the outside world, not a
// project, so it is shared by every workspace on the machine: one
// download feeds all of them, and `harness update-providers` refreshes
// the same store a session reads.
func GlobalCatalogDir() string {
	return filepath.Join(filepath.Dir(GlobalConfigData()), catalogDirName)
}

// DefaultWorkspaceDataDirectory returns the machine-owned data
// directory for the workspace rooted at workingDir: a directory under
// the global data root, keyed by the workspace's project boundary (the
// git worktree root, or workingDir itself outside a repository). The
// SQLite database, state.yaml, logs, exports and checkpoints all live
// there, so nothing harness-owned is written inside the user's project.
// Subdirectories of one repository share a single workspace directory,
// matching the sharing the legacy in-repo .harness discovery provided.
func DefaultWorkspaceDataDirectory(workingDir string) string {
	root := filepath.Dir(GlobalConfigData())
	return filepath.Join(root, workspacesDirName, workspaceDirName(projectBoundary(workingDir)))
}

// workspaceDirName derives a deterministic, filesystem-safe directory
// name for a workspace root: a hash of its canonical absolute path,
// suffixed for debuggability with a slug of its base name.
func workspaceDirName(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	h := xxh3.New()
	h.WriteString(filepath.ToSlash(abs))
	return fmt.Sprintf("%x-%s", h.Sum(nil), workspaceSlug(filepath.Base(abs)))
}

// workspaceSlug flattens a directory base name into a lowercase,
// filesystem-safe slug. Falls back to "workspace" when nothing usable
// remains (e.g. the filesystem root).
func workspaceSlug(base string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-.")
	if len(slug) > 24 {
		slug = slug[:24]
	}
	if slug == "" {
		return "workspace"
	}
	return slug
}

// migrateLegacyDataDir copies a legacy in-repo data directory (the
// ".harness" layout this package used before workspace data moved under
// the global data root) into dir, the workspace's current data
// directory. It runs at most once per workspace: only when the new
// directory does not exist or is empty, and only when a legacy
// directory is present. The legacy directory itself is left untouched
// so the user can delete it, or keep it as a backup, by hand.
//
// Two harness processes racing the migration copy identical bytes from
// the same source, so the worst case is a torn copy that the next
// launch overwrites; a live SQLite writer in the legacy directory can
// produce a torn database copy, which is logged rather than fatal.
func migrateLegacyDataDir(workingDir, dir string) {
	if dir == "" {
		return
	}
	legacy, ok := fsext.LookupClosestBounded(workingDir, projectBoundary(workingDir), defaultDataDirectory)
	if !ok {
		legacy = filepath.Join(workingDir, defaultDataDirectory)
	}
	entries, err := os.ReadDir(legacy)
	if err != nil || len(entries) == 0 {
		return
	}
	if existing, err := os.ReadDir(dir); err == nil && len(existing) > 0 {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("Failed to create workspace data directory", "dir", dir, "error", err)
		return
	}
	copied := 0
	for _, entry := range entries {
		if entry.Name() == dataDirLockFile {
			continue
		}
		if err := copyPath(filepath.Join(legacy, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			slog.Warn("Failed to migrate legacy workspace data", "from", filepath.Join(legacy, entry.Name()), "error", err)
			continue
		}
		copied++
	}
	slog.Info("Migrated legacy in-repo data directory", "from", legacy, "to", dir, "items", copied)
}

// dataDirLockFile mirrors the lock file name db.Connect creates inside
// a data directory; a live lock is never worth copying.
const dataDirLockFile = "harness.lock"

// copyPath copies the file or tree at src to dst, best effort.
func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		return os.WriteFile(dst, data, mode)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		return os.WriteFile(target, data, mode)
	})
}
