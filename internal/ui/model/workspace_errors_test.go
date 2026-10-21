package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

var errUnreachable = errors.New("server unreachable")

type unreachableWorkspace struct {
	*countingWorkspace
}

func (w *unreachableWorkspace) AgentReadyErr() error {
	return errors.Join(workspace.ErrServerUnreachable, errUnreachable)
}

func (w *unreachableWorkspace) AgentIsBusy() (bool, error) { return false, errUnreachable }

func (w *unreachableWorkspace) AgentCancelTurn(string) error { return errUnreachable }

func (w *unreachableWorkspace) LSPGetStates() (map[string]workspace.LSPClientInfo, error) {
	return nil, errUnreachable
}

func (w *unreachableWorkspace) LSPFileDiagnostics() (map[string]lsp.DiagnosticCounts, error) {
	return nil, errUnreachable
}

func newUnreachableUI(t *testing.T) *UI {
	t.Helper()
	pinTTLs(t)
	m := newBusyUI(&countingWorkspace{ready: true})
	m.com.Workspace = &unreachableWorkspace{countingWorkspace: &countingWorkspace{}}
	return m
}

func TestBusyRefreshErrorKeepsLastKnownState(t *testing.T) {
	m := newUnreachableUI(t)
	warmCaches(m, true)
	m.invalidateBusyCaches()

	runCmds(m, m.dispatchBusyRefresh())

	require.True(t, m.isAgentBusy(), "an unreachable server must not read as idle")
	require.True(t, m.agentReady, "an unreachable server must not read as not ready")
	require.True(t, m.agentBusyCache.fresh(busyCacheTTL), "a failed probe waits a TTL before retrying")
	require.False(t, m.busyFetchInFlight)
}

func TestPromptQueueRefreshErrorKeepsQueue(t *testing.T) {
	m := newUnreachableUI(t)
	warmCaches(m, true)
	m.promptQueueItems = []string{"a"}

	runCmds(m, m.dispatchPromptQueueRefresh())

	require.Equal(t, []string{"a"}, m.promptQueueItems)
	require.False(t, m.promptQueueInFlight)
}

func TestLSPRefreshErrorKeepsLiveDiagnostics(t *testing.T) {
	m := newUnreachableUI(t)
	warmCaches(m, false)
	prev := map[string]lsp.DiagnosticCounts{"/tmp/a.go": {Error: 2}}
	m.lspStates = map[string]workspace.LSPClientInfo{"gopls": {Name: "gopls", State: lsp.StateReady}}
	m.lspFileDiagnostics = prev

	runCmds(m, m.dispatchLSPRefresh())

	require.Equal(t, prev, m.lspFileDiagnostics, "an unreachable server must not report files clean")
	require.Contains(t, m.lspStates, "gopls")
	require.False(t, m.lspFetchInFlight)
}

func TestLSPRefreshSuccessAppliesDiagnostics(t *testing.T) {
	pinTTLs(t)
	ws := &countingWorkspace{
		ready:     true,
		lspStates: map[string]workspace.LSPClientInfo{"gopls": {Name: "gopls", State: lsp.StateReady}},
		lspDiags:  map[string]lsp.DiagnosticCounts{"gopls": {Warning: 3}},
	}
	m := newBusyUI(ws)
	warmCaches(m, false)

	runCmds(m, m.dispatchLSPRefresh())

	require.Equal(t, ws.lspStates, m.lspStates)
	require.Equal(t, lsp.DiagnosticCounts{Warning: 3}, m.lspDiagnostics["gopls"])
}

func TestCancelTurnErrorIsReported(t *testing.T) {
	m := newUnreachableUI(t)
	warmCaches(m, true)

	m.cancelAgent()
	cmd := m.cancelAgent()
	require.NotNil(t, cmd)

	msg := cmd()
	info, ok := msg.(util.InfoMsg)
	require.True(t, ok, "a failed cancel must surface, got %T", msg)
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Contains(t, info.Msg, errUnreachable.Error())
}

func TestMCPStatesMsgReportsError(t *testing.T) {
	t.Parallel()

	msg := mcpStatesMsg(&mcpStatesWorkspace{err: errUnreachable})
	info, ok := msg.(util.InfoMsg)
	require.True(t, ok, "got %T", msg)
	require.Equal(t, util.InfoTypeError, info.Type)

	msg = mcpStatesMsg(&mcpStatesWorkspace{})
	_, ok = msg.(mcpStateChangedMsg)
	require.True(t, ok, "got %T", msg)
}

type mcpStatesWorkspace struct {
	workspace.Workspace
	err error
}

func (w *mcpStatesWorkspace) MCPGetStates() (map[string]mcp.ClientInfo, error) {
	return nil, w.err
}
