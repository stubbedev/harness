package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// WaitToolRenderContext renders the wait tool: the call that blocks
// until named background work - jobs, timers and background agents -
// finishes, and reports what finished and what is still outstanding.
// Its header names what it waits on, so the transcript says what the
// turn is doing; the report is its body.
type WaitToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (w *WaitToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	var params tools.WaitParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)
	return renderTimedResult(sty, width, opts, waitTarget(params), false)
}

// DispatcherWaitRenderContext renders the dispatcher tool's historical
// waiting form: the call with no prompt that blocks until background
// subagents report back. While pending it says how many agents it is
// still waiting on; once settled its header carries the wait's outcome
// and its body the collected reports.
type DispatcherWaitRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (w *DispatcherWaitRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
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

// isWaitRenderer reports whether r renders a wait call: the built-in
// wait tool or the dispatcher's historical waiting form. Both name the
// call by what it is doing, so the generic "Waiting for tool response"
// line would only repeat the header.
func isWaitRenderer(r ToolRenderer) bool {
	switch r.(type) {
	case *WaitToolRenderContext, *DispatcherWaitRenderContext:
		return true
	}
	return false
}

// waitTarget summarises what a wait call watches: every piece of work
// the session still has outstanding when none is named, otherwise the
// ids it was given.
func waitTarget(params tools.WaitParams) string {
	if len(params.WaitFor) == 0 {
		return "everything outstanding"
	}
	return strings.Join(params.WaitFor, ", ")
}

// waitToolLabel names the wait tool's call: the tense follows the run,
// like every other progressive label.
func waitToolLabel(running bool) string {
	if running {
		return "Waiting"
	}
	return "Waited"
}

// waitLabel names a historical dispatcher wait call: the live agent
// count while it runs, the past tense once the wait returned. It is the
// call's [ToolMessageItem.DisplayName], so every surface shows it.
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
