package chat

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func diagnosticsRenderItem(t *testing.T, input, result string) *LSPToolMessageItem {
	t.Helper()
	sty := &styles.Styles{}
	item := newLSPToolMessageItem(sty, message.ToolCall{
		ID:       "tc1",
		Name:     "lsp_diagnostics",
		Input:    input,
		Finished: true,
	}, &message.ToolResult{Content: result}, false)
	di, ok := item.(*LSPToolMessageItem)
	require.True(t, ok)
	return di
}

// A diagnostics item without a pushed live state must render exactly its
// stored report: nil means unknown, not resolved.
func TestDiagnosticsRenderWithoutLiveState(t *testing.T) {
	t.Parallel()
	item := diagnosticsRenderItem(t, `{"file_path":"/x/f.go"}`, "Error: /x/f.go:1:1 boom")
	out := item.BodyRender(80)
	require.Contains(t, out, "boom")
	require.NotContains(t, out, "resolved")
	require.NotContains(t, out, "still open")
}

// Once the servers report the file clean, the item must say so instead of
// leaving the old error as the last word.
func TestDiagnosticsRenderShowsResolution(t *testing.T) {
	t.Parallel()
	item := diagnosticsRenderItem(t, `{"file_path":"/x/f.go"}`, "Error: /x/f.go:1:1 boom")
	item.SetLiveDiagnostics(map[string]lsp.DiagnosticCounts{
		"/x/f.go": {Error: 0, Warning: 0},
	})
	out := item.BodyRender(80)
	require.Contains(t, out, "boom")
	require.Contains(t, out, "resolved")
	require.Contains(t, out, "no diagnostics remain for")
}

// Problems the servers still hold are named, not papered over.
func TestDiagnosticsRenderShowsRemaining(t *testing.T) {
	t.Parallel()
	item := diagnosticsRenderItem(t, `{"file_path":"/x/f.go"}`, "Error: /x/f.go:1:1 boom")
	item.SetLiveDiagnostics(map[string]lsp.DiagnosticCounts{
		"/x/f.go": {Error: 1, Warning: 2},
	})
	out := strings.ToLower(item.BodyRender(80))
	require.Contains(t, out, "1 error")
	require.Contains(t, out, "2 warnings")
	require.Contains(t, out, "still open")
}

// A file the servers are not tracking says nothing: their silence about it is
// not evidence it is clean.
func TestDiagnosticsRenderSkipsUnknownFile(t *testing.T) {
	t.Parallel()
	item := diagnosticsRenderItem(t, `{"file_path":"/x/f.go"}`, "Error: /x/f.go:1:1 boom")
	item.SetLiveDiagnostics(map[string]lsp.DiagnosticCounts{
		"/x/other.go": {Error: 3},
	})
	out := item.BodyRender(80)
	require.NotContains(t, out, "resolved")
	require.NotContains(t, out, "still open")
}

// A project-wide report sums every file, and an empty snapshot reads as a
// clean project.
func TestDiagnosticsRenderProjectTotals(t *testing.T) {
	t.Parallel()
	item := diagnosticsRenderItem(t, `{}`, "Error: /x/f.go:1:1 boom")
	item.SetLiveDiagnostics(map[string]lsp.DiagnosticCounts{
		"/x/f.go": {Error: 1},
		"/x/g.go": {Warning: 1},
	})
	out := strings.ToLower(item.BodyRender(80))
	require.Contains(t, out, "1 error")
	require.Contains(t, out, "1 warning")
	require.Contains(t, out, "still open in the project")

	item.SetLiveDiagnostics(map[string]lsp.DiagnosticCounts{})
	require.Contains(t, strings.ToLower(item.BodyRender(80)), "no diagnostics remain in the project")
}

// A refresh that changed nothing must not invalidate the item's render
// cache: the LSP state refreshes on every event, and the transcript should
// not pay a re-render each time.
func TestDiagnosticsSetLiveDiagnosticsSkipsNoOps(t *testing.T) {
	t.Parallel()
	item := diagnosticsRenderItem(t, `{"file_path":"/x/f.go"}`, "Error: /x/f.go:1:1 boom")
	live := map[string]lsp.DiagnosticCounts{"/x/f.go": {}}
	item.SetLiveDiagnostics(live)
	version := item.Version()

	item.SetLiveDiagnostics(live)
	require.Equal(t, version, item.Version(), "an unchanged snapshot must not bump the item")

	item.SetLiveDiagnostics(map[string]lsp.DiagnosticCounts{"/x/f.go": {Error: 1}})
	require.NotEqual(t, version, item.Version(), "a changed snapshot must bump the item")
}

// liveCountNote lists only the non-zero severities, singular and all.
func TestLiveCountNote(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", liveCountNote(lsp.DiagnosticCounts{}))
	require.Equal(t, "1 error", liveCountNote(lsp.DiagnosticCounts{Error: 1}))
	require.Equal(t, "2 errors, 1 warning, 1 hint",
		liveCountNote(lsp.DiagnosticCounts{Error: 2, Warning: 1, Hint: 1}))
}

// A streamed lsp call is created before its input arrives, so the action
// is unknown at construction. Once the input lands the item must render
// with the action's renderer, not the diagnostics fallback.
func TestLSPItemPicksRendererAfterInputArrives(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "m1", message.ToolCall{ID: "tc1", Name: "lsp"}, nil, false)
	item.SetToolCall(message.ToolCall{
		ID:       "tc1",
		Name:     "lsp",
		Input:    `{"action":"rename","symbol":"oldName","new_name":"newName"}`,
		Finished: true,
	})
	item.SetResult(&message.ToolResult{ToolCallID: "tc1", Content: "renamed 3 occurrences"})
	out := item.Render(100)
	require.Contains(t, out, "oldName → newName")
	require.NotContains(t, out, "project")

	setter, ok := item.(LiveDiagnosticsSetter)
	require.True(t, ok)
	setter.SetLiveDiagnostics(map[string]lsp.DiagnosticCounts{})
	require.NotContains(t, item.Render(100), "resolved")
}
