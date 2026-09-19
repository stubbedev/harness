package agent

import (
	"context"
	"sync"
	"time"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/lsp"
)

type agentWorkspace struct {
	store       *config.ConfigStore
	manager     *lsp.Manager
	directories *DirectoryInstructions
	once        sync.Once
}

func (c *coordinator) newAgentWorkspace(root string) *agentWorkspace {
	store := c.cfg.WorkspaceSnapshot(root)
	return &agentWorkspace{store: store, manager: lsp.NewManager(store), directories: NewDirectoryInstructions(root, store.Config().Options.ContextPaths)}
}

func (c *coordinator) directoryTracker(workspace ...*agentWorkspace) *DirectoryInstructions {
	if len(workspace) > 0 && workspace[0] != nil {
		return workspace[0].directories
	}
	c.directoryOnce.Do(func() {
		c.directoryInstructions = NewDirectoryInstructions(c.cfg.WorkingDir(), c.cfg.Config().Options.ContextPaths)
	})
	return c.directoryInstructions
}

func (w *agentWorkspace) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		tools.CloseTerminalSessionsUnder(w.store.WorkingDir())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		w.manager.StopAll(ctx)
	})
}
