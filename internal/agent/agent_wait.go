package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
)

const (
	// defaultWaitTimeoutSeconds bounds a wait when the model does not ask
	// for one, so a hung background child cannot wedge the orchestrator's
	// turn.
	defaultWaitTimeoutSeconds = 600
	// maxWaitTimeoutSeconds caps an explicit timeout to keep a runaway
	// request from pinning the turn for hours.
	maxWaitTimeoutSeconds = 3600
)

// waitForSubagents is the `agent` tool with no prompt: it blocks until the
// named background handles finish (or a message arrives from one of them,
// or the timeout expires) and hands back their results. With no handles
// named it waits on every background agent this session has dispatched.
// Results are kept, so waiting again on a finished handle returns its
// result again.
func (c *coordinator) waitForSubagents(ctx context.Context, parentSession string, requested []string, timeoutSeconds *int) fantasy.ToolResponse {
	// Deduplicate while preserving order: the response lists each handle
	// once, in the order asked for.
	handleSet := make(map[string]bool, len(requested))
	var handles []string
	for _, h := range requested {
		if h == "" || handleSet[h] {
			continue
		}
		handleSet[h] = true
		handles = append(handles, h)
	}
	if len(handles) == 0 {
		for _, h := range c.knownBackgroundHandles(parentSession) {
			handleSet[h] = true
			handles = append(handles, h)
		}
	}
	if len(handles) == 0 {
		return fantasy.NewTextErrorResponse("nothing to wait for: this session has no background agents; give a prompt to dispatch one")
	}

	var runs []*backgroundRun
	var unknown []string
	for _, h := range handles {
		run, ok := c.backgroundRunFor(parentSession, h)
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
			strings.Join(c.knownBackgroundHandles(parentSession), ", "),
		))
	}

	timeout := defaultWaitTimeoutSeconds
	if timeoutSeconds != nil {
		timeout = min(max(*timeoutSeconds, 0), maxWaitTimeoutSeconds)
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()
	// A user prompt queued while this wait runs must interject, not wait
	// out the timeout: baseline the queue's arrival epoch and watch it.
	queueBaseline := c.queueArrivalEpoch(parentSession)

	for {
		// Fetch the wakeup channels before draining: record/finish notify
		// by closing them under the same lock the drains read, so an event
		// that lands between the check and the select still wakes this loop
		// (see liveInbox; the queue signals close-and-replace the same way).
		signal := c.liveInboxSignalChan(parentSession)
		qsig := c.queueArrivalChan(parentSession)

		msgs := c.drainLiveInboxFrom(parentSession, handleSet)
		allFinished := true
		for _, r := range runs {
			if !r.isFinished() {
				allFinished = false
				break
			}
		}
		queuedArrivals := c.queueArrivalEpoch(parentSession) - queueBaseline

		switch {
		case allFinished:
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, "All waited agents finished."))
		case len(msgs) > 0:
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, "Returned early: new message(s) arrived; some agents are still running."))
		case queuedArrivals > 0:
			// The queued prompt stays queued: the next PrepareStep folds it
			// into the next provider request, exactly as it would between
			// steps. Ending the wait here only hurries the model to it.
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, fmt.Sprintf(
				"Returned early: %d user prompt(s) were queued while waiting; they will be delivered as the next user message, before your next request.", queuedArrivals)))
		case timeout == 0:
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, "Snapshot (timeout 0): not all agents have finished yet."))
		case ctx.Err() != nil:
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, "Interrupted: wait cancelled before all agents finished."))
		case !time.Now().Before(deadline):
			return fantasy.NewTextResponse(renderWaitResult(runs, msgs, fmt.Sprintf("Timed out after %s: some agents are still running.", time.Duration(timeout)*time.Second)))
		}

		select {
		case <-signal:
		case <-qsig:
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
