package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// elicitingServer is a server whose tool asks the client a question the way
// protocol 2026-07-28 requires: the first call returns InputRequests, and
// the client is expected to answer and call again with InputResponses.
// Sending elicitation/create mid-request (the pre-2026-07-28 shape) is
// refused by the SDK, so a server that still does that never reaches the
// client's handler at all.
func elicitingServer(t *testing.T, params *mcp.ElicitParams) *mcp.Server {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "srv"}, nil)
	mcp.AddTool(
		server,
		&mcp.Tool{Name: "ask", Description: "asks the user"},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			if resp, ok := req.Params.InputResponses["q"]; ok {
				answered, err := json.Marshal(resp)
				require.NoError(t, err)
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: string(answered)}},
				}, nil, nil
			}
			return &mcp.CallToolResult{
				InputRequests: mcp.InputRequestMap{"q": params},
				RequestState:  "ask",
			}, nil, nil
		},
	)
	return server
}

// connectWithElicitation wires a client the way Initialize does — the
// installed elicitation handler becomes the connection's handler — and
// connects it to server over in-memory transports.
func connectWithElicitation(t *testing.T, server *mcp.Server, name string) *mcp.ClientSession {
	t.Helper()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	opts := &mcp.ClientOptions{}
	if elicit := currentElicitationHandler(name); elicit != nil {
		opts.ElicitationHandler = elicit
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "harness-test"}, opts)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession
}

func TestElicitationReachesTheHandler(t *testing.T) {
	params := &mcp.ElicitParams{
		Message: "Which branch?",
		RequestedSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"branch": {"type": "string"}},
			"required": ["branch"]
		}`),
	}

	t.Run("an answer is delivered and the tool call resumes", func(t *testing.T) {
		var gotServer string
		var gotMessage string
		SetElicitationHandler(func(_ context.Context, server string, p *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			gotServer = server
			gotMessage = p.Message
			return &mcp.ElicitResult{
				Action:  "accept",
				Content: map[string]any{"branch": "main"},
			}, nil
		})
		t.Cleanup(func() { SetElicitationHandler(nil) })

		session := connectWithElicitation(t, elicitingServer(t, params), "elicit")

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ask"})
		require.NoError(t, err)

		require.Equal(t, "elicit", gotServer)
		require.Equal(t, "Which branch?", gotMessage)
		require.False(t, res.IsError, "tool call failed: %v", res.Content)
		require.Len(t, res.Content, 1)
		text, ok := res.Content[0].(*mcp.TextContent)
		require.True(t, ok)
		require.JSONEq(t, `{"action":"accept","content":{"branch":"main"}}`, text.Text)
	})

	t.Run("a handler error declines rather than failing the call", func(t *testing.T) {
		SetElicitationHandler(func(context.Context, string, *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			return nil, context.Canceled
		})
		t.Cleanup(func() { SetElicitationHandler(nil) })

		session := connectWithElicitation(t, elicitingServer(t, params), "elicit")

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ask"})
		require.NoError(t, err)
		require.Len(t, res.Content, 1)
		text, ok := res.Content[0].(*mcp.TextContent)
		require.True(t, ok)
		require.Contains(t, text.Text, `"action":"decline"`)
	})

	// With no handler installed the client advertises no elicitation
	// capability, so the server's question cannot be answered at all.
	t.Run("no handler means no elicitation capability", func(t *testing.T) {
		SetElicitationHandler(nil)

		session := connectWithElicitation(t, elicitingServer(t, params), "elicit")

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ask"})
		require.Error(t, err)
	})
}
