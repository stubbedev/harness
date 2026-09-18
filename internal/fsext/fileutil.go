package fsext

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/charlievieth/fastwalk"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/home"
)

type FileInfo struct {
	Path    string
	ModTime time.Time
}

// commonIgnoredDirs are the directory names every crawling walker
// skips: VCS metadata, harness's own data directory, dependency
// stores, and build output. Single source for SkipHidden's walk and
// the stats crawl; callers layer their own extras on top (the stats
// crawl also skips .svn, .hg, .cache, .npm, .cargo, Library,
// Applications, and System).
var commonIgnoredDirs = []string{
	".harness",
	".git",
	"node_modules",
	"vendor",
	"dist",
	"build",
	"target",
	".idea",
	".vscode",
	"__pycache__",
	"bin",
	"obj",
	"out",
	"coverage",
	"logs",
	"generated",
	"bower_components",
	"jspm_packages",
}

// IsCommonIgnoredDir reports whether name is a directory every
// crawling walker skips.
func IsCommonIgnoredDir(name string) bool {
	return slices.Contains(commonIgnoredDirs, name)
}

func SkipHidden(path string) bool {
	// Check for hidden files (starting with a dot)
	base := filepath.Base(path)
	if base != "." && strings.HasPrefix(base, ".") {
		return true
	}

	parts := strings.SplitSeq(path, string(os.PathSeparator))
	for part := range parts {
		if IsCommonIgnoredDir(part) {
			return true
		}
	}
	return false
}

// FastGlobWalker provides gitignore-aware file walking with fastwalk
// It uses hierarchical ignore checking like git does, checking .gitignore/.harnessignore
// files in each directory from the root to the target path.
type FastGlobWalker struct {
	directoryLister *directoryLister
}

func NewFastGlobWalker(searchPath string) *FastGlobWalker {
	return &FastGlobWalker{
		directoryLister: NewDirectoryLister(searchPath),
	}
}

// ShouldSkip checks if a file path should be skipped based on hierarchical gitignore,
// harnessignore, and hidden file rules.
func (w *FastGlobWalker) ShouldSkip(path string) bool {
	return w.directoryLister.shouldIgnore(path, nil, false)
}

// ShouldSkipDir checks if a directory path should be skipped based on hierarchical
// gitignore, harnessignore, and hidden file rules.
func (w *FastGlobWalker) ShouldSkipDir(path string) bool {
	return w.directoryLister.shouldIgnore(path, nil, true)
}

// Glob globs files.
//
// Does not respect gitignore.
func Glob(pattern string, cwd string, limit int) ([]string, bool, error) {
	return globWithDoubleStar(context.Background(), pattern, cwd, limit, false)
}

// GlobGitignoreAware globs files respecting gitignore.
func GlobGitignoreAware(pattern string, cwd string, limit int) ([]string, bool, error) {
	return globWithDoubleStar(context.Background(), pattern, cwd, limit, true)
}

// GlobGitignoreAwareCtx is like [GlobGitignoreAware] but stops early when ctx
// is cancelled (e.g. on timeout), returning whatever was found so far.
func GlobGitignoreAwareCtx(ctx context.Context, pattern, cwd string, limit int) ([]string, bool, error) {
	return globWithDoubleStar(ctx, pattern, cwd, limit, true)
}

