package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/fsext"
)

// regexCache provides thread-safe caching of compiled regex patterns
type regexCache struct {
	*csync.Map[string, *regexp.Regexp]
}

// newRegexCache creates a new regex cache
func newRegexCache() *regexCache {
	return &regexCache{
		Map: csync.NewMap[string, *regexp.Regexp](),
	}
}

// get retrieves a compiled regex from cache or compiles and caches it
func (rc *regexCache) get(pattern string) (*regexp.Regexp, error) {
	re, ok := rc.Get(pattern)
	if ok && re != nil {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	rc.Set(pattern, re)
	return re, nil
}

// ResetCache clears compiled regex caches to prevent unbounded growth across sessions.
func ResetCache() {
	searchRegexCache.Reset(map[string]*regexp.Regexp{})
	globRegexCache.Reset(map[string]*regexp.Regexp{})
}

// Global regex cache instances
var (
	searchRegexCache = newRegexCache()
	globRegexCache   = newRegexCache()
	// Pre-compiled regex for glob conversion (used frequently)
	globBraceRegex = regexp.MustCompile(`\{([^}]+)\}`)
)

type grepMatch struct {
	path     string
	modTime  time.Time
	lineNum  int
	charNum  int
	lineText string
}

func searchFiles(ctx context.Context, pattern, rootPath, include string, limit int) ([]grepMatch, bool, error) {
	matches, err := searchWithRipgrep(ctx, pattern, rootPath, include)
	if err != nil {
		matches, err = searchFilesWithRegex(pattern, rootPath, include)
		if err != nil {
			return nil, false, err
		}
	}

	// Use a stable sort so that the multiple matches a single file can
	// contribute (all sharing the same modTime) keep their original
	// line order and stay grouped together in the rendered output.
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].modTime.After(matches[j].modTime)
	})

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}

	return matches, truncated, nil
}

func searchWithRipgrep(ctx context.Context, pattern, path, include string) ([]grepMatch, error) {
	cmd := getRgSearchCmd(ctx, pattern, path, include)
	if cmd == nil {
		return nil, fmt.Errorf("ripgrep not found in $PATH")
	}

	// Only add ignore files if they exist
	for _, ignoreFile := range []string{".gitignore", ".harnessignore"} {
		ignorePath := filepath.Join(path, ignoreFile)
		if _, err := os.Stat(ignorePath); err == nil {
			cmd.Args = append(cmd.Args, "--ignore-file", ignorePath)
		}
	}

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []grepMatch{}, nil
		}
		return nil, err
	}

	var matches []grepMatch
	lastPath := ""
	var lastModTime time.Time
	lastOK := false
	for line := range bytes.SplitSeq(bytes.TrimSpace(output), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var match ripgrepMatch
		if err := json.Unmarshal(line, &match); err != nil {
			continue
		}
		if match.Type != "match" {
			continue
		}
		// rg groups its matches per file, so one stat per distinct path
		// covers the whole run instead of one per match line.
		if match.Data.Path.Text != lastPath {
			lastPath = match.Data.Path.Text
			fi, err := os.Stat(lastPath)
			lastOK = err == nil
			if lastOK {
				lastModTime = fi.ModTime()
			}
		}
		if !lastOK {
			continue // Skip files we can't access
		}
		for _, m := range match.Data.Submatches {
			matches = append(matches, grepMatch{
				path:     match.Data.Path.Text,
				modTime:  lastModTime,
				lineNum:  match.Data.LineNumber,
				charNum:  m.Start + 1, // ensure 1-based
				lineText: strings.TrimSpace(match.Data.Lines.Text),
			})
			// only get the first match of each line
			break
		}
	}
	return matches, nil
}

type ripgrepMatch struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		Submatches []struct {
			Start int `json:"start"`
		} `json:"submatches"`
	} `json:"data"`
}

func searchFilesWithRegex(pattern, rootPath, include string) ([]grepMatch, error) {
	matches := []grepMatch{}

	// Use cached regex compilation
	regex, err := searchRegexCache.get(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	var includePattern *regexp.Regexp
	if include != "" {
		regexPattern := globToRegex(include)
		includePattern, err = globRegexCache.get(regexPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid include pattern: %w", err)
		}
	}

	// Create walker with gitignore and harnessignore support
	walker := fsext.NewFastGlobWalker(rootPath)

	err = filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entries are skipped, not fatal
		}

		if entry.IsDir() {
			if walker.ShouldSkip(path, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if walker.ShouldSkip(path, false) {
			return nil
		}

		// Skip hidden files (starting with a dot) to match ripgrep's default behavior
		base := filepath.Base(path)
		if base != "." && strings.HasPrefix(base, ".") {
			return nil
		}

		if includePattern != nil && !includePattern.MatchString(path) {
			return nil
		}

		lineMatches, err := fileMatches(path, regex)
		if err != nil || len(lineMatches) == 0 {
			return nil //nolint:nilerr // unreadable files are skipped, not fatal
		}
		info, err := entry.Info()
		if err != nil {
			return nil //nolint:nilerr // a file gone since the walk listed it is skipped
		}

		for _, lm := range lineMatches {
			matches = append(matches, grepMatch{
				path:     path,
				modTime:  info.ModTime(),
				lineNum:  lm.lineNum,
				charNum:  lm.charNum,
				lineText: lm.lineText,
			})
			if len(matches) >= 200 {
				return filepath.SkipAll
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return matches, nil
}

// lineMatch is a single matching line within a file: its 1-based line
// number, the 1-based column of the first match on that line, and the
// line text (with the trailing newline stripped).
type lineMatch struct {
	lineNum  int
	charNum  int
	lineText string
}

// fileMatches returns every line in filePath that matches pattern. Like
// ripgrep, it reports one entry per matching line (using the first match
// on the line for the column) instead of stopping at the first match in
// the file.
func fileMatches(filePath string, pattern *regexp.Regexp) ([]lineMatch, error) {
	if pattern == nil {
		return nil, nil
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Only search text files. The sniff peeks at the buffered head, so
	// the file is opened and read once.
	reader := bufio.NewReader(file)
	head, err := reader.Peek(512)
	if err != nil && err != io.EOF && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, nil
	}
	if !isText(head) {
		return nil, nil
	}

	var matches []lineMatch
	lineNum := 0
	for {
		line, err := reader.ReadString('\n')
		lineNum++
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if loc := pattern.FindStringIndex(line); loc != nil {
			matches = append(matches, lineMatch{
				lineNum:  lineNum,
				charNum:  loc[0] + 1,
				lineText: line,
			})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	return matches, nil
}

// isText reports whether a file whose first bytes are head is text, by
// its sniffed MIME type.
func isText(head []byte) bool {
	contentType := http.DetectContentType(head)
	return strings.HasPrefix(contentType, "text/") ||
		contentType == "application/json" ||
		contentType == "application/xml" ||
		contentType == "application/javascript" ||
		contentType == "application/x-sh"
}

func globToRegex(glob string) string {
	regexPattern := strings.ReplaceAll(glob, ".", "\\.")
	regexPattern = strings.ReplaceAll(regexPattern, "*", ".*")
	regexPattern = strings.ReplaceAll(regexPattern, "?", ".")

	// Use pre-compiled regex instead of compiling each time
	regexPattern = globBraceRegex.ReplaceAllStringFunc(regexPattern, func(match string) string {
		inner := match[1 : len(match)-1]
		return "(" + strings.ReplaceAll(inner, ",", "|") + ")"
	})

	return regexPattern
}
