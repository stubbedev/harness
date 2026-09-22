package model

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/workspace"
)

type headerWorkspace struct {
	workspace.Workspace
}

func (w *headerWorkspace) Config() *config.Config { return &config.Config{} }

// drawHeaderLine renders the compact header into a fresh screen buffer
// and returns its single row, styles stripped.
func drawHeaderLine(t *testing.T, com *common.Common, sess *session.Session, diagnostics lsp.DiagnosticCounts) string {
	t.Helper()
	const w = 120
	scr := uv.NewScreenBuffer(w, 1)
	newHeader(com).drawHeader(scr, uv.Rect(0, 0, w, 1), sess, w, diagnostics, "")
	return strings.TrimRight(ansi.Strip(scr.Render()), " ")
}

// headerTestCommon builds the common state the header renders from: a
// config-bearing workspace, since the header also reads the agent's
// model and context window.
func headerTestCommon() *common.Common {
	return common.DefaultCommon(&headerWorkspace{Workspace: &countingWorkspace{ready: true}})
}

// TestHeaderDiagnosticsBreakDownBySeverity pins the header's diagnostic
// hint to the per-severity form the details view uses: 1 error, 2
// warnings and 3 hints render as E1 W2 H3 - not the old total badge
// ("E6") that passed a warning count off as errors.
func TestHeaderDiagnosticsBreakDownBySeverity(t *testing.T) {
	t.Parallel()

	sess := &session.Session{ID: "s1"}
	line := drawHeaderLine(t, headerTestCommon(), sess, lsp.DiagnosticCounts{Error: 1, Warning: 2, Hint: 3})
	require.Contains(t, line, "E1")
	require.Contains(t, line, "W2")
	require.Contains(t, line, "H3")
	require.NotContains(t, line, "E6")
}

// TestHeaderDiagnosticsAllClearRendersNothing pins that a session with
// no diagnostics shows no diagnostic hint at all.
func TestHeaderDiagnosticsAllClearRendersNothing(t *testing.T) {
	t.Parallel()

	sess := &session.Session{ID: "s1"}
	line := drawHeaderLine(t, headerTestCommon(), sess, lsp.DiagnosticCounts{})
	require.NotContains(t, line, "E0")
}

// TestHeaderRendersWithoutSession pins the landing behavior: the status
// line renders before any session exists, so the working directory and
// git branch are on screen from the first frame instead of appearing
// only after the first message creates a session.
func TestHeaderRendersWithoutSession(t *testing.T) {
	t.Parallel()

	line := drawHeaderLine(t, headerTestCommon(), nil, lsp.DiagnosticCounts{Error: 1})
	require.Contains(t, line, "E1", "the status line must render before a session exists")
}

// TestHeaderCarriesNoDetailsHint pins that the session-details toggle is
// hinted on the bottom help row (see help_hints_test.go), never inside
// the status line itself.
func TestHeaderCarriesNoDetailsHint(t *testing.T) {
	t.Parallel()

	sess := &session.Session{ID: "s1"}
	line := drawHeaderLine(t, headerTestCommon(), sess, lsp.DiagnosticCounts{})
	require.NotContains(t, line, "ctrl+d")
	require.NotContains(t, line, "details")
}
