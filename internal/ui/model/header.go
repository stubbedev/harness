package model

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
)

const (
	leftPadding  = 1
	rightPadding = 1
)

type header struct {
	com   *common.Common
	width int
}

// newHeader creates a new header model.
func newHeader(com *common.Common) *header {
	h := &header{
		com: com,
	}
	h.refresh()
	return h
}

// refresh invalidates cached header state. Call after the theme changes.
func (h *header) refresh() {
	h.width = 0
}

// drawHeader draws the compact status line for the given session: working
// directory, git state, diagnostics, and context usage with model flush as
// one row directly above the editor. It renders in every state with an
// editor, landing included - only the session-scoped context usage needs a
// session, so the directory and branch are on screen from the first frame.
// diagnostics come from the UI's memoized LSP state: drawing runs on every
// frame and must not probe the workspace (a synchronous HTTP round-trip in
// client/server mode). breadcrumb is the parent-session breadcrumb shown
// when a child (subagent) session is being viewed, empty otherwise.
func (h *header) drawHeader(
	scr uv.Screen,
	area uv.Rectangle,
	session *session.Session,
	width int,
	diagnostics lsp.DiagnosticCounts,
	breadcrumb string,
) {
	h.width = width

	// The compact header is a single status line, like the status bars
	// other coding agents render: working directory and git state flush
	// left, diagnostics and context usage with model flush right.
	availWidth := width - leftPadding - rightPadding
	left, right := renderHeaderDetails(
		h.com,
		session,
		diagnostics,
		breadcrumb,
	)

	gap := availWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		// Not enough room: shrink the left side first so the usage and
		// model stay visible, then truncate whatever still overflows.
		maxLeft := max(0, availWidth-lipgloss.Width(right)-1)
		left = ansi.Truncate(left, maxLeft, "…")
		if lipgloss.Width(left)+lipgloss.Width(right) > availWidth {
			right = ansi.Truncate(right, max(0, availWidth-lipgloss.Width(left)), "…")
		}
		gap = availWidth - lipgloss.Width(left) - lipgloss.Width(right)
	}

	line := left + strings.Repeat(" ", max(gap, 0)) + right

	view := uv.NewStyledString(
		h.com.Styles.Header.Wrapper.Padding(0, rightPadding, 0, leftPadding).Render(line),
	)
	view.Draw(scr, area)
}

// renderHeaderDetails renders the two halves of the compact status line:
// the left (breadcrumb when viewing a child session, working directory and
// git state) and the right (LSP errors and context usage with model, or
// just the model while no session exists yet).
func renderHeaderDetails(
	com *common.Common,
	session *session.Session,
	diagnostics lsp.DiagnosticCounts,
	breadcrumb string,
) (left, right string) {
	t := com.Styles

	// Left: working directory and git state.
	const dirTrimLimit = 4
	cwd := fsext.DirTrim(fsext.PrettyPath(com.Workspace.WorkingDir()), dirTrimLimit)

	var leftParts []string
	if breadcrumb != "" {
		leftParts = append(leftParts, breadcrumb)
	}
	leftParts = append(leftParts, t.Header.WorkingDir.Render(cwd))
	if seg := gitSegment(com); seg != "" {
		leftParts = append(leftParts, seg)
	}

	// Right: diagnostics and context usage with the model ID.
	var rightParts []string
	// Diagnostics are shown broken down by severity, the same rendered
	// form the LSP section uses; the all-clear case renders nothing.
	if diagnostics := lspDiagnostics(t, severityCounts(diagnostics)); diagnostics != "" {
		rightParts = append(rightParts, diagnostics)
	}

	agentCfg := com.Config().Agents[config.AgentCoder]
	model := com.Config().GetModelByType(agentCfg.Model)
	if model != nil {
		// Measured against the usable window, not the raw one: max_tokens is
		// reserved from the same window, so a percentage of the raw number
		// reads lower than the share of the budget actually spent.
		usable := com.Config().UsableContextWindowFor(agentCfg.Model)
		if session != nil && session.ID != "" && usable > 0 {
			percentage := (float64(session.CompletionTokens+session.PromptTokens) / float64(usable)) * 100
			// The model ID rides beside the context percentage so the
			// line shows what is answering, not just how full it is.
			percentageText := fmt.Sprintf("%d%% %s", int(percentage), model.ID)
			if session.EstimatedUsage {
				percentageText = "~" + percentageText
			}
			rightParts = append(rightParts, t.Header.Percentage.Render(percentageText))
		} else {
			// No session yet (landing): the line still shows what will
			// answer, just without a context percentage.
			rightParts = append(rightParts, t.Header.Percentage.Render(model.ID))
		}
	}

	dot := t.Header.Separator.Render(" • ")
	return strings.Join(leftParts, dot), dot + strings.Join(rightParts, dot)
}
