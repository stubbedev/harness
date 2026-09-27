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
	"github.com/stubbedev/harness/internal/ui/styles"
)

const (
	leftPadding  = 1
	rightPadding = 1
)

type header struct {
	com *common.Common

	// view memoizes the rendered header line. drawHeader runs on every
	// frame, so the line is rebuilt only when an input to it changed:
	// the draw width, the theme (refresh), or any of the state inputs
	// in headerState.
	view      string
	viewWidth int
	viewState headerState
	viewValid bool
}

// headerState is the memo key for the rendered header line: everything
// renderHeaderState reads, in its cheapest comparable form. The
// diagnostics, git segment, context usage and goal state each move
// through their own watcher or message; the width changes through
// layout; the theme goes through refresh.
type headerState struct {
	breadcrumb  string
	diagnostics lsp.DiagnosticCounts
	cwd         string
	git         string
	sessionID   string
	tokens      int64
	estimated   bool
	goalActive  bool
	hasModel    bool
	modelID     string
	usable      int64
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
	h.viewValid = false
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
	state := h.currentState(session, diagnostics, breadcrumb)
	if !h.viewValid || h.viewWidth != width || h.viewState != state {
		h.view = h.renderLine(width, state)
		h.viewWidth = width
		h.viewState = state
		h.viewValid = true
	}
	uv.NewStyledString(h.view).Draw(scr, area)
}

// currentState snapshots the inputs the compact status line renders
// from. The state is the memo key, so every read here is one the line
// actually depends on; the git segment comes from the watcher's cache
// and the model from config, both cheap enough to read per frame.
func (h *header) currentState(
	session *session.Session,
	diagnostics lsp.DiagnosticCounts,
	breadcrumb string,
) headerState {
	const dirTrimLimit = 4
	agentCfg := h.com.Config().Agents[config.AgentCoder]
	model := h.com.Config().GetModelByType(agentCfg.Model)

	state := headerState{
		breadcrumb:  breadcrumb,
		diagnostics: diagnostics,
		cwd:         fsext.DirTrim(fsext.PrettyPath(h.com.Workspace.WorkingDir()), dirTrimLimit),
		git:         gitSegment(h.com),
	}
	if session != nil {
		state.sessionID = session.ID
		state.tokens = int64(session.CompletionTokens + session.PromptTokens)
		state.estimated = session.EstimatedUsage
		state.goalActive = session.Goal.Active()
	}
	if model != nil {
		state.hasModel = true
		state.modelID = model.ID
		state.usable = h.com.Config().UsableContextWindowFor(agentCfg.Model)
	}
	return state
}

// renderLine renders the compact status line: the two halves flush left
// and right with the gap spread between them; when the halves do not
// fit, the left side shrinks first so the usage and model stay visible,
// then whatever still overflows is truncated. The result carries the
// wrapper's horizontal padding.
func (h *header) renderLine(width int, state headerState) string {
	availWidth := width - leftPadding - rightPadding
	left, right := renderHeaderState(h.com.Styles, state)

	gap := availWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		maxLeft := max(0, availWidth-lipgloss.Width(right)-1)
		left = ansi.Truncate(left, maxLeft, "…")
		if lipgloss.Width(left)+lipgloss.Width(right) > availWidth {
			right = ansi.Truncate(right, max(0, availWidth-lipgloss.Width(left)), "…")
		}
		gap = availWidth - lipgloss.Width(left) - lipgloss.Width(right)
	}

	line := left + strings.Repeat(" ", max(gap, 0)) + right

	return h.com.Styles.Header.Wrapper.Padding(0, rightPadding, 0, leftPadding).Render(line)
}

// renderHeaderState renders the two halves of the compact status line:
// the left (breadcrumb when viewing a child session, working directory and
// git state) and the right (LSP errors and context usage with model, or
// just the model while no session exists yet).
func renderHeaderState(t *styles.Styles, state headerState) (left, right string) {
	// Left: working directory and git state.
	var leftParts []string
	if state.breadcrumb != "" {
		leftParts = append(leftParts, state.breadcrumb)
	}
	leftParts = append(leftParts, t.Header.WorkingDir.Render(state.cwd))
	if state.git != "" {
		leftParts = append(leftParts, state.git)
	}

	// Right: diagnostics and context usage with the model ID.
	var rightParts []string
	// An active goal means the agent will keep taking turns on its own,
	// which is worth knowing before typing into the session.
	if state.goalActive {
		rightParts = append(rightParts, t.Header.Goal.Render("◎ goal"))
	}
	// Diagnostics are shown broken down by severity, the same rendered
	// form the LSP section uses; the all-clear case renders nothing.
	if diagnostics := lspDiagnostics(t, severityCounts(state.diagnostics)); diagnostics != "" {
		rightParts = append(rightParts, diagnostics)
	}

	if state.hasModel {
		// Measured against the usable window, not the raw one: max_tokens is
		// reserved from the same window, so a percentage of the raw number
		// reads lower than the share of the budget actually spent.
		if state.sessionID != "" && state.usable > 0 {
			percentage := (float64(state.tokens) / float64(state.usable)) * 100
			// The model ID rides beside the context percentage so the
			// line shows what is answering, not just how full it is.
			percentageText := fmt.Sprintf("%d%% %s", int(percentage), state.modelID)
			if state.estimated {
				percentageText = "~" + percentageText
			}
			rightParts = append(rightParts, t.Header.Percentage.Render(percentageText))
		} else {
			// No session yet (landing): the line still shows what will
			// answer, just without a context percentage.
			rightParts = append(rightParts, t.Header.Percentage.Render(state.modelID))
		}
	}

	dot := t.Header.Separator.Render(" • ")
	return strings.Join(leftParts, dot), dot + strings.Join(rightParts, dot)
}
