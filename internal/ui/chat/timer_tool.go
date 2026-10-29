package chat

import (
	"encoding/json"
	"time"

	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// TimerToolRenderContext renders the timer tool: the call that arms a
// one-shot timer and answers at once. Its header carries the armed
// length so the call reads as the wait it sets up.
type TimerToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (t *TimerToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return renderTimedResult(sty, width, opts, timerLengthSummary(opts.ToolCall.Input), true)
}

// timerLengthSummary is a timer call's armed length, parsed from its
// input. The header and the collapsed group's one-liner both read it,
// so the same call says the same thing wherever it renders.
func timerLengthSummary(input string) string {
	var params tools.TimerParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return ""
	}
	return timerDuration(params.Seconds)
}

// timerDuration humanises a timer's armed length. Zero renders empty:
// the input may still be streaming, so no length is known yet.
func timerDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	return common.FormatDuration(time.Duration(seconds) * time.Second)
}
