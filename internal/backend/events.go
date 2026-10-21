package backend

import (
	"context"
	"errors"
	"log/slog"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/crash"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
)

func (b *Backend) SubscribeEventFrames(ctx context.Context, workspaceID string) (<-chan pubsub.Event[[]byte], error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.EventFrames(ctx), nil
}

func (ws *Workspace) EventFrames(ctx context.Context) <-chan pubsub.Event[[]byte] {
	ws.framesOnce.Do(ws.startFrameEncoder)
	return ws.frames.Subscribe(ctx)
}

func (ws *Workspace) startFrameEncoder() {
	ws.frames = pubsub.NewStreamingBroker[[]byte]()
	events := ws.Events(ws.ctx)
	crash.Go("backend.eventFrames", func() {
		defer ws.frames.Shutdown()
		for ev := range events {
			if ws.frames.GetSubscriberCount() == 0 {
				continue
			}
			frame := encodeEventFrame(ev.Payload)
			if frame == nil {
				continue
			}
			if ev.MustDeliver {
				ws.frames.PublishMustDeliver(ws.ctx, ev.Type, frame)
			} else {
				ws.frames.Publish(ev.Type, frame)
			}
		}
	})
}

func encodeEventFrame(ev any) []byte {
	frame, err := proto.EncodeEvent(ev)
	switch {
	case err == nil:
		return frame
	case errors.Is(err, proto.ErrNoWireForm):
		slog.Debug("Dropping event with no wire form for SSE", "error", err)
	default:
		slog.Warn("Failed to encode event for SSE", "error", err)
	}
	return nil
}

// GetLSPDiagnostics returns diagnostics for a specific LSP client in
// the workspace.
func (b *Backend) GetLSPDiagnostics(workspaceID, lspName string) (map[protocol.DocumentURI][]protocol.Diagnostic, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	for name, client := range ws.LSPManager.Clients().Seq2() {
		if name == lspName {
			return client.GetDiagnostics(), nil
		}
	}

	return nil, ErrLSPClientNotFound
}

// MCPAuthenticate runs the OAuth flow for a named MCP server with the
// local browser suppressed: the authorization URL is exposed via
// MCPAuthURL/MCPPendingAuth for the calling client to open on the user's
// machine. The call blocks until the flow completes, fails, or ctx is
// cancelled. workspaceID selects the workspace whose config drives the
// flow.
func (b *Backend) MCPAuthenticate(ctx context.Context, workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	finish, cancel, err := mcptools.BeginAuth(ws.Cfg, name)
	if err != nil {
		return err
	}
	defer cancel()
	return finish(ctx)
}
