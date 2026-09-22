package model

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

const (
	// gitBranchGlyph is the powerline branch symbol, matching the one
	// starship renders for git branches.
	gitBranchGlyph = "\ue0a0"
	// gitInfoTimeout bounds each git invocation so a wedged repo (e.g. a
	// hanging hook or stale NFS mount) cannot pile up processes.
	gitInfoTimeout = 2 * time.Second
)

// gitSummary is the structured git segment: branch, working-tree
// counts and ahead/behind, each rendered in its own color.
type gitSummary struct {
	branch string // branch name or short SHA
	dirty  string // "=2 !3 +1" style counts, empty when clean
	remote string // "⇡2" / "⇣5" / "⇕", empty when in sync
}

// gitSegment renders the git segment of the compact status line for the
// workspace's working directory, honoring the git_status option: branch in
// the theme's primary color, working-tree counts in the warning color,
// ahead/behind markers in the info color. Empty when disabled or outside a
// repository. Single source for every surface that shows git state; the
// status line draws it in every state, so the branch shows from the first
// frame of a session.
func gitSegment(com *common.Common) string {
	var tuiOpts *config.TUIOptions
	if cfg := com.Config(); cfg != nil && cfg.Options != nil {
		tuiOpts = cfg.Options.TUI
	}
	if !tuiOpts.ShowGitStatus() {
		return ""
	}
	// The segment reads a cache the git watcher refreshes in the
	// background, so this never blocks on a subprocess.
	return gitHeaderParts(com.Styles, com.Workspace.WorkingDir())
}

// gitHeaderParts renders the git segment of the compact status header,
// starship-style: branch in the theme's primary color, working-tree
// counts in the warning color, ahead/behind markers in the info color.
func gitHeaderParts(t *styles.Styles, dir string) string {
	info := gitStatusInfo(dir)
	if info == "" {
		return ""
	}
	sum := parseGitSummary(info)
	if sum.branch == "" {
		return ""
	}
	segment := t.Header.GitBranch.Render(gitBranchGlyph + " " + sum.branch)
	var counts []string
	if sum.dirty != "" {
		counts = append(counts, t.Header.GitStatus.Render(sum.dirty))
	}
	if sum.remote != "" {
		counts = append(counts, t.Header.GitRemote.Render(sum.remote))
	}
	if len(counts) > 0 {
		segment += " " + t.Header.GitStatus.Render("[") + strings.Join(counts, " ") + t.Header.GitStatus.Render("]")
	}
	return segment
}

// gitStatusInfo returns the cached status for dir, filled by the git
// watcher (see gitwatch.go): filesystem events in the git directory,
// agent tool results and a slow backstop ticker refresh it in the
// background, and each directory has its own cache entry so a workspace
// switch can never show the previous workspace's summary. The draw path
// never runs git; the first frames of a session may render without git
// state until the initial refresh lands.
func gitStatusInfo(dir string) string {
	return gitWatch.status(dir)
}

// collectGitInfo shells out to git exactly once - `git status` with the
// branch header carries everything the segment shows - and formats the
// result. A detached HEAD costs one extra rev-parse for the short SHA.
// Outside a repo, or when git is missing, it returns "" and the header
// drops the segment.
func collectGitInfo(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitInfoTimeout)
	defer cancel()

	status, err := gitOutput(ctx, dir, "status", "--porcelain=v1", "--branch")
	if err != nil {
		return ""
	}
	branch := branchFromPorcelain(status)
	if branch == "" {
		return ""
	}
	if branch == "HEAD" {
		// Detached HEAD: show the short SHA instead of the literal
		// branch name.
		if sha, shaErr := gitOutput(ctx, dir, "rev-parse", "--short", "HEAD"); shaErr == nil && sha != "" {
			branch = sha
		}
	}
	return formatGitStatus(branch, status)
}

