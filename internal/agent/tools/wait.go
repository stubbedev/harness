package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stubbedev/harness/internal/toolname"

	"charm.land/fantasy"
)

//go:embed wait.md
var waitDescription string

// WaitToolName is the wire name of the tool that waits for background
// work - jobs, timers and background agents - to finish.
const WaitToolName = toolname.Wait

const (
	// DefaultWaitTimeoutSeconds bounds a wait when the model does not ask
	// for one, so a hung background child cannot wedge the orchestrator's
	// turn.
	DefaultWaitTimeoutSeconds = 600
	// MaxWaitTimeoutSeconds caps an explicit timeout to keep a runaway
	// request from pinning the turn for hours.
	MaxWaitTimeoutSeconds = 3600
)

// WaitItemKind names what a wait item is.
type WaitItemKind string

const (
	// WaitItemJob is a background shell job.
	WaitItemJob WaitItemKind = "job"
	// WaitItemTimer is a one-shot timer.
	WaitItemTimer WaitItemKind = "timer"
	// WaitItemAgent is a background sub-agent dispatch.
	WaitItemAgent WaitItemKind = "agent"
)

// WaitItem is one piece of work a wait reports on. The fields its kind
// does not use stay zero.
type WaitItem struct {
	Kind WaitItemKind
	// ID names the item: job-N, timer-N or a bg- handle.
	ID string
	// Subject says what the work is: a job's command, a timer's duration,
	// an agent's name.
	Subject string
	// Status is where it stands: running, exited, armed, completed.
	Status string
	// Finished reports the work is done.
	Finished bool
	// AlreadyCollected reports an earlier wait handed its result back, so
	// this one does not repeat it.
	AlreadyCollected bool
	// Queued reports an agent waiting for a concurrency slot.
	Queued bool
	// Elapsed is how long a job has run, or did run.
	Elapsed time.Duration
	// Remaining is how long an armed timer has left.
	Remaining time.Duration
	// ExitCode is a finished job's exit status, nil when it has none.
	ExitCode *int
	// Output is a job's output tail.
	Output string
	// Result is an agent's final message. ResultErr marks it a failure.
	Result    string
	ResultErr bool
}

// WaitMessage is a message a background agent sent the waiting session
// while the wait was parked.
type WaitMessage struct {
	AgentName string
	Handle    string
	Text      string
}

// WaitReport is one wait's observation: the outcome sentence, one item
// per piece of work it watched, and the messages it drained.
type WaitReport struct {
	// Summary is the leading sentence: what finished, or why the wait
	// returned early.
	Summary  string
	Items    []WaitItem
	Messages []WaitMessage
}

// Waiter is the one waiting surface for background work. It blocks until
// any named piece of work is done, or the wait is interrupted, and
// reports the collected results together with everything still
// outstanding. The coordinator is the only implementation; a nil waiter
// means the tool cannot wait on anything.
type Waiter interface {
	Wait(ctx context.Context, sessionID string, waitFor []string, timeoutSeconds *int) fantasy.ToolResponse
}

