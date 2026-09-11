package model

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
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

// drawHeader draws the header for the given session. lspErrorCount comes
// from the UI's memoized LSP state: drawing runs on every frame and must not
// probe the workspace (a synchronous HTTP round-trip in client/server mode).
func (h *header) drawHeader(
	scr uv.Screen,
	area uv.Rectangle,
	session *session.Session,
	compact bool,
	detailsOpen bool,
	width int,
	lspErrorCount int,
	hyperCredits *int,
) {
	h.width = width

	if !compact || session == nil {
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
		lspErrorCount,
		detailsOpen,
		hyperCredits,
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
// the left (working directory and git state) and the right (LSP errors,
// context usage with model, hypercredits, and the details hint).
func renderHeaderDetails(
	com *common.Common,
	session *session.Session,
	lspErrorCount int,
	detailsOpen bool,
	hyperCredits *int,
) (left, right string) {
	t := com.Styles

	// Left: working directory and git state.
	const dirTrimLimit = 4
	cwd := fsext.DirTrim(fsext.PrettyPath(com.Workspace.WorkingDir()), dirTrimLimit)

	var leftParts []string
	leftParts = append(leftParts, t.Header.WorkingDir.Render(cwd))
	if com.Config().Options.TUI.ShowGitStatus() {
		// The git segment reads from a TTL cache fed by a background
		// poll, so this never blocks on a subprocess.
		if seg := gitHeaderParts(t, com.Workspace.WorkingDir()); seg != "" {
			leftParts = append(leftParts, seg)
		}
	}

	// Right: diagnostics, context usage with the model ID, hypercredits,
	// and the session-details hint.
	var rightParts []string
	if lspErrorCount > 0 {
		rightParts = append(rightParts, t.LSP.ErrorDiagnostic.Render(fmt.Sprintf("%s%d", styles.LSPErrorIcon, lspErrorCount)))
	}

	agentCfg := com.Config().Agents[config.AgentCoder]
	model := com.Config().GetModelByType(agentCfg.Model)
	if model != nil && model.ContextWindow > 0 {
		percentage := (float64(session.CompletionTokens+session.PromptTokens) / float64(model.ContextWindow)) * 100
		// The model ID rides beside the context percentage so the
		// header shows what is answering, not just how full it is.
		percentageText := fmt.Sprintf("%d%% %s", int(percentage), model.ID)
		if session.EstimatedUsage {
			percentageText = "~" + percentageText
		}
		rightParts = append(rightParts, t.Header.Percentage.Render(percentageText))
	}

	if com.IsHyper() && hyperCredits != nil {
		hc := t.Header.HypercreditIcon.Render(styles.HypercreditIcon) + " " + t.Header.Percentage.Render(common.FormatCredits(*hyperCredits))
		rightParts = append(rightParts, hc)
	}

	const keystroke = "ctrl+d"
	if detailsOpen {
		rightParts = append(rightParts, t.Header.Keystroke.Render(keystroke)+t.Header.KeystrokeTip.Render(" close"))
	} else {
		rightParts = append(rightParts, t.Header.Keystroke.Render(keystroke)+t.Header.KeystrokeTip.Render(" open "))
	}

	dot := t.Header.Separator.Render(" • ")
	return strings.Join(leftParts, dot), dot + strings.Join(rightParts, dot)
}
