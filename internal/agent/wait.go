package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"

	"github.com/stubbedev/harness/internal/agent/tools"
)

// waitTargets is the work one wait call watches, resolved once before it
// starts: whatever its ids named, or everything the session still has
// outstanding.
type waitTargets struct {
	jobs   []*backgroundJob
	timers []*sessionTimer
	runs   []*backgroundRun
}

// Wait implements tools.Waiter: the one waiting surface for background
// shell jobs, timers and background agents. It blocks until any watched
// piece of work is done, a background agent sends a message, the user
// queues a prompt, the timeout expires or the turn is cancelled - and
// then reports the results it collected together with everything still
// outstanding, so the model can dispatch more work at once and wait
// again for the rest. Each result is handed back exactly once, ever.
//
// Nothing here owns the work it waits on: a job, timer or agent outlives
// both the call that started it and this one, and only its session's
// cancel or the coordinator's shutdown stops it.
func (c *coordinator) Wait(ctx context.Context, parentSession string, requested []string, timeoutSeconds *int) (response fantasy.ToolResponse) {
	targets, err := c.resolveWaitTargets(parentSession, requested)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error())
	}
	defer func() {
		response = withBackgroundMetadata(response, targets.runs)
	}()

	timeout := tools.DefaultWaitTimeoutSeconds
	if timeoutSeconds != nil {
		timeout = min(max(*timeoutSeconds, 0), tools.MaxWaitTimeoutSeconds)
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()
	// A user prompt queued while this wait runs must interject, not wait
	// out the timeout: baseline the queue's arrival epoch and watch it.
	queueBaseline := c.queueArrivalEpoch(parentSession)
	// Messages are drained only for the agents this wait watches; the rest
	// stay for the next step's fold.
	handleSet := make(map[string]bool, len(targets.runs))
	for _, run := range targets.runs {
		handleSet[run.handle] = true
	}

	for {
		// Fetch the wakeup channels before reading state: every producer
		// records under the lock its channel closes under, so an event that
		// lands between the read and the select still wakes this loop (see
		// liveInbox and signalMap). Jobs, timers and the inbox close and
		// replace theirs; the queue signals the same way.
		jobSignal := c.jobs.signalChan(parentSession)
		timerSignal := c.timers.signalChan(parentSession)
		inboxSignal := c.liveInboxSignalChan(parentSession)
		queueSignal := c.queueArrivalChan(parentSession)

		items, outstanding := c.waitSnapshot(targets)
		msgs := c.drainLiveInboxFrom(parentSession, handleSet)
		queuedArrivals := c.queueArrivalEpoch(parentSession) - queueBaseline
		collected := 0
		for _, item := range items {
			if item.Finished && !item.AlreadyCollected {
				collected++
			}
		}
		render := func(summary string) fantasy.ToolResponse {
			return fantasy.NewTextResponse(tools.RenderWaitReport(tools.WaitReport{
				Summary:  summary,
				Items:    items,
				Messages: waitMessages(msgs),
			}))
		}

		switch {
		case collected > 0 && outstanding > 0:
			return render(fmt.Sprintf(
				"%d finished and collected; %d still outstanding. Do other work now if you have it, then wait again for the rest.",
				collected, outstanding))
		case collected > 0:
			return render("Every waited piece of work finished; the results are below.")
		case outstanding == 0:
			return render("Nothing is outstanding: every waited result was returned by an earlier wait.")
		case len(msgs) > 0:
			return render("Returned early: new message(s) arrived; the work is still outstanding.")
		case queuedArrivals > 0:
			// The queued prompt stays queued: the next PrepareStep folds it
			// into the next provider request, exactly as it would between
			// steps. Ending the wait here only hurries the model to it.
			return render(fmt.Sprintf(
				"Returned early: %d user prompt(s) were queued while waiting; they will be delivered as the next user message, before your next request.", queuedArrivals))
		case timeout == 0:
			return render("Snapshot (timeout 0): nothing has finished yet.")
		case ctx.Err() != nil:
			return render("Interrupted: the wait was cancelled before anything finished; what is still outstanding is listed below.")
		case !time.Now().Before(deadline):
			return render(fmt.Sprintf("Timed out after %s: the work is still outstanding.", time.Duration(timeout)*time.Second))
		}

		select {
		case <-jobSignal:
		case <-timerSignal:
		case <-inboxSignal:
		case <-queueSignal:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
}

// waitSnapshot evaluates every watched target once, handing back each
// finished result no earlier wait collected, and reports how many targets
// are still outstanding.
func (c *coordinator) waitSnapshot(targets waitTargets) ([]tools.WaitItem, int) {
	items := make([]tools.WaitItem, 0, len(targets.jobs)+len(targets.timers)+len(targets.runs))
	outstanding := 0
	for _, job := range targets.jobs {
		info, delivered := job.take()
		if info.State == JobRunning {
			outstanding++
		}
		items = append(items, tools.WaitItem{
			Kind:             tools.WaitItemJob,
			ID:               info.ID,
			Subject:          info.Command,
			Status:           string(info.State),
			Finished:         info.State != JobRunning,
			AlreadyCollected: info.State != JobRunning && !delivered,
			Elapsed:          info.Elapsed,
			ExitCode:         info.ExitCode,
			Output:           info.Output,
		})
	}
	now := c.timers.now()
	for _, timer := range targets.timers {
		info, delivered := timer.take(now)
		if info.State == TimerArmed {
			outstanding++
		}
		items = append(items, tools.WaitItem{
			Kind:             tools.WaitItemTimer,
			ID:               info.ID,
			Subject:          info.Duration.String(),
			Status:           string(info.State),
			Finished:         info.State != TimerArmed,
			AlreadyCollected: info.State != TimerArmed && !delivered,
			Remaining:        info.Remaining,
		})
	}
	for _, run := range targets.runs {
		finished, status, result, resultErr, delivered := run.take()
		if !finished {
			outstanding++
		}
		items = append(items, tools.WaitItem{
			Kind:             tools.WaitItemAgent,
			ID:               run.handle,
			Subject:          run.agentName,
			Status:           string(status),
			Finished:         finished,
			AlreadyCollected: finished && !delivered,
			Queued:           !finished && run.isQueued(),
			Result:           result,
			ResultErr:        resultErr,
		})
	}
	return items, outstanding
}

// resolveWaitTargets maps ids of any kind to this session's records. With
// no ids it names every piece of work the session still has outstanding,
// one whose result an earlier wait has not collected yet; an unknown id
// fails the call before waiting, naming the ids the session owns.
func (c *coordinator) resolveWaitTargets(parentSession string, requested []string) (waitTargets, error) {
	ids := uniqueHandles(requested)
	var targets waitTargets
	if len(ids) > 0 {
		var unknown []string
		for _, id := range ids {
			if job := c.jobs.lookup(parentSession, id); job != nil {
				targets.jobs = append(targets.jobs, job)
				continue
			}
			if timer := c.timers.lookup(parentSession, id); timer != nil {
				targets.timers = append(targets.timers, timer)
				continue
			}
			if run, ok := c.subagentRuns.lookupByHandle(parentSession, id); ok {
				targets.runs = append(targets.runs, run)
				continue
			}
			unknown = append(unknown, id)
		}
		if len(unknown) > 0 {
			return waitTargets{}, fmt.Errorf(
				"unknown wait target(s): %s. Ids this session owns: %s",
				strings.Join(unknown, ", "), strings.Join(ownedWaitIDs(c, parentSession), ", "),
			)
		}
		return targets, nil
	}

	for _, job := range c.jobs.list(parentSession) {
		if info := job.info(); info.State == JobRunning || !info.Collected {
			targets.jobs = append(targets.jobs, job)
		}
	}
	now := c.timers.now()
	for _, timer := range c.timers.records(parentSession) {
		if info := timer.info(now); info.State == TimerArmed || !info.Collected {
			targets.timers = append(targets.timers, timer)
		}
	}
	for _, handle := range c.subagentRuns.handles(parentSession) {
		run, ok := c.subagentRuns.lookupByHandle(parentSession, handle)
		if ok && (!run.isFinished() || !run.isCollected()) {
			targets.runs = append(targets.runs, run)
		}
	}
	owned := ownedWaitIDs(c, parentSession)
	if len(owned) == 0 {
		return waitTargets{}, errors.New("nothing to wait for: this session has no background jobs, timers or agents; start one and wait for it when it is the next thing you can do")
	}
	if len(targets.jobs)+len(targets.timers)+len(targets.runs) == 0 {
		return waitTargets{}, fmt.Errorf(
			"nothing to wait for: every result was already returned by an earlier wait (%s); start further work to wait on",
			strings.Join(owned, ", "),
		)
	}
	return targets, nil
}

// ownedWaitIDs lists every id this session owns across the three kinds of
// background work, for the error an unknown id gets.
func ownedWaitIDs(c *coordinator, parentSession string) []string {
	var ids []string
	for _, job := range c.jobs.list(parentSession) {
		ids = append(ids, job.id)
	}
	for _, timer := range c.timers.records(parentSession) {
		ids = append(ids, timer.id)
	}
	return append(ids, c.subagentRuns.handles(parentSession)...)
}

// waitMessages adapts the drained inbox to the wait report's shape.
func waitMessages(msgs []SubagentInboxMessage) []tools.WaitMessage {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]tools.WaitMessage, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, tools.WaitMessage{AgentName: msg.AgentName, Handle: msg.Handle, Text: msg.Text})
	}
	return out
}