// WaitParams is the tool's input.
type WaitParams struct {
	// WaitFor names the ids to wait on, of any kind. Empty means every
	// piece of work the session still has outstanding.
	WaitFor []string `json:"wait_for,omitempty"`
	// TimeoutSeconds bounds the wait; nil means DefaultWaitTimeoutSeconds.
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`
}

// waitTool is the wait surface: it hands the call to the coordinator and
// renders the report the coordinator builds.
type waitTool struct {
	waiter Waiter
	opts   fantasy.ProviderOptions
}

// NewWaitTool builds the tool that waits for background work to finish.
func NewWaitTool(waiter Waiter) fantasy.AgentTool {
	return &waitTool{waiter: waiter}
}

func (t *waitTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        WaitToolName,
		Description: waitDescription,
		Parameters: map[string]any{
			"wait_for": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Ids of the work to wait on, of any kind: `job-N` from a background shell command, `timer-N` from the timer tool, `bg-…` from a background agent. Omit or leave empty to wait on every piece of work this session still has outstanding.",
			},
			"timeout_seconds": map[string]any{
				"type": "integer",
				"description": fmt.Sprintf(
					"How long to wait, in seconds. Default %d, maximum %d. 0 never blocks: it returns the current snapshot immediately.",
					DefaultWaitTimeoutSeconds, MaxWaitTimeoutSeconds,
				),
			},
		},
		// Nothing is required - an empty call waits on everything - but the
		// empty slice has to be explicit: a nil Required marshals to JSON
		// null, which some providers reject.
		Required: []string{},
		Parallel: true,
	}
}

func (t *waitTool) ProviderOptions() fantasy.ProviderOptions        { return t.opts }
func (t *waitTool) SetProviderOptions(opts fantasy.ProviderOptions) { t.opts = opts }

func (t *waitTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if t.waiter == nil {
		return fantasy.NewTextErrorResponse("waiting is unavailable: this agent has no background work registry"), nil
	}
	var params WaitParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.NewTextErrorResponse("invalid parameters: " + err.Error()), nil
	}
	sessionID, err := SessionIDOrError(ctx, "waiting")
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	return t.waiter.Wait(ctx, sessionID, params.WaitFor, params.TimeoutSeconds), nil
}

// RenderWaitReport renders a wait's observation for the model: the
// outcome, then each item's state with the result this wait collected,
// then the messages drained while waiting. A result an earlier wait
// already returned is named but not repeated.
func RenderWaitReport(report WaitReport) string {
	var b strings.Builder
	b.WriteString(report.Summary)
	for _, item := range report.Items {
		b.WriteString("\n\n")
		b.WriteString(renderWaitItem(item))
	}
	for i, msg := range report.Messages {
		fmt.Fprintf(&b, "\n\n[%d] Message from %s (%s):\n%s", i+1, msg.AgentName, msg.Handle, msg.Text)
	}
	b.WriteString("\n")
	return b.String()
}

func renderWaitItem(item WaitItem) string {
	switch item.Kind {
	case WaitItemJob:
		return renderJobWaitItem(item)
	case WaitItemTimer:
		return renderTimerWaitItem(item)
	default:
		return renderAgentWaitItem(item)
	}
}

func renderJobWaitItem(item WaitItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "background job %s (%s): ", item.ID, item.Subject)
	if !item.Finished {
		fmt.Fprintf(&b, "running for %s", roundWaitDuration(item.Elapsed))
	} else {
		b.WriteString(item.Status)
		if item.ExitCode != nil {
			fmt.Fprintf(&b, ", exit code %d", *item.ExitCode)
		}
		if item.Elapsed > 0 {
			fmt.Fprintf(&b, " after %s", roundWaitDuration(item.Elapsed))
		}
	}
	if item.AlreadyCollected {
		b.WriteString("; result already returned by an earlier wait")
		return b.String()
	}
	if item.Output != "" {
		b.WriteString("\n  Output (tail):\n")
		b.WriteString(indentWaitText(item.Output, "    "))
	}
	return b.String()
}

func renderTimerWaitItem(item WaitItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "timer %s (%s): %s", item.ID, item.Subject, item.Status)
	if !item.Finished {
		fmt.Fprintf(&b, "; fires in %s", roundWaitDuration(item.Remaining))
	}
	if item.AlreadyCollected {
		b.WriteString("; result already returned by an earlier wait")
	}
	return b.String()
}

func renderAgentWaitItem(item WaitItem) string {
	if !item.Finished {
		if item.Queued {
			return fmt.Sprintf("%s (%s): queued for a concurrency slot", item.Subject, item.ID)
		}
		return fmt.Sprintf("%s (%s): still running", item.Subject, item.ID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s): %s", item.Subject, item.ID, item.Status)
	if item.AlreadyCollected {
		b.WriteString("; result already returned by an earlier wait")
		return b.String()
	}
	if item.Result != "" {
		if item.ResultErr {
			fmt.Fprintf(&b, "\n  Error: %s", item.Result)
		} else {
			fmt.Fprintf(&b, "\n  Result:\n%s", item.Result)
		}
	}
	return b.String()
}

// roundWaitDuration trims a duration to a tenth of a second: the model
// needs the magnitude, not the nanoseconds.
func roundWaitDuration(d time.Duration) time.Duration {
	return d.Round(100 * time.Millisecond)
}

// indentWaitText prefixes every line of text, so a multi-line block sits
// under the item it belongs to.
func indentWaitText(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
