package chat

import (
	"fmt"

	"github.com/stubbedev/harness/internal/ui/styles"
)

// WaitToolRenderContext renders the dispatcher tool's waiting form: the
// call with no prompt that blocks until background subagents report
// back. While pending it names the agents it is still waiting on, so
// the transcript says what the turn is doing; once settled its result
// carries the collected reports.
type WaitToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (w *WaitToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return renderStandardTool(sty, width, opts, w.header(opts), func() ([]string, bool) {
		return nil, true
	}, func() string {
		return renderToolResultTextContent(sty, opts.Result.Content, toolResultContentWidths{Body: width, Diff: width}, opts.ExpandedContent)
	})
}

// header names the call: the live count while pending, the past tense
// once the wait returned.
func (w *WaitToolRenderContext) header(opts *ToolRenderOpts) string {
	if opts.IsPending() {
		if n := opts.WaitingAgents; n > 0 {
			return fmt.Sprintf("Waiting for %d agent%s", n, pluralAgents(n))
		}
		return "Waiting for agents"
	}
	return "Waited for agents"
}

func pluralAgents(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
