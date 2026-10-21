package agent

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"
)

// uniqueHandles drops empty and repeated handles, keeping the order they
// were asked for in.
func uniqueHandles(requested []string) []string {
	seen := make(map[string]bool, len(requested))
	handles := make([]string, 0, len(requested))
	for _, h := range requested {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		handles = append(handles, h)
	}
	return handles
}

// resolveBackgroundRuns maps handles to this session's runs. An unknown
// handle fails the whole call, naming the handles the session owns.
func (c *coordinator) resolveBackgroundRuns(parentSession string, handles []string) ([]*backgroundRun, *fantasy.ToolResponse) {
	runs := make([]*backgroundRun, 0, len(handles))
	var unknown []string
	for _, h := range handles {
		run, ok := c.subagentRuns.lookupByHandle(parentSession, h)
		if !ok {
			unknown = append(unknown, h)
			continue
		}
		runs = append(runs, run)
	}
	if len(unknown) > 0 {
		resp := fantasy.NewTextErrorResponse(fmt.Sprintf(
			"unknown background agent handle(s): %s. Handles started by this session: %s",
			strings.Join(unknown, ", "),
			strings.Join(c.subagentRuns.handles(parentSession), ", "),
		))
		return nil, &resp
	}
	return runs, nil
}

// runningBackgroundRuns lists this session's background runs that have
// not finished, in handle order.
func (c *coordinator) runningBackgroundRuns(parentSession string) []*backgroundRun {
	var running []*backgroundRun
	for _, h := range c.subagentRuns.handles(parentSession) {
		if run, ok := c.subagentRuns.lookupByHandle(parentSession, h); ok && !run.isFinished() {
			running = append(running, run)
		}
	}
	return running
}

// resolveRunningTargets resolves the handles a message or cancel acts on
// and requires every one of them to still be running: acting on a
// finished agent cannot take effect, so it fails the call before anything
// is sent rather than being dropped.
func (c *coordinator) resolveRunningTargets(parentSession string, requested []string) ([]*backgroundRun, *fantasy.ToolResponse) {
	runs, errResp := c.resolveBackgroundRuns(parentSession, uniqueHandles(requested))
	if errResp != nil {
		return nil, errResp
	}
	var finished []string
	for _, run := range runs {
		if done, status, _, _ := run.snapshot(); done {
			finished = append(finished, fmt.Sprintf("%s (%s): %s", run.agentName, run.handle, status))
		}
	}
	if len(finished) > 0 {
		resp := fantasy.NewTextErrorResponse(fmt.Sprintf(
			"nothing was sent: these agents already finished and take no further steps: %s. Collect their results by calling %s with no prompt.",
			strings.Join(finished, ", "), AgentToolName,
		))
		return nil, &resp
	}
	return runs, nil
}

// describeRuns renders runs as "name (handle)" for tool responses.
func describeRuns(runs []*backgroundRun) string {
	names := make([]string, 0, len(runs))
	for _, run := range runs {
		names = append(names, fmt.Sprintf("%s (%s)", run.agentName, run.handle))
	}
	return strings.Join(names, ", ")
}

// DiscardSubagentDispatch implements SubagentDispatchDiscarder: the run
// started for childSessionID is cancelled and dropped from the session's
// background runs, so no wait collects it and no handle list names it.
// The model never saw its handle. A blocking dispatch needs nothing here:
// its run ended with the early call's context.
func (c *coordinator) DiscardSubagentDispatch(childSessionID string) {
	c.subagentRuns.discard(childSessionID)
}

// messageSubagents is the `agent` tool with a message: it delivers text
// to running background agents, each reading it as a user message before
// its next step. With no handles named it goes to every running agent
// this session dispatched.
func (c *coordinator) messageSubagents(ctx context.Context, parentSession string, requested []string, text string) (response fantasy.ToolResponse) {
	text = strings.TrimSpace(text)
	if text == "" {
		return fantasy.NewTextErrorResponse(`"message" must not be blank`)
	}
	var runs []*backgroundRun
	if len(uniqueHandles(requested)) == 0 {
		runs = c.runningBackgroundRuns(parentSession)
		if len(runs) == 0 {
			return fantasy.NewTextErrorResponse("nothing to message: this session has no running background agents")
		}
	} else {
		var errResp *fantasy.ToolResponse
		if runs, errResp = c.resolveRunningTargets(parentSession, requested); errResp != nil {
			return *errResp
		}
	}
	defer func() {
		response = withBackgroundMetadata(response, runs)
	}()

	var failed []string
	var deliveredRuns []*backgroundRun
	for _, run := range runs {
		if err := c.SteerSubagent(ctx, run.childSession, text); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s): %v", run.agentName, run.handle, err))
			continue
		}
		deliveredRuns = append(deliveredRuns, run)
	}
	var lines []string
	if len(deliveredRuns) > 0 {
		lines = append(lines, fmt.Sprintf(
			"Message delivered to %d agent(s): %s. Each reads it as a user message before its next step.",
			len(deliveredRuns), describeRuns(deliveredRuns)))
	}
	if len(failed) > 0 {
		lines = append(lines, "Not delivered: "+strings.Join(failed, "; "))
		return fantasy.NewTextErrorResponse(strings.Join(lines, "\n"))
	}
	return fantasy.NewTextResponse(lines[0])
}

// cancelSubagents is the `agent` tool with cancel: it stops the named
// running background agents. Their results, including whatever they sent
// before stopping, are still collected by waiting on the handles.
func (c *coordinator) cancelSubagents(parentSession string, requested []string) (response fantasy.ToolResponse) {
	if len(uniqueHandles(requested)) == 0 {
		return fantasy.NewTextErrorResponse(`"cancel" needs the "handles" of the agents to cancel`)
	}
	runs, errResp := c.resolveRunningTargets(parentSession, requested)
	if errResp != nil {
		return *errResp
	}
	defer func() {
		response = withBackgroundMetadata(response, runs)
	}()

	var cancelled []*backgroundRun
	var gone []string
	for _, run := range runs {
		if !c.subagentRuns.cancel(run.childSession) {
			gone = append(gone, fmt.Sprintf("%s (%s)", run.agentName, run.handle))
			continue
		}
		cancelled = append(cancelled, run)
	}
	var lines []string
	if len(cancelled) > 0 {
		lines = append(lines, fmt.Sprintf(
			"Cancelled %d agent(s): %s. Collect their results by calling %s with no prompt.",
			len(cancelled), describeRuns(cancelled), AgentToolName))
	}
	if len(gone) > 0 {
		lines = append(lines, "Already finished before the cancel landed: "+strings.Join(gone, ", "))
		return fantasy.NewTextErrorResponse(strings.Join(lines, "\n"))
	}
	return fantasy.NewTextResponse(lines[0])
}
