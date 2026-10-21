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

// waitForSubagents is the `agent` tool with no prompt: it blocks until any
// of the named background handles finishes (or a message arrives from one
// of them, or the timeout expires) and hands back the results of every
// waited handle that finished since it was last collected, with the state
// of the rest, so the caller can dispatch more work at once and wait again
// for the others. Each result is returned exactly once. With no handles
// named it waits on every background agent this session has dispatched
// whose result has not been collected yet.
func (c *coordinator) waitForSubagents(ctx context.Context, parentSession string, requested []string, timeoutSeconds *int) (response fantasy.ToolResponse) {
	handles := uniqueHandles(requested)
	if len(handles) == 0 {
		known := c.subagentRuns.handles(parentSession)
		if len(known) == 0 {
			return fantasy.NewTextErrorResponse("nothing to wait for: this session has no background agents; give a prompt to dispatch one")
		}
		handles = c.uncollectedBackgroundHandles(parentSession, known)
		if len(handles) == 0 {
			return fantasy.NewTextErrorResponse("nothing to wait for: every background agent's result was already returned by an earlier wait; give a prompt to dispatch one")
		}
	}

	runs, errResp := c.resolveBackgroundRuns(parentSession, handles)
	if errResp != nil {
		return *errResp
	}
	defer func() {
		response = withBackgroundMetadata(response, runs)
	}()
	handleSet := make(map[string]bool, len(handles))
	for _, h := range handles {
		handleSet[h] = true
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
		collected := make(map[*backgroundRun]bool)
		running := 0
		for _, r := range runs {
			if r.collect() {
				collected[r] = true
			} else if !r.isFinished() {
				running++
			}
		}
		queuedArrivals := c.queueArrivalEpoch(parentSession) - queueBaseline
		render := func(outcome string) fantasy.ToolResponse {
			return fantasy.NewTextResponse(renderWaitResult(runs, collected, msgs, outcome))
		}

		switch {
		case len(collected) > 0 && running > 0:
			return render(fmt.Sprintf("%d agent(s) finished; %d still running. Dispatch further work now if you have it, then wait again to collect the rest.", len(collected), running))
		case len(collected) > 0:
			return render("All waited agents finished.")
		case running == 0:
			return render("Every waited agent had already finished; its result was returned by an earlier wait.")
		case len(msgs) > 0:
			return render("Returned early: new message(s) arrived; the agents are still running.")
		case queuedArrivals > 0:
			// The queued prompt stays queued: the next PrepareStep folds it
			// into the next provider request, exactly as it would between
			// steps. Ending the wait here only hurries the model to it.
			return render(fmt.Sprintf(
				"Returned early: %d user prompt(s) were queued while waiting; they will be delivered as the next user message, before your next request.", queuedArrivals))
		case timeout == 0:
			return render("Snapshot (timeout 0): no waited agent has finished yet.")
		case ctx.Err() != nil:
			return render("Interrupted: wait cancelled before any agent finished.")
		case !time.Now().Before(deadline):
			return render(fmt.Sprintf("Timed out after %s: the agents are still running.", time.Duration(timeout)*time.Second))
		}

		select {
		case <-signal:
		case <-qsig:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
}

func (c *coordinator) uncollectedBackgroundHandles(parentSession string, handles []string) []string {
	var uncollected []string
	for _, h := range handles {
		if run, ok := c.subagentRuns.lookupByHandle(parentSession, h); ok && !run.isCollected() {
			uncollected = append(uncollected, h)
		}
	}
	return uncollected
}

// renderWaitResult reports each handle's state, the results this call
// collected, and any messages drained by this call. A result an earlier
// wait already returned is not repeated.
func renderWaitResult(runs []*backgroundRun, collected map[*backgroundRun]bool, msgs []SubagentInboxMessage, outcome string) string {
	var b strings.Builder
	b.WriteString(outcome)
	for _, r := range runs {
		finished, status, result, resultErr := r.snapshot()
		switch {
		case !finished && r.isQueued():
			fmt.Fprintf(&b, "\n\n%s (%s): queued for a concurrency slot", r.agentName, r.handle)
			continue
		case !finished:
			fmt.Fprintf(&b, "\n\n%s (%s): still running", r.agentName, r.handle)
			continue
		case !collected[r]:
			fmt.Fprintf(&b, "\n\n%s (%s): %s; result already returned by an earlier wait", r.agentName, r.handle, status)
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
