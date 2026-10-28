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
	var params tools.TimerParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		params.Seconds = 0
	}
	return renderTimedResult(sty, width, opts, timerDuration(params.Seconds), true)
}

// timerDuration humanises a timer's armed length. Zero renders empty:
// the input may still be streaming, so no length is known yet.
func timerDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	return common.FormatDuration(time.Duration(seconds) * time.Second)
}
