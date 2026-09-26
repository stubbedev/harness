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
	label := opts.Name
	if opts.Status == ToolStatusRunning {
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
// tense once the wait returned. It is the call's
// [ToolMessageItem.DisplayName], so every surface shows it.
func waitLabel(running bool, waiting int) string {
	if running {
		if waiting > 0 {
			return fmt.Sprintf("Waiting for %d agent%s", waiting, pluralAgents(waiting))
		}
		return "Waiting for agents"
	}
	return "Waited for agents"
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
