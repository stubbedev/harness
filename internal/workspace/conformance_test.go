package workspace_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/workspace"
)

// conformancePair is the same backend workspace seen two ways: in process,
// the way the server itself runs it, and through the HTTP client, the way
// a TUI attached to a server does.
type conformancePair struct {
	local  *workspace.AppWorkspace
	remote *workspace.ClientWorkspace
	// messages writes transcript rows the way the agent does.
	messages message.Service
}

func newConformancePair(t *testing.T) conformancePair {
	t.Helper()
	xdgIsolate(t)
	rt := newRuntimeServer(t)
	cwd, dataDir := t.TempDir(), t.TempDir()
	c := rt.newClient(t, cwd)
	wsProto, err := c.CreateWorkspace(t.Context(), proto.Workspace{Path: cwd, DataDir: dataDir})
	require.NoError(t, err)
	backendWS, err := rt.srv.Backend().GetWorkspace(wsProto.ID)
	require.NoError(t, err)
	return conformancePair{
		local:    backendWS.Ops(),
		remote:   workspace.NewClientWorkspace(c, *wsProto),
		messages: backendWS.Messages,
	}
}

// TestWorkspaceConformance runs each operation on one implementation and
// reads it back through the other, so a behaviour that exists in process
// but is lost, reshaped or unimplemented over the wire fails here.
func TestWorkspaceConformance(t *testing.T) {
	p := newConformancePair(t)
	ctx := context.Background()

	// Sessions made remotely are the sessions the server holds.
	created, err := p.remote.CreateSession(ctx, "conformance")
	require.NoError(t, err)
	local, err := p.local.GetSession(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.Title, local.Title)

	require.NoError(t, p.remote.RenameSession(ctx, created.ID, "renamed"))
	local, err = p.local.GetSession(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", local.Title)

	remoteList, err := p.remote.ListSessions(ctx)
	require.NoError(t, err)
	localList, err := p.local.ListSessions(ctx)
	require.NoError(t, err)
	require.Len(t, remoteList, len(localList))

	// A transcript with every kind of part reads the same both ways.
	parts := []message.ContentPart{
		message.TextContent{Text: "prompt"},
		message.ShellCommand{Command: "ls", Output: "a"},
	}
	_, err = p.messages.Create(ctx, created.ID, message.CreateMessageParams{Role: message.User, Parts: parts})
	require.NoError(t, err)
	_, err = p.messages.Create(ctx, created.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{
		message.ContextNote{Kind: message.ContextNoteRuntime, Text: "env"},
	}})
	require.NoError(t, err)
	_, err = p.messages.Create(ctx, created.ID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: "reply"},
		message.ToolCall{ID: "c1", Name: "mcp_srv_tool", MCPServer: "srv", Finished: true},
	}})
	require.NoError(t, err)

	localMsgs, err := p.local.ListMessages(ctx, created.ID)
	require.NoError(t, err)
	remoteMsgs, err := p.remote.ListMessages(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, remoteMsgs, len(localMsgs))
	for i := range localMsgs {
		require.Equal(t, localMsgs[i].Parts, remoteMsgs[i].Parts, "message %d parts", i)
	}

	localHistory, err := p.local.PromptHistory(ctx, created.ID)
	require.NoError(t, err)
	remoteHistory, err := p.remote.PromptHistory(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, localHistory, remoteHistory)
	require.NotEmpty(t, remoteHistory)

	require.NoError(t, p.remote.DeleteSession(ctx, created.ID))
	_, err = p.local.GetSession(ctx, created.ID)
	require.Error(t, err, "a session deleted remotely is gone locally")
}
