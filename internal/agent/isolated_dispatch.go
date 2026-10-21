package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/filepathext"
	"github.com/tidwall/sjson"
)

type isolatedDispatch struct {
	workspace *agentWorkspace
	worktree  *AgentWorktree
	once      sync.Once
}

func (c *coordinator) prepareIsolatedDispatch(ctx context.Context) (*isolatedDispatch, error) {
	worktree, err := NewWorktree(ctx, c.cfg.WorkingDir(), "")
	if err != nil {
		return nil, err
	}
	root := worktree.Path
	if relative, ok := filepathext.RelWithin(worktree.SourceRoot, c.cfg.WorkingDir()); ok {
		root = filepath.Join(root, relative)
	}
	return &isolatedDispatch{worktree: worktree, workspace: c.newAgentWorkspace(root)}, nil
}

func (d *isolatedDispatch) prompt(parentDir, task string) string {
	return fmt.Sprintf("Isolated workspace: %s, a Git worktree on branch %s. Work in that checkout; do not run commands in, or change, the parent checkout %s. Changes and commits made in the isolated checkout are retained on branch %s and reported to the caller.\n\n%s", d.workspace.store.WorkingDir(), d.worktree.Branch, parentDir, d.worktree.Branch, task)
}

func worktreeReport(result WorktreeResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nIsolated changes retained at %s on branch %s (based on %s).", result.Path, result.Branch, result.Base)
	if len(result.Commits) > 0 {
		b.WriteString("\nCommits on the branch, oldest first:")
		hashes := make([]string, 0, len(result.Commits))
		for _, commit := range result.Commits {
			fmt.Fprintf(&b, "\n- %s %s", commit.Hash, commit.Subject)
			hashes = append(hashes, commit.Hash)
		}
		b.WriteString("\nApply them in the parent checkout with: git cherry-pick " + strings.Join(hashes, " "))
	}
	fmt.Fprintf(&b, "\nPatch of the final checkout against the dispatch snapshot, committed and uncommitted changes together: %s", result.PatchPath)
	if result.IndexPatchPath != "" {
		b.WriteString("\nStaged changes patch: " + result.IndexPatchPath)
	}
	b.WriteString("\nNothing has been merged into the parent checkout.")
	return b.String()
}

func (d *isolatedDispatch) finish(response *fantasy.ToolResponse) {
	d.once.Do(func() {
		d.workspace.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		result, err := d.worktree.Finish(ctx)
		if response == nil {
			if err != nil {
				slog.Warn("Failed to finish unused subagent worktree", "path", result.Path, "error", err)
			}
			return
		}
		if err != nil {
			response.Content += fmt.Sprintf("\nWorktree cleanup/export failed; checkout retained at %s: %v", result.Path, err)
		} else if result.Removed {
			response.Content += "\nIsolated worktree unchanged and removed."
		} else {
			response.Content += worktreeReport(result)
		}
		metadata := response.Metadata
		if metadata == "" {
			metadata = "{}"
		}
		data, marshalErr := json.Marshal(result)
		if marshalErr == nil {
			if updated, setErr := sjson.SetRaw(metadata, "worktree", string(data)); setErr == nil {
				response.Metadata = updated
			}
		}
	})
}
