package mcp

import (
	"context"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stubbedev/harness/internal/procscope"
)

// scopedTransport starts a stdio MCP server in a systemd scope of its own
// (see procscope.Servers), so the kernel killing a runaway server for
// running out of memory takes neither Harness nor the terminal around it
// down. Where servers cannot be contained it is the command transport
// unchanged.
//
// The command is wrapped at the last moment, in Connect, and put back as
// configured once the process is started: everything that reads it
// before (the transport's tests) or after (stdioCheck re-running a server
// that failed to start) sees the server, not systemd-run. os/exec reads
// neither Path nor Args once Start has returned.
type scopedTransport struct {
	inner *mcp.CommandTransport
	label string

	mu    sync.Mutex
	scope *procscope.Scope
}

// newScopedTransport wraps a stdio server's command transport; name is
// the server's configured name, which labels its scope's unit.
func newScopedTransport(name string, inner *mcp.CommandTransport) *scopedTransport {
	return &scopedTransport{inner: inner, label: "mcp-" + name}
}

// Connect implements [mcp.Transport].
func (t *scopedTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	cmd := t.inner.Command
	path, args := cmd.Path, cmd.Args
	var rest []string
	if len(args) > 1 {
		rest = args[1:]
	}
	name, argv, scope := procscope.Servers.Command(t.label, path, rest)
	if scope != nil {
		cmd.Path = name
		cmd.Args = append([]string{name}, argv...)
		// Cancelling the session kills the server's process group (see
		// configureStdioProcess); the scope also holds what left it.
		cancel := cmd.Cancel
		cmd.Cancel = func() error {
			var err error
			if cancel != nil {
				err = cancel()
			} else if cmd.Process != nil {
				err = cmd.Process.Kill()
			}
			scope.Kill()
			return err
		}
	}
	conn, err := t.inner.Connect(ctx)
	cmd.Path, cmd.Args = path, args
	if err != nil {
		scope.Kill()
		return nil, err
	}
	if cmd.Process != nil {
		procscope.RaiseOOMScore(cmd.Process.Pid)
	}
	t.mu.Lock()
	t.scope = scope
	t.mu.Unlock()
	return conn, nil
}

// serverScope returns the scope the started server runs in, nil when it
// runs uncontained or has not started.
func (t *scopedTransport) serverScope() *procscope.Scope {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scope
}

// unwrapTransport implements [transportWrapper].
func (t *scopedTransport) unwrapTransport() mcp.Transport { return t.inner }
