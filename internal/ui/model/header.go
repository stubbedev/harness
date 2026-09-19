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

// drawHeader draws the header for the given session. diagnostics come
// from the UI's memoized LSP state: drawing runs on every frame and must
// not probe the workspace (a synchronous HTTP round-trip in client/server
// mode). breadcrumb is the parent-session breadcrumb shown when a child
// (subagent) session is being viewed, empty otherwise.
func (h *header) drawHeader(
	scr uv.Screen,
	area uv.Rectangle,
	session *session.Session,
	detailsOpen bool,
	width int,
	diagnostics lsp.DiagnosticCounts,
	breadcrumb string,
) {
	h.width = width

	if session == nil {
		return
	}

	if session.ID == "" {
		return
	}

	// The compact header is a single status line, like the status bars
	// other coding agents render: working directory and git state flush
	// left, context usage, model and the details hint flush right.
	availWidth := width - leftPadding - rightPadding
	left, right := renderHeaderDetails(
		h.com,
		session,
		diagnostics,
		detailsOpen,
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
// git state) and the right (LSP errors, context usage with model, and the
// details hint).
func renderHeaderDetails(
	com *common.Common,
	session *session.Session,
	diagnostics lsp.DiagnosticCounts,
	detailsOpen bool,
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
	var tuiOpts *config.TUIOptions
	if cfg := com.Config(); cfg != nil && cfg.Options != nil {
		tuiOpts = cfg.Options.TUI
	}
	if tuiOpts.ShowGitStatus() {
		// The git segment reads a cache the git watcher refreshes in
		// the background, so this never blocks on a subprocess.
		if seg := gitHeaderParts(t, com.Workspace.WorkingDir()); seg != "" {
			leftParts = append(leftParts, seg)
		}
	}

	// Right: diagnostics, context usage with the model ID, and the
	// session-details hint.
	var rightParts []string
	// Diagnostics are shown broken down by severity, the same rendered
	// form the LSP section uses; the all-clear case renders nothing.
	if diagnostics := lspDiagnostics(t, severityCounts(diagnostics)); diagnostics != "" {
		rightParts = append(rightParts, diagnostics)
	}

	agentCfg := com.Config().Agents[config.AgentCoder]
	model := com.Config().GetModelByType(agentCfg.Model)
	// Measured against the usable window, not the raw one: max_tokens is
	// reserved from the same window, so a percentage of the raw number
	// reads lower than the share of the budget actually spent.
	usable := com.Config().UsableContextWindowFor(agentCfg.Model)
	if model != nil && usable > 0 {
		percentage := (float64(session.CompletionTokens+session.PromptTokens) / float64(usable)) * 100
		// The model ID rides beside the context percentage so the
		// header shows what is answering, not just how full it is.
		percentageText := fmt.Sprintf("%d%% %s", int(percentage), model.ID)
		if session.EstimatedUsage {
			percentageText = "~" + percentageText
		}
		rightParts = append(rightParts, t.Header.Percentage.Render(percentageText))
	}

	// The details hint follows the keymap so a rebind of chat.details
	// moves the label with it.
	keystroke := com.KeyMap().Chat.Details.Help().Key
	if detailsOpen {
		rightParts = append(rightParts, t.Header.Keystroke.Render(keystroke)+t.Header.KeystrokeTip.Render(" close"))
	} else {
		rightParts = append(rightParts, t.Header.Keystroke.Render(keystroke)+t.Header.KeystrokeTip.Render(" open "))
	}

	dot := t.Header.Separator.Render(" • ")
	return strings.Join(leftParts, dot), dot + strings.Join(rightParts, dot)
}
