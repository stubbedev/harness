package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/filepathext"
)

// fabricatedPathLimit caps how many missing paths one warning lists; a
// report that cites more is unreliable as a whole, and an unbounded list
// would only bloat the orchestrator's context.
const fabricatedPathLimit = 10

const citationDelimiters = "`\"',;()[]{}<>|\u201c\u201d\u2018\u2019\u2014\u2013"

var citationAnchor = regexp.MustCompile(`(?::\d+(?:[-,]\d+)*)+$|#L\d+(?:-L?\d+)?$`)

// warnOnFabricatedPaths scans a sub-agent's final report for cited
// workspace paths that do not exist and appends a warning to the response
// the orchestrating agent receives. Small models occasionally invent
// plausible packages, files, and file:line references; the dispatcher is
// the last line of defense before an orchestrator treats the report as
// fact or edits against an invented path. Fetch research (the research
// tool) is exempt: it runs in a temporary directory and cites URLs and
// saved-page paths, not workspace files. An isolated agent's checkout is
// searched alongside the workspace.
func (c *coordinator) warnOnFabricatedPaths(params subAgentParams, resp *fantasy.ToolResponse) {
	if resp == nil || resp.IsError || params.AgentName == tools.ResearchToolName {
		return
	}
	roots := []string{c.cfg.WorkingDir()}
	if params.WorkspaceRoot != "" {
		roots = append(roots, params.WorkspaceRoot)
	}
	missing := missingCitedPaths(resp.Content, roots)
	if len(missing) == 0 {
		return
	}
	slog.Warn(
		"Subagent cited paths missing from the working tree",
		"subagent", params.AgentName,
		"missing", strings.Join(missing, ", "),
	)
	var b strings.Builder
	b.WriteString("\n\n## Warning: paths cited by this sub-agent do not exist\n")
	for _, p := range missing {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	b.WriteString("\nThe working tree contains none of these. Parts of the report above may be fabricated; verify with ls or glob before relying on, or editing, anything it named.\n")
	resp.Content += b.String()
}

// missingCitedPaths extracts file-path-shaped tokens from report and
// returns the ones that resolve to nothing under any of roots. A token
// counts as a citation when it contains a path separator and looks like a
// file (an extension such as .go), an explicit directory (trailing
// separator) or a glob; bare prose like "read/write" or "fast/task" never
// matches. Only checkable citations are flagged: an absolute path inside
// a root, or a relative path whose first element exists in a root. Paths
// relative to a package directory, fixture names mentioned as data and
// paths outside every root are never reported: a missed warning costs
// less than a false one, and nothing outside the roots is ever probed.
func missingCitedPaths(report string, roots []string) []string {
	var missing []string
	seen := make(map[string]bool)
	for _, raw := range strings.FieldsFunc(report, isCitationDelimiter) {
		token := trimCitation(raw)
		if token == "" || strings.Contains(token, "://") || !strings.ContainsFunc(token, isCitationSeparator) {
			continue
		}
		trimmed := strings.TrimRightFunc(token, isCitationSeparator)
		dirCited := trimmed != token
		token = trimmed
		segment := token[strings.LastIndexFunc(token, isCitationSeparator)+1:]
		glob := strings.ContainsAny(token, "*?")
		if !glob && !dirCited && !hasFileExtension(segment) {
			continue
		}
		if seen[token] {
			continue
		}
		seen[token] = true
		rel, checkable := citationPath(roots, token)
		if !checkable || existsInAnyRoot(roots, rel, glob) {
			continue
		}
		missing = append(missing, token)
		if len(missing) >= fabricatedPathLimit {
			break
		}
	}
	return missing
}

func isCitationDelimiter(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(citationDelimiters, r)
}

// isCitationSeparator reports whether r separates path elements in a
// citation: the slash every report writes, or the platform's own
// separator, which absolute paths carry on Windows.
func isCitationSeparator(r rune) bool {
	return r == '/' || r == filepath.Separator
}

func trimCitation(token string) string {
	for {
		before := token
		token = strings.Trim(token, "*~")
		if len(token) > 1 && token[0] == '_' && token[len(token)-1] == '_' {
			token = token[1 : len(token)-1]
		}
		token = strings.TrimRight(token, ".:!?")
		token = citationAnchor.ReplaceAllString(token, "")
		if token == before {
			return token
		}
	}
}

func citationPath(roots []string, token string) (string, bool) {
	if filepath.IsAbs(token) {
		target := filepath.Clean(token)
		for _, root := range roots {
			if rel, ok := filepathext.RelWithin(root, target); ok {
				return rel, true
			}
		}
		return "", false
	}
	rel := filepath.Clean(filepath.FromSlash(token))
	if !filepath.IsLocal(rel) {
		return "", false
	}
	first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	for _, root := range roots {
		if _, err := os.Lstat(filepath.Join(root, first)); err == nil {
			return rel, true
		}
	}
	return "", false
}

func existsInAnyRoot(roots []string, rel string, glob bool) bool {
	for _, root := range roots {
		target := filepath.Join(root, rel)
		if glob {
			if matches, err := filepath.Glob(target); err == nil && len(matches) > 0 {
				return true
			}
			continue
		}
		if _, err := os.Stat(target); err == nil {
			return true
		}
	}
	return false
}

// hasFileExtension reports whether name carries a short alphanumeric
// extension, the shape of a source-file citation (foo.go, NOTES.md).
func hasFileExtension(name string) bool {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || dot == len(name)-1 {
		return false
	}
	ext := name[dot+1:]
	if len(ext) > 8 {
		return false
	}
	for _, r := range ext {
		isAlnum := 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9'
		if !isAlnum {
			return false
		}
	}
	return true
}
