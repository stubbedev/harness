package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools"
)

// fabricatedPathLimit caps how many missing paths one warning lists; a
// report that cites more is unreliable as a whole, and an unbounded list
// would only bloat the orchestrator's context.
const fabricatedPathLimit = 10

// warnOnFabricatedPaths scans a sub-agent's final report for cited
// workspace paths that do not exist and appends a warning to the response
// the orchestrating agent receives. Small models occasionally invent
// plausible packages, files, and file:line references; the dispatcher is
// the last line of defense before an orchestrator treats the report as
// fact or edits against an invented path. Fetch research (the research
// tool) is exempt: it runs in a temporary directory and cites URLs and
// saved-page paths, not workspace files.
func (c *coordinator) warnOnFabricatedPaths(params subAgentParams, resp *fantasy.ToolResponse) {
	if resp == nil || resp.IsError || params.AgentName == tools.ResearchToolName {
		return
	}
	missing := missingCitedPaths(resp.Content, c.cfg.WorkingDir())
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
// returns the ones that resolve to nothing under workdir. A token counts
// as a citation when it contains a slash and looks like a file (an
// extension such as .go) or an explicit directory (trailing slash) or a
// glob; bare prose like "read/write" or "fast/task" never matches.
func missingCitedPaths(report, workdir string) []string {
	var missing []string
	seen := make(map[string]bool)
	for _, raw := range strings.FieldsFunc(report, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		token := strings.Trim(raw, "`\"'")
		token = strings.TrimLeft(token, "([{<")
		token = strings.TrimRight(token, ")]}>,.;:!?")
		token = strings.TrimRight(token, "'\"`")
		// file:line and file:line:col references keep only the file.
		if i := strings.LastIndexByte(token, ':'); i > 0 {
			if suffix := token[i+1:]; suffix != "" && isAllDigits(suffix) {
				token = token[:i]
			}
		}
		if token == "" || strings.Contains(token, "://") || !strings.Contains(token, "/") {
			continue
		}
		dirCited := strings.HasSuffix(token, "/")
		token = strings.TrimRight(token, "/")
		segment := token[strings.LastIndexByte(token, '/')+1:]
		glob := strings.ContainsAny(token, "*?[")
		if !glob && !dirCited && !hasFileExtension(segment) {
			continue
		}
		if seen[token] {
			continue
		}
		if pathExists(workdir, token, glob) {
			seen[token] = true
			continue
		}
		seen[token] = true
		missing = append(missing, token)
		if len(missing) >= fabricatedPathLimit {
			break
		}
	}
	return missing
}

// pathExists reports whether token resolves under workdir. The token
// comes from a sub-agent's free-text report, so the probe stays inside
// the workspace: relative tokens are joined onto workdir, absolute
// tokens are honored only when they already name something under it,
// and anything that resolves outside - via .. or an unrelated absolute
// path - counts as missing rather than being statted. Nothing outside
// the workspace is ever probed; an existence oracle over arbitrary
// paths would itself be a disclosure. Globs match when at least one
// file or directory matches.
func pathExists(workdir, token string, glob bool) bool {
	target := token
	if !filepath.IsAbs(target) {
		target = filepath.Join(workdir, target)
	}
	rel, err := filepath.Rel(workdir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if glob {
		matches, err := filepath.Glob(target)
		return err == nil && len(matches) > 0
	}
	_, err = os.Stat(target)
	return err == nil
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

// isAllDigits reports whether s consists only of ASCII digits.
func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
