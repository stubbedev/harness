package chat

import (
	"fmt"
	"strings"

	"github.com/stubbedev/harness/internal/ui/styles"
)

// WaitToolRenderContext renders the dispatcher tool's waiting form: the
// call with no prompt that blocks until background subagents report
// back. While pending it says how many agents it is still waiting on,
// so the transcript says what the turn is doing; once settled its
// header carries the wait's outcome and its body the collected reports.
type WaitToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (w *WaitToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	running := opts.Status == ToolStatusRunning
	label := waitLabel(running, opts.WaitingAgents)
	if running {
		// The header already says the turn is waiting and on how many
		// agents; the generic "Waiting for tool response" line under it
		// would only repeat that, so a running wait is its header.
		return strings.TrimSuffix(toolHeader(sty, label, width, opts), " ")
	}
	var outcome, reports string
	if opts.Result != nil {
		outcome, reports = splitWaitResult(opts.Result.Content)
	}
	return renderStandardTool(sty, width, opts, label, func() ([]string, bool) {
		if outcome == "" {
			return nil, true
		}
		return []string{outcome}, true
	}, func() string {
		if reports == "" {
			return ""
		}
		// The reports are the agents' own write-ups, which are
		// markdown; render them as prose rather than as a
		// line-numbered file.
		return toolOutputMarkdownContent(sty, reports, width, opts.ExpandedContent)
	})
}

// waitLabel names a wait call: the live count while it runs, the past
// tense once the wait returned. The full render and the collapsed
// one-liner share it, so the two cannot disagree.
func waitLabel(running bool, waiting int) string {
	if running {
		if waiting > 0 {
			return fmt.Sprintf("Waiting for %d agent%s", waiting, pluralAgents(waiting))
		}
		return "Waiting for agents"
	}
	return "Waited for agents"
}

// liveDisplayName labels a wait call by what it is doing, the live
// agent count included, where [ToolDisplayName] could only say "Agent":
// the name alone cannot tell a wait from a dispatch, and the count is
// item state. Other calls report false and keep their display name.
func (t *baseToolMessageItem) liveDisplayName() (string, bool) {
	if _, ok := t.toolRenderer.(*WaitToolRenderContext); !ok {
		return "", false
	}
	waiting := 0
	if t.waitingAgents != nil {
		waiting = t.waitingAgents()
	}
	return waitLabel(t.EffectiveStatus() == ToolStatusRunning, waiting), true
}

// splitWaitResult separates a wait result's first line, the outcome
// ("All waited agents finished.", "Timed out after ..."), from the
// per-agent reports that follow it.
func splitWaitResult(content string) (outcome, reports string) {
	content = strings.TrimSpace(content)
	outcome, reports, _ = strings.Cut(content, "\n")
	return strings.TrimSpace(outcome), strings.TrimSpace(reports)
}

func pluralAgents(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
