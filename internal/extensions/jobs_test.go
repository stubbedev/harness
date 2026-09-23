package extensions_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/extensions"
)

// findTool returns the named tool from the host's tool set.
func findTool(t *testing.T, host *extensions.Host, name string) fantasy.AgentTool {
	t.Helper()
	for _, tool := range host.Tools() {
		if tool.Info().Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not in the tool set", name)
	return nil
}

// runJobTool calls the job-collection tool with the given params.
func runJobTool(t *testing.T, host *extensions.Host, params extensions.JobToolParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)

	rsp, err := findTool(t, host, extensions.JobToolName).Run(t.Context(), fantasy.ToolCall{
		ID:    "call",
		Name:  extensions.JobToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	return rsp
}

func TestJobOutlivesTheCallThatStartedIt(t *testing.T) {
	t.Parallel()

	// The server blocks until the test releases it, so the job is
	// provably still running after the tool call that started it has
	// already returned.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte("slow answer"))
	}))
	t.Cleanup(server.Close)

	root := writeExtension(t, "slow", `
harness.register_job({
  name = "fetch",
  description = "Fetches something slow",
  handler = function(args)
    local rsp = harness.http.request({ url = args.url })
    return { body = rsp.body, status = rsp.status }
  end,
})

harness.register_tool({
  name = "start_fetch",
  handler = function()
    return harness.jobs.start("fetch", { url = URL })
  end,
})
`)
	prependGlobal(t, root, "slow", "URL", server.URL)

	host := newHost(t, []string{root}, func(o *extensions.Options) {
		o.HTTPClient = server.Client()
	})

	// The tool returns the job ID straight away, while the request is
	// still blocked on the server.
	started := time.Now()
	rsp := runTool(t, host, "start_fetch")
	require.Less(t, time.Since(started), 2*time.Second)
	jobID := rsp.Content
	require.NotEmpty(t, jobID)

	jobs := host.Jobs()
	require.Len(t, jobs, 1)
	require.Equal(t, extensions.JobRunning, jobs[0].State)
	require.Equal(t, "slow", jobs[0].Extension)

	// Nothing to collect while it runs.
	require.Contains(t, runJobTool(t, host, extensions.JobToolParams{Action: "result"}).Content,
		"No finished jobs")

	close(release)

	// Waiting on the job hands back what it produced.
	collected := runJobTool(t, host, extensions.JobToolParams{Action: "result", ID: jobID, Wait: 10})
	require.False(t, collected.IsError)
	require.Contains(t, collected.Content, "slow answer")
	require.Contains(t, collected.Content, string(extensions.JobDone))

	// And it is only handed out once.
	require.Contains(t, runJobTool(t, host, extensions.JobToolParams{Action: "result"}).Content,
		"No finished jobs")
}

func TestJobQueueDrains(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "queued", `
harness.register_job({
  name = "double",
  handler = function(args) return tostring(args.n * 2) end,
})

harness.register_tool({
  name = "start_double",
  parameters = { n = { type = "number", description = "number to double" } },
  handler = function(input) return harness.jobs.start("double", { n = input.n }) end,
})
`)

	host := newHost(t, []string{root})
	tool := findTool(t, host, "start_double")

	for _, n := range []string{"2", "3"} {
		rsp, err := tool.Run(t.Context(), fantasy.ToolCall{
			ID: "c", Name: "start_double", Input: `{"n":` + n + `}`,
		})
		require.NoError(t, err)
		require.NotEmpty(t, rsp.Content)
	}

	require.Eventually(t, func() bool {
		for _, job := range host.Jobs() {
			if job.State == extensions.JobRunning {
				return false
			}
		}
		return len(host.Jobs()) == 2
	}, 10*time.Second, 10*time.Millisecond)

	drained := runJobTool(t, host, extensions.JobToolParams{Action: "result"})
	require.Contains(t, drained.Content, "4")
	require.Contains(t, drained.Content, "6")

	listed := runJobTool(t, host, extensions.JobToolParams{Action: "list"})
	require.Contains(t, listed.Content, "queued:double")
	require.Contains(t, listed.Content, "(collected)")
}

