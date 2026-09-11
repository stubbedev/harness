package model

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stubbedev/harness/internal/ui/styles"
)

const (
	// gitInfoTTL bounds how often the header re-polls git. The draw path
	// only ever reads a cached string, so a refresh is one background
	// subprocess pair per interval, never a per-frame exec.
	gitInfoTTL = 5 * time.Second
	// gitBranchGlyph is the powerline branch symbol, matching the one
	// starship renders for git branches.
	gitBranchGlyph = "\ue0a0"
	// gitInfoTimeout bounds each git invocation so a wedged repo (e.g. a
	// hanging hook or stale NFS mount) cannot pile up processes.
	gitInfoTimeout = 2 * time.Second
)

var (
	gitMu         sync.Mutex
	gitCache      string
	gitRefreshAt  time.Time
	gitRefreshing bool
)

// gitSummary is the structured git segment: branch, working-tree
// counts and ahead/behind, each rendered in its own color.
type gitSummary struct {
	branch string // branch name or short SHA
	dirty  string // "=2 !3 +1" style counts, empty when clean
	remote string // "⇡2" / "⇣5" / "⇕", empty when in sync
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

// gitStatusInfo returns the cached status for dir, refreshing it
// asynchronously when the TTL has expired. The first frames of a session
// may render without git state; it appears once the background poll
// lands. The cache is keyed by nothing but time: Harness draws a single
// working directory at a time, and a workspace switch falls back to the
// stale string for at most one TTL.
func gitStatusInfo(dir string) string {
	if dir == "" {
		return ""
	}
	gitMu.Lock()
	if gitRefreshing || time.Now().Before(gitRefreshAt) {
		cached := gitCache
		gitMu.Unlock()
		return cached
	}
	gitRefreshing = true
	// Read the stale value while still holding the lock: the refresh
	// goroutine below writes gitCache, so reading it after the unlock
	// races with that write.
	cached := gitCache
	gitMu.Unlock()
	go func() {
		info := collectGitInfo(dir)
		gitMu.Lock()
		gitCache = info
		gitRefreshAt = time.Now().Add(gitInfoTTL)
		gitRefreshing = false
		gitMu.Unlock()
	}()
	return cached
}

// collectGitInfo shells out to git twice - branch, then porcelain status
// - and formats the result. Outside a repo, or when git is missing, it
// returns "" and the header drops the segment.
func collectGitInfo(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitInfoTimeout)
	defer cancel()

	branch, err := gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch == "" {
		return ""
	}
	if branch == "HEAD" {
		// Detached HEAD: show the short SHA instead of the literal
		// branch name.
		if sha, shaErr := gitOutput(ctx, dir, "rev-parse", "--short", "HEAD"); shaErr == nil && sha != "" {
			branch = sha
		}
	}

	status, err := gitOutput(ctx, dir, "status", "--porcelain=v1", "--branch")
	if err != nil {
		return gitBranchGlyph + " " + branch
	}
	return formatGitStatus(branch, status)
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