// branchFromPorcelain extracts the branch name from the "## " header
// line of `git status --porcelain=v1 --branch`: "main" from
// "## main...origin/main", "main" from an unborn branch's "## No
// commits yet on main", and "HEAD" when detached.
func branchFromPorcelain(status string) string {
	for line := range strings.SplitSeq(status, "\n") {
		if rest, ok := strings.CutPrefix(line, "## "); ok {
			// The upstream is separated by a literal "...", which a
			// branch name cannot contain (".." is invalid in refs),
			// so dotted names like release/1.0 survive the cut.
			if i := strings.Index(rest, "..."); i >= 0 {
				rest = rest[:i]
			}
			rest = strings.TrimSpace(rest)
			rest = strings.TrimPrefix(rest, "No commits yet on ")
			return strings.TrimSuffix(rest, " (no branch)")
		}
	}
	return ""
}

// parseGitSummary splits a cached "<glyph> branch [counts]" string back
// into its structured parts so the header can color each differently.
func parseGitSummary(info string) gitSummary {
	var s gitSummary
	info = strings.TrimSpace(strings.TrimPrefix(info, gitBranchGlyph))
	if i := strings.IndexByte(info, '['); i >= 0 {
		counts := strings.TrimSuffix(info[i+1:], "]")
		info = strings.TrimSpace(info[:i])
		for part := range strings.FieldsSeq(counts) {
			switch {
			case strings.HasPrefix(part, "⇡"), strings.HasPrefix(part, "⇣"), strings.HasPrefix(part, "⇕"):
				s.remote = strings.TrimSpace(s.remote + " " + part)
			default:
				s.dirty = strings.TrimSpace(s.dirty + " " + part)
			}
		}
	}
	s.branch = strings.TrimSpace(info)
	return s
}

// formatGitStatus renders "<glyph> <branch> [<counts>]" from a branch
// name and `git status --porcelain=v1 --branch` output. The counts use
// starship's default symbols: conflicted (=), untracked (?), modified
// (!), staged (+), renamed (»), deleted (✘), and ahead/behind (⇡/⇣).
func formatGitStatus(branch, status string) string {
	result := gitBranchGlyph + " " + branch

	var ahead, behind int
	var conflicted, untracked, modified, staged, renamed, deleted int
	for line := range strings.SplitSeq(status, "\n") {
		if strings.HasPrefix(line, "##") {
			if i := strings.IndexByte(line, '['); i >= 0 {
				if j := strings.IndexByte(line[i:], ']'); j > 0 {
					for part := range strings.SplitSeq(line[i+1:i+j], ",") {
						if v, ok := strings.CutPrefix(strings.TrimSpace(part), "ahead "); ok {
							ahead, _ = strconv.Atoi(v)
						} else if v, ok := strings.CutPrefix(strings.TrimSpace(part), "behind "); ok {
							behind, _ = strconv.Atoi(v)
						}
					}
				}
			}
			continue
		}
		if len(line) < 2 {
			continue
		}
		x, y := line[0], line[1]
		if x == '?' && y == '?' {
			untracked++
			continue
		}
		if x == 'U' || y == 'U' || x == y && (x == 'A' || x == 'D') {
			conflicted++
			continue
		}
		if x != ' ' {
			if x == 'R' || x == 'C' {
				renamed++
			} else {
				staged++
			}
		}
		if y == 'D' {
			deleted++
		} else if y != ' ' {
			modified++
		}
	}

	var segs []string
	add := func(n int, sym string) {
		if n > 0 {
			segs = append(segs, fmt.Sprintf("%s%d", sym, n))
		}
	}
	add(conflicted, "=")
	add(untracked, "?")
	add(modified, "!")
	add(staged, "+")
	add(renamed, "»")
	add(deleted, "✘")
	switch {
	case ahead > 0 && behind > 0:
		segs = append(segs, "⇕")
	case ahead > 0:
		add(ahead, "⇡")
	case behind > 0:
		add(behind, "⇣")
	}
	if len(segs) > 0 {
		result += " [" + strings.Join(segs, " ") + "]"
	}
	return result
}

// gitOutput runs git in dir and returns its trimmed stdout.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
