package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"

	"github.com/stubbedev/harness/internal/agent/tools"
)

//go:embed templates/wait_tool.md
var waitToolDescription string

const (
	// WaitToolName is the sync point for background dispatches: it blocks
	// until the named handles finish (or a message arrives, or its timeout
	// expires) and hands back their results.
	WaitToolName = "wait"

	// defaultWaitTimeout bounds a wait when the model does not ask for one,
	// so a hung background child cannot wedge the orchestrator's turn.
	defaultWaitTimeoutSeconds = 600
	// maxWaitTimeoutSeconds caps an explicit timeout to keep a runaway
	// request from pinning the turn for hours.
	maxWaitTimeoutSeconds = 3600
)

// waitTool implements the wait tool. It is exposed alongside the dispatcher
// (see buildTools) because it is only meaningful with background handles.
type waitTool struct {
	coord *coordinator
	opts  fantasy.ProviderOptions
}

func newWaitTool(coord *coordinator) *waitTool {
	return &waitTool{coord: coord}
}

func (t *waitTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        WaitToolName,
		Description: waitToolDescription,
		Parameters: map[string]any{
			"handles": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Handles returned by background `agent` calls. Results and statuses are reported for each.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("How long to wait, in seconds. Default %d, maximum %d. 0 never blocks: it returns the current snapshot immediately.", defaultWaitTimeoutSeconds, maxWaitTimeoutSeconds),
			},
		},
		Required: []string{"handles"},
		Parallel: true,
	}
}

func (t *waitTool) ProviderOptions() fantasy.ProviderOptions        { return t.opts }
func (t *waitTool) SetProviderOptions(opts fantasy.ProviderOptions) { t.opts = opts }

func (t *waitTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	parentSession := tools.GetSessionFromContext(ctx)
	if parentSession == "" {
		return fantasy.NewTextErrorResponse("session id missing from context"), nil
	}
	var params struct {
		Handles        []string `json:"handles"`
		TimeoutSeconds *int     `json:"timeout_seconds,omitempty"`
	}
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.NewTextErrorResponse("invalid parameters: " + err.Error()), nil
	}

	// Deduplicate while preserving order: the response lists each handle
	// once, in the order asked for.
	handleSet := make(map[string]bool, len(params.Handles))
	var handles []string
	for _, h := range params.Handles {
		if h == "" || handleSet[h] {
			continue
		}
		handleSet[h] = true
		handles = append(handles, h)
	}
	if len(handles) == 0 {
		return fantasy.NewTextErrorResponse(`"handles" must not be empty`), nil
	}

	var runs []*backgroundRun
	var unknown []string
	for _, h := range handles {
		run, ok := t.coord.backgroundRunFor(parentSession, h)
		if !ok {
			unknown = append(unknown, h)
			continue
		}
		runs = append(runs, run)
	}
	if len(unknown) > 0 {
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"unknown background agent handle(s): %s. Handles started by this session: %s",
			strings.Join(unknown, ", "),
			strings.Join(t.coord.knownBackgroundHandles(parentSession), ", "),
		)), nil
	}

	timeout := defaultWaitTimeoutSeconds
	if params.TimeoutSeconds != nil {
		timeout = min(max(*params.TimeoutSeconds, 0), maxWaitTimeoutSeconds)
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()

	for {
		// Fetch the wakeup channel before draining: record/finish notify by
		// closing it under the same lock the drains read, so an event that
		// lands between the check and the select still wakes this loop
		// (see liveInbox).
		signal := t.coord.liveInboxSignalChan(parentSession)

		msgs := t.coord.drainLiveInboxFrom(parentSession, handleSet)
		allFinished := true
		for _, r := range runs {
			if !r.isFinished() {
				allFinished = false
				break
			}
		}

		switch {
		case allFinished:
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, "All waited agents finished.")), nil
		case len(msgs) > 0:
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, "Returned early: new message(s) arrived; some agents are still running.")), nil
		case timeout == 0:
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, "Snapshot (timeout 0): not all agents have finished yet.")), nil
		case ctx.Err() != nil:
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, "Interrupted: wait cancelled before all agents finished.")), nil
		case !time.Now().Before(deadline):
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, fmt.Sprintf("Timed out after %s: some agents are still running.", time.Duration(timeout)*time.Second))), nil
		}

		select {
		case <-signal:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
}

// renderWaitResult reports each handle's state, the collected results of
// finished runs, and any messages drained by this call.
func renderWaitResult(runs []*backgroundRun, msgs []SubagentInboxMessage, outcome string) string {
	var b strings.Builder
	b.WriteString(outcome)
	for _, r := range runs {
		finished, status, result, resultErr := r.snapshot()
		if !finished {
			fmt.Fprintf(&b, "\n\n%s (%s): still running", r.agentName, r.handle)
			continue
		}
		fmt.Fprintf(&b, "\n\n%s (%s): %s", r.agentName, r.handle, status)
		if result != "" {
			if resultErr {
				fmt.Fprintf(&b, "\n  Error: %s", result)
			} else {
				fmt.Fprintf(&b, "\n  Result:\n%s", result)
			}
		}
	}
	for i, msg := range msgs {
		fmt.Fprintf(&b, "\n\n[%d] Message from %s (%s):\n%s", i+1, msg.AgentName, msg.Handle, msg.Text)
	}
	b.WriteString("\n")
	return b.String()
}
