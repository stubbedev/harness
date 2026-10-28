package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/stubbedev/harness/internal/toolname"

	"charm.land/fantasy"
)

//go:embed timer.md
var timerDescription string

// TimerToolName is the wire name of the tool that arms a primed timer.
const TimerToolName = toolname.Timer

const (
	// MinTimerSeconds and MaxTimerSeconds bound a timer's duration. The
	// description names them, the tool rejects anything outside them and
	// the timer registry holds the same bound, so the model reads one
	// number wherever the bound surfaces.
	MinTimerSeconds = 1
	MaxTimerSeconds = 600
)

// Timer is what arming a timer reports about it: the id a later wait
// names, how long it was armed for and when it fires.
type Timer struct {
	ID       string
	Duration time.Duration
	FiresAt  time.Time
}

// TimerArmer arms a timer that outlives the call asking for it. The
// coordinator is the only implementation; a nil armer means the tool
// cannot arm anything.
type TimerArmer interface {
	ArmTimer(ctx context.Context, sessionID string, seconds int) (Timer, error)
}

// TimerParams is the tool's input.
type TimerParams struct {
	Seconds int `json:"seconds" description:"How long to wait, in whole seconds, from 1 to 600."`
}

// NewTimerTool builds the tool that arms a one-shot timer and answers at
// once: the model does not sleep or poll, it arms and carries on, and
// the result arrives through the wait tool when the timer fires.
func NewTimerTool(armer TimerArmer) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		TimerToolName,
		timerDescription,
		func(ctx context.Context, params TimerParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if armer == nil {
				return fantasy.NewTextErrorResponse("timers are unavailable: this agent has no timer registry"), nil
			}
			if params.Seconds < MinTimerSeconds || params.Seconds > MaxTimerSeconds {
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"seconds must be between %d and %d, got %d",
					MinTimerSeconds, MaxTimerSeconds, params.Seconds,
				)), nil
			}
			sessionID, err := SessionIDOrError(ctx, "arming a timer")
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			timer, err := armer.ArmTimer(ctx, sessionID, params.Seconds)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			var sb strings.Builder
			fmt.Fprintf(
				&sb,
				"[timer %s armed for %s; it fires at %s]",
				timer.ID, timer.Duration, timer.FiresAt.Format(time.RFC3339),
			)
			sb.WriteString("\nIts result arrives through the wait tool, with no polling needed.")
			return fantasy.NewTextResponse(sb.String()), nil
		},
	)
}
