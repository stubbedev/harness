package workspace

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/client"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/proto"
)

func downClientWorkspace(t *testing.T) *ClientWorkspace {
	t.Helper()
	c, err := client.NewClient(t.TempDir(), "tcp", "127.0.0.1:1")
	require.NoError(t, err)
	return NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})
}

func handlerClientWorkspace(t *testing.T, h http.HandlerFunc) *ClientWorkspace {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)
	return NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})
}

func TestClientWorkspace_ServerDownMutatorsReturnErrors(t *testing.T) {
	t.Parallel()

	ws := downClientWorkspace(t)
	ctx := t.Context()

	calls := map[string]func() error{
		"AgentCancel":           func() error { return ws.AgentCancel("s1") },
		"AgentClearQueue":       func() error { return ws.AgentClearQueue("s1") },
		"FileTrackerRecordRead": func() error { return ws.FileTrackerRecordRead(ctx, "s1", "/tmp/x") },
		"LSPStart":              func() error { return ws.LSPStart(ctx, "/tmp/x") },
		"LSPStopAll":            func() error { return ws.LSPStopAll(ctx) },
		"MCPRefreshPrompts":     func() error { return ws.MCPRefreshPrompts(ctx, "m") },
		"MCPRefreshResources":   func() error { return ws.MCPRefreshResources(ctx, "m") },
		"RefreshMCPTools":       func() error { return ws.RefreshMCPTools(ctx, "m") },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, call())
		})
	}
}

func TestClientWorkspace_ServerDownReadersReturnErrors(t *testing.T) {
	t.Parallel()

	ws := downClientWorkspace(t)
	ctx := t.Context()

	calls := map[string]func() error{
		"AgentIsBusy": func() error { _, err := ws.AgentIsBusy(); return err },
		"AgentIsSessionBusy": func() error {
			_, err := ws.AgentIsSessionBusy("s1")
			return err
		},
		"AgentModel":         func() error { _, err := ws.AgentModel(); return err },
		"AgentQueuedPrompts": func() error { _, err := ws.AgentQueuedPrompts("s1"); return err },
		"AgentQueuedPromptsList": func() error {
			_, err := ws.AgentQueuedPromptsList("s1")
			return err
		},
		"FileTrackerLastReadTime": func() error {
			_, err := ws.FileTrackerLastReadTime(ctx, "s1", "/tmp/x")
			return err
		},
		"GetDefaultSmallModel": func() error {
			_, err := ws.GetDefaultSmallModel("p")
			return err
		},
		"LSPGetStates": func() error { _, err := ws.LSPGetStates(); return err },
		"LSPGetDiagnosticCounts": func() error {
			_, err := ws.LSPGetDiagnosticCounts("gopls")
			return err
		},
		"MCPGetStates": func() error { _, err := ws.MCPGetStates(); return err },
		"MCPAuthURL":   func() error { _, err := ws.MCPAuthURL("m"); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, call())
		})
	}
}

func TestClientWorkspace_LSPFileDiagnosticsServerDownIsNotClean(t *testing.T) {
	t.Parallel()

	counts, err := downClientWorkspace(t).LSPFileDiagnostics()
	require.Error(t, err)
	require.Nil(t, counts)
}

func TestClientWorkspace_LSPFileDiagnosticsFailingServerIsNotClean(t *testing.T) {
	t.Parallel()

	ws := handlerClientWorkspace(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspaces/ws-1/lsps":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]proto.LSPClientInfo{
				"gopls": {Name: "gopls", State: lsp.StateReady},
			}))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	counts, err := ws.LSPFileDiagnostics()
	require.Error(t, err)
	require.Nil(t, counts)
}

func TestClientWorkspace_LSPDiagnosticsSkipStoppedServers(t *testing.T) {
	t.Parallel()

	uri := protocol.DocumentURI("file:///tmp/a.go")
	ws := handlerClientWorkspace(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspaces/ws-1/lsps":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]proto.LSPClientInfo{
				"gopls":   {Name: "gopls", State: lsp.StateReady},
				"stopped": {Name: "stopped", State: lsp.StateStopped},
			}))
		case "/v1/workspaces/ws-1/lsps/gopls/diagnostics":
			require.NoError(t, json.NewEncoder(w).Encode(map[protocol.DocumentURI][]protocol.Diagnostic{
				uri: {{Severity: protocol.SeverityError}, {Severity: protocol.SeverityWarning}},
			}))
		case "/v1/workspaces/ws-1/lsps/stopped/diagnostics":
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(proto.Error{
				Message: "LSP client not found",
				Code:    proto.ErrorCodeNotFound,
			}))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	})

	counts, err := ws.LSPFileDiagnostics()
	require.NoError(t, err)
	require.Equal(t, map[string]lsp.DiagnosticCounts{
		"/tmp/a.go": {Error: 1, Warning: 1},
	}, counts)

	stopped, err := ws.LSPGetDiagnosticCounts("stopped")
	require.NoError(t, err)
	require.Equal(t, lsp.DiagnosticCounts{}, stopped)

	gopls, err := ws.LSPGetDiagnosticCounts("gopls")
	require.NoError(t, err)
	require.Equal(t, lsp.DiagnosticCounts{Error: 1, Warning: 1}, gopls)
}

func TestClientWorkspace_StateReadersReturnServerValues(t *testing.T) {
	t.Parallel()

	ws := handlerClientWorkspace(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspaces/ws-1/agent":
			require.NoError(t, json.NewEncoder(w).Encode(proto.AgentInfo{IsReady: true, IsBusy: true}))
		case "/v1/workspaces/ws-1/agent/sessions/s1/prompts/list":
			require.NoError(t, json.NewEncoder(w).Encode([]string{"a", "b"}))
		case "/v1/workspaces/ws-1/agent/sessions/s1/cancel":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	})

	busy, err := ws.AgentIsBusy()
	require.NoError(t, err)
	require.True(t, busy)

	prompts, err := ws.AgentQueuedPromptsList("s1")
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, prompts)

	require.NoError(t, ws.AgentCancel("s1"))
}
