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
	if relative, err := filepath.Rel(worktree.SourceRoot, c.cfg.WorkingDir()); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		root = filepath.Join(root, relative)
	}
	return &isolatedDispatch{worktree: worktree, workspace: c.newAgentWorkspace(root)}, nil
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
			response.Content += fmt.Sprintf("\nIsolated changes retained at %s (branch %s). Patch: %s. Changes have not been merged into the parent checkout.", result.Path, result.Branch, result.PatchPath)
			if result.IndexPatchPath != "" {
				response.Content += "\nStaged changes patch: " + result.IndexPatchPath
			}
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