func TestJobFailureIsReported(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "failing", `
harness.register_job({
  name = "boom",
  handler = function() error("job exploded") end,
})
harness.register_tool({
  name = "start_boom",
  handler = function() return harness.jobs.start("boom") end,
})
`)

	host := newHost(t, []string{root})
	jobID := runTool(t, host, "start_boom").Content

	result := runJobTool(t, host, extensions.JobToolParams{Action: "result", ID: jobID, Wait: 10})
	require.Contains(t, result.Content, string(extensions.JobFailed))
	require.Contains(t, result.Content, "job exploded")
}

func TestJobCancel(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)

	root := writeExtension(t, "cancelable", `
harness.register_job({
  name = "hang",
  handler = function() return harness.http.request({ url = URL }).body end,
})
harness.register_tool({
  name = "start_hang",
  handler = function() return harness.jobs.start("hang") end,
})
`)
	prependGlobal(t, root, "cancelable", "URL", server.URL)

	host := newHost(t, []string{root}, func(o *extensions.Options) {
		o.HTTPClient = server.Client()
	})
	jobID := runTool(t, host, "start_hang").Content

	canceled := runJobTool(t, host, extensions.JobToolParams{Action: "cancel", ID: jobID})
	require.False(t, canceled.IsError)

	require.Eventually(t, func() bool {
		for _, job := range host.Jobs() {
			if job.ID == jobID {
				return job.State != extensions.JobRunning
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond)

	// Cancelling something that is no longer running says so.
	again := runJobTool(t, host, extensions.JobToolParams{Action: "cancel", ID: jobID})
	require.True(t, again.IsError)
}

func TestJobToolOnlyExistsWhenJobsAreRegistered(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "jobless", `
harness.register_tool({ name = "plain", handler = function() return "" end })
`)

	for _, tool := range newHost(t, []string{root}).Tools() {
		require.NotEqual(t, extensions.JobToolName, tool.Info().Name)
	}
}

func TestJobStartRejectsUnknownName(t *testing.T) {
	t.Parallel()

	root := writeExtension(t, "unknownjob", `
harness.register_job({ name = "known", handler = function() return "" end })
harness.register_tool({
  name = "start_unknown",
  handler = function() return harness.jobs.start("nope") end,
})
`)

	rsp := runTool(t, newHost(t, []string{root}), "start_unknown")
	require.True(t, rsp.IsError)
	require.Contains(t, rsp.Content, `registers no job "nope"`)
}

// One extension must not see, collect or cancel another's jobs.
func TestJobsAreScopedToTheirExtension(t *testing.T) {
	t.Parallel()

	owner := writeExtension(t, "owner", `
harness.register_job({ name = "work", handler = function() return "done" end })
harness.register_tool({
  name = "start_work",
  handler = function() return harness.jobs.start("work", {}) end,
})
`)
	other := writeExtension(t, "other", `
harness.register_tool({
  name = "peek",
  parameters = { id = { type = "string", description = "job id" } },
  handler = function(input)
    local status = harness.jobs.status(input.id)
    return string.format("results=%d status=%s cancel=%s",
      #harness.jobs.results(), tostring(status), tostring(harness.jobs.cancel(input.id)))
  end,
})
`)

	host := newHost(t, []string{owner, other})
	id := runTool(t, host, "start_work").Content
	require.NotEmpty(t, id)
	require.Eventually(t, func() bool {
		jobs := host.Jobs()
		return len(jobs) == 1 && jobs[0].State != extensions.JobRunning
	}, 10*time.Second, 10*time.Millisecond)

	rsp, err := findTool(t, host, "peek").Run(t.Context(), fantasy.ToolCall{
		ID: "p", Name: "peek", Input: `{"id":"` + id + `"}`,
	})
	require.NoError(t, err)
	require.Equal(t, "results=0 status=nil cancel=false", rsp.Content)

	drained := runJobTool(t, host, extensions.JobToolParams{Action: "result"})
	require.Contains(t, drained.Content, "done", "the other extension must not have collected the result")
}