func globWithDoubleStar(ctx context.Context, pattern, searchPath string, limit int, gitignore bool) ([]string, bool, error) {
	// Normalize pattern to forward slashes on Windows so their config can use
	// backslashes
	pattern = filepath.ToSlash(pattern)

	walker := NewFastGlobWalker(searchPath)
	found := csync.NewSlice[FileInfo]()
	conf := fastwalk.Config{
		// Do not follow symlinks: following them lets the walk escape the
		// search root (into module caches, the nix store, $HOME, etc.) and
		// chase cycles, which is slow and can hang. Mirrors the rg path,
		// which no longer passes -L.
		Follow:  false,
		ToSlash: fastwalk.DefaultToSlash(),
		Sort:    fastwalk.SortFilesFirst,
	}
	err := fastwalk.Walk(&conf, searchPath, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll // Timed out or cancelled; stop walking.
		}
		if err != nil {
			return nil // Skip files we can't access
		}

		isDir := d.IsDir()
		if isDir {
			if gitignore && walker.ShouldSkipDir(path) {
				return filepath.SkipDir
			}
		} else {
			if gitignore && walker.ShouldSkip(path) {
				return nil
			}
		}

		relPath, err := filepath.Rel(searchPath, path)
		if err != nil {
			relPath = path
		}

		// Normalize separators to forward slashes
		relPath = filepath.ToSlash(relPath)

		// Check if path matches the pattern
		matched, err := doublestar.Match(pattern, relPath)
		if err != nil || !matched {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		found.Append(FileInfo{Path: path, ModTime: info.ModTime()})
		if limit > 0 && found.Len() >= limit*2 { // NOTE: why x2?
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return nil, false, fmt.Errorf("fastwalk error: %w", err)
	}

	matches := slices.SortedFunc(found.Seq(), func(a, b FileInfo) int {
		return b.ModTime.Compare(a.ModTime)
	})
	matches, truncated := truncate(matches, limit)

	results := make([]string, len(matches))
	for i, m := range matches {
		results[i] = m.Path
	}
	return results, truncated || errors.Is(err, filepath.SkipAll), nil
}

// ShouldExcludeFile checks if a file should be excluded from processing
// based on common patterns and ignore rules.
func ShouldExcludeFile(rootPath, filePath string) bool {
	info, err := os.Stat(filePath)
	isDir := err == nil && info.IsDir()
	return NewDirectoryLister(rootPath).
		shouldIgnore(filePath, nil, isDir)
}

func PrettyPath(path string) string {
	return home.Short(path)
}

func DirTrim(pwd string, lim int) string {
	var (
		out string
		sep = string(filepath.Separator)
	)
	dirs := strings.Split(pwd, sep)
	if lim > len(dirs)-1 || lim <= 0 {
		return pwd
	}
	for i := len(dirs) - 1; i > 0; i-- {
		out = sep + out
		if i == len(dirs)-1 {
			out = dirs[i]
		} else if i >= len(dirs)-lim {
			// Keep the first grapheme cluster, not the first byte: CJK,
			// combining marks, and emoji can span multiple bytes and runes,
			// so a byte or single rune would render the wrong character.
			first, _ := ansi.FirstGraphemeCluster(dirs[i], ansi.GraphemeWidth)
			out = first + out
		} else {
			out = "..." + out
			break
		}
	}
	out = filepath.Join("~", out)
	return out
}

// PathOrPrefix returns the prefix if the path starts with it, or falls back to
// the path otherwise.
func PathOrPrefix(path, prefix string) string {
	if HasPrefix(path, prefix) {
		return prefix
	}
	return path
}

// HasPrefix checks if the given path starts with the specified prefix.
// Uses filepath.Rel to determine if path is within prefix.
func HasPrefix(path, prefix string) bool {
	rel, err := filepath.Rel(prefix, path)
	if err != nil {
		return false
	}
	// If path is within prefix, Rel will not return a path starting with ".."
	return !strings.HasPrefix(rel, "..")
}

// ToUnixLineEndings converts Windows line endings (CRLF) to Unix line endings (LF).
func ToUnixLineEndings(content string) (string, bool) {
	if strings.Contains(content, "\r\n") {
		return strings.ReplaceAll(content, "\r\n", "\n"), true
	}
	return content, false
}

// ToWindowsLineEndings converts Unix line endings (LF) to Windows line endings (CRLF).
func ToWindowsLineEndings(content string) (string, bool) {
	if !strings.Contains(content, "\r\n") {
		return strings.ReplaceAll(content, "\n", "\r\n"), true
	}
	return content, false
}

func truncate[T any](input []T, limit int) ([]T, bool) {
	if limit > 0 && len(input) > limit {
		return input[:limit], true
	}
	return input, false
}
