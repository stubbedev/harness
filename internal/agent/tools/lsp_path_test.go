package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/lsp"
)

func runLSP(t *testing.T, tool fantasy.AgentTool, params LSPParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(sessionContext(t, "s1"), fantasy.ToolCall{ID: "call", Name: LSPToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func newClientlessManager(t *testing.T) (*lsp.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	off := false
	cfg := &config.Config{Options: &config.Options{AutoLSP: &off}}
	return lsp.NewManager(config.NewTestStoreWithWorkingDir(cfg, dir)), dir
}

func TestLSPPathsResolveAgainstWorkingDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paths := lspPaths{workingDir: dir}

	require.Equal(t, absPath(dir), paths.root(""))
	require.Equal(t, absPath(filepath.Join(dir, "pkg")), paths.root("pkg"))
	require.Equal(t, absPath(filepath.Join(dir, "main.go")), paths.optionalFile("main.go"))
	require.Equal(t, absPath(""), paths.optionalFile(""))
	other := filepath.Join(t.TempDir(), "x.go")
	require.Equal(t, absPath(other), paths.root(other))

	file, err := paths.file(LSPActionSymbols, "main.go")
	require.NoError(t, err)
	require.Equal(t, absPath(filepath.Join(dir, "main.go")), file)
}

func TestLSPToolResolvesRelativePathAgainstWorkingDirNotProcessCwd(t *testing.T) {
	t.Parallel()
	manager, client, dir := newDiagnosticsManager(t)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NotEqual(t, cwd, dir)
	client.PublishTestDiagnostics(filepath.Join(dir, "main.go"), []protocol.Diagnostic{diag(2, protocol.SeverityError, "undefined: x")})

	tool := NewLSPTool(manager, nil, nil, nil, dir)
	resp := runLSP(t, tool, LSPParams{Action: LSPActionDiagnostics, Path: "main.go"})

	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "<file_diagnostics>")
	require.NotContains(t, resp.Content, "<project_diagnostics>")
	require.Contains(t, resp.Content, "undefined: x")
}

func TestLSPFileActionsTakePathAndNameWhatNeedsAFile(t *testing.T) {
	t.Parallel()
	manager, dir := newClientlessManager(t)
	tool := NewLSPTool(manager, nil, nil, nil, dir)

	for _, action := range lspFileActions {
		resp := runLSP(t, tool, LSPParams{Action: action, Symbol: "f", Replacement: "x"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, string(action)+" needs path to name a file")
		require.Contains(t, resp.Content, "symbols and replace_symbol act on one file")
		require.NotContains(t, resp.Content, "file_path")

		resp = runLSP(t, tool, LSPParams{Action: action, Symbol: "f", Replacement: "x", Path: "."})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, dir+" is a directory")
	}
}

func TestLSPSymbolsReadsPath(t *testing.T) {
	t.Parallel()
	manager, dir := newClientlessManager(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600))
	tool := NewLSPTool(manager, nil, nil, nil, dir)

	resp := runLSP(t, tool, LSPParams{Action: LSPActionSymbols, Path: "main.go"})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "no LSP client handles "+filepath.Join(dir, "main.go"))
	require.Contains(t, resp.Content, "no client is running")
	require.Contains(t, resp.Content, "action restart")
}

func TestLSPResolveReportsMissingClientWithNextStep(t *testing.T) {
	t.Parallel()
	manager, dir := newClientlessManager(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc Target() {}\n"), 0o600))
	tool := NewLSPTool(manager, nil, nil, nil, dir)

	for _, action := range []LSPAction{LSPActionDefinition, LSPActionReferences, LSPActionRename} {
		resp := runLSP(t, tool, LSPParams{Action: action, Symbol: "Target", NewName: "Other"})
		require.True(t, resp.IsError, action)
		require.Contains(t, resp.Content, "cannot resolve symbol 'Target'", action)
		require.Contains(t, resp.Content, "no LSP client handles any file under "+dir, action)
		require.Contains(t, resp.Content, "action restart", action)
		require.NotContains(t, resp.Content, "not found", action)
	}
}

func TestLSPResolveReportsSymbolMentionedNowhere(t *testing.T) {
	t.Parallel()
	manager, _, dir := newDiagnosticsManager(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600))
	tool := NewLSPTool(manager, nil, nil, nil, dir)

	resp := runLSP(t, tool, LSPParams{Action: LSPActionDefinition, Symbol: "Missing", Path: "."})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "cannot resolve symbol 'Missing'")
	require.Contains(t, resp.Content, "no file under "+dir+" mentions it")
}

func TestLSPResolveReportsSearchFailure(t *testing.T) {
	t.Parallel()
	manager, dir := newClientlessManager(t)
	tool := NewLSPTool(manager, nil, nil, nil, dir)
	missing := filepath.Join(dir, "missing")

	resp := runLSP(t, tool, LSPParams{Action: LSPActionDefinition, Symbol: "Target", Path: "missing"})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "cannot resolve symbol 'Target'")
	require.Contains(t, resp.Content, missing)
}
