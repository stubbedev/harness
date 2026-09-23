package fsext

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/home"
)

// commonIgnoredDirs are the directory names every crawling walker
// skips: VCS metadata, harness's own data directory, dependency
// stores, and build output. Single source for the stats crawl and any
// other crawler; callers layer their own extras on top (the stats
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

// DetectLineEndings returns the line ending a body already uses: CRLF
// when it contains any CRLF, otherwise LF. Single source for code that
// must preserve a file's existing ending style when rejoining lines
// (LSP workspace edits) or reporting it.
func DetectLineEndings(content string) string {
	if strings.Contains(content, "\r\n") {
		return "\r\n"
	}
	return "\n"
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
