package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

type Status string

const (
	Passed  Status = "passed"
	Failed  Status = "failed"
	Skipped Status = "skipped"
	Blocked Status = "blocked"
)

type CheckResult struct {
	Name      string        `json:"name"`
	Command   []string      `json:"command"`
	Status    Status        `json:"status"`
	Reason    string        `json:"reason,omitempty"`
	ExitCode  int           `json:"exit_code"`
	Output    string        `json:"output,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
	TimedOut  bool          `json:"timed_out,omitempty"`
	Duration  time.Duration `json:"duration_ns"`
}

type Result struct {
	Status         Status        `json:"status"`
	Reason         string        `json:"reason,omitempty"`
	ChangedPaths   []string      `json:"changed_paths"`
	RevisionBefore string        `json:"revision_before,omitempty"`
	RevisionAfter  string        `json:"revision_after,omitempty"`
	StartedAt      time.Time     `json:"started_at"`
	FinishedAt     time.Time     `json:"finished_at"`
	Checks         []CheckResult `json:"checks"`
}

func (r Result) Metadata() string {
	b, _ := json.Marshal(struct {
		Verification Result `json:"verification"`
	}{r})
	return string(b)
}

type Runner struct {
	root   string
	config VerificationConfig
}

func New(root string, cfg VerificationConfig) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("verification workspace is not a directory")
	}
	cfg.Rules = slices.Clone(cfg.Rules)
	for i := range cfg.Rules {
		cfg.Rules[i].Paths = slices.Clone(cfg.Rules[i].Paths)
		cfg.Rules[i].Inputs = slices.Clone(cfg.Rules[i].Inputs)
		cfg.Rules[i].Command = slices.Clone(cfg.Rules[i].Command)
	}
	return &Runner{root: root, config: cfg}, nil
}

func (r *Runner) selectRules(paths []string) ([]Rule, []string, error) {
	normalized := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			return nil, nil, fmt.Errorf("empty changed path")
		}
		if filepath.IsAbs(p) {
			// The workspace root is canonicalized at construction; an
			// absolute ledger path may arrive through a symlinked prefix
			// (macOS TMPDIR is /var, the canonical prefix /private/var),
			// which would otherwise relativize as an escape.
			if resolved, err := filepath.EvalSymlinks(p); err == nil {
				p = resolved
			}
			var err error
			p, err = filepath.Rel(r.root, p)
			if err != nil {
				return nil, nil, err
			}
		}
		p = filepath.ToSlash(filepath.Clean(p))
		if p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "\\") || strings.Contains(p, ":") {
			return nil, nil, fmt.Errorf("changed path outside workspace: %q", p)
		}
		normalized = append(normalized, p)
	}
	slices.Sort(normalized)
	normalized = slices.Compact(normalized)
	var rules []Rule
	for _, rule := range r.config.Rules {
		for _, p := range normalized {
			if matches(rule.Paths, p) {
				rules = append(rules, rule)
				break
			}
		}
	}
	return rules, normalized, nil
}

func matches(globs []string, p string) bool {
	for _, glob := range globs {
		if ok, _ := doublestar.Match(glob, p); ok {
			return true
		}
	}
	return false
}

func (r *Runner) Run(ctx context.Context, changedPaths []string) (result Result) {
	result = Result{Status: Blocked, StartedAt: time.Now(), Checks: []CheckResult{}}
	defer func() { result.FinishedAt = time.Now() }()
	rules, paths, err := r.selectRules(changedPaths)
	result.ChangedPaths = paths
	if err != nil {
		result.Reason = err.Error()
		return
	}
	result.RevisionBefore, err = r.fingerprint(ctx, rules, paths)
	if err != nil {
		result.Reason = err.Error()
		return
	}
	result.Status = Passed
	if len(rules) == 0 {
		result.Status = Skipped
		result.Reason = "no configured rules apply"
	}
	for _, rule := range rules {
		if ctx.Err() != nil {
			result.Status = Blocked
			result.Reason = ctx.Err().Error()
			break
		}
		check := r.runCheck(ctx, rule)
		result.Checks = append(result.Checks, check)
		if check.Status == Blocked {
			result.Status = Blocked
		} else if check.Status == Failed && result.Status != Blocked {
			result.Status = Failed
		}
	}
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	result.RevisionAfter, err = r.fingerprint(postCtx, rules, paths)
	if err != nil {
		result.Status = Blocked
		result.Reason = "post-verification revision: " + err.Error()
	} else if result.RevisionAfter != result.RevisionBefore {
		result.Status = Blocked
		result.Reason = "relevant workspace content changed during verification"
	}
	return
}

func (r *Runner) Current(ctx context.Context, changedPaths []string, result Result) (bool, error) {
	if result.RevisionBefore == "" || result.RevisionBefore != result.RevisionAfter {
		return false, nil
	}
	rules, paths, err := r.selectRules(changedPaths)
	if err != nil {
		return false, err
	}
	revision, err := r.fingerprint(ctx, rules, paths)
	return err == nil && revision == result.RevisionAfter, err
}

type Decision struct {
	Allow  bool   `json:"allow"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

func (r *Runner) Gate(ctx context.Context, changedPaths []string, result *Result, repairAttempts int) Decision {
	if !r.config.RequireOnCompletion {
		return Decision{Allow: true, Action: "complete"}
	}
	rules, _, err := r.selectRules(changedPaths)
	if err != nil {
		return Decision{Action: "blocked", Reason: err.Error()}
	}
	if len(rules) == 0 {
		return Decision{Allow: true, Action: "complete", Reason: "no configured rules apply"}
	}
	if result == nil {
		return Decision{Action: "verify", Reason: "applicable checks have not run"}
	}
	current, err := r.Current(ctx, changedPaths, *result)
	if err != nil {
		return Decision{Action: "blocked", Reason: err.Error()}
	}
	if !current {
		return Decision{Action: "verify", Reason: "verification is stale"}
	}
	if result.Status == Passed {
		return Decision{Allow: true, Action: "complete"}
	}
	if result.Status == Failed && repairAttempts >= 0 && repairAttempts < r.config.MaxRepairAttempts {
		return Decision{Action: "repair", Reason: "applicable checks failed"}
	}
	return Decision{Action: "blocked", Reason: "verification did not pass or repair budget exhausted"}
}
