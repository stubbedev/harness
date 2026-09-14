package extensions

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
)

//go:embed job_tool.md
var jobToolDescription string

// JobToolName is the tool the model uses to collect what background jobs
// produced.
const JobToolName = "extension_jobs"

// maxJobWaitSeconds caps how long a collect may block, so waiting on a
// job cannot pin a turn.
const maxJobWaitSeconds = 300

// jobTool exposes the job queue to the model. It is only added to the
// tool set when at least one extension registered a job handler.
type jobTool struct {
	host *Host
	opts fantasy.ProviderOptions
}

// JobToolParams is the tool's input.
type JobToolParams struct {
	Action string `json:"action" description:"list (every job and its state), result (collect finished results), or cancel"`
	ID     string `json:"id,omitempty" description:"result/cancel: the job ID. result without an ID collects every finished result"`
	Wait   int    `json:"wait,omitempty" description:"result with an ID: seconds to wait for a job that is still running (default 0)"`
}

func (t *jobTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        JobToolName,
		Description: jobToolDescription,
		Parameters: map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "result", "cancel"},
				"description": "list, result, or cancel",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "The job ID, as returned when the job was started.",
			},
			"wait": map[string]any{
				"type":        "integer",
				"description": "Seconds to wait for a running job before giving up. Only used by result with an id.",
			},
		},
		Required: []string{"action"},
		Parallel: true,
	}
}

func (t *jobTool) ProviderOptions() fantasy.ProviderOptions { return t.opts }

func (t *jobTool) SetProviderOptions(opts fantasy.ProviderOptions) { t.opts = opts }

func (t *jobTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var params JobToolParams
	if err := unmarshalParams(call.Input, &params); err != nil {
		return fantasy.NewTextErrorResponse("invalid arguments: " + err.Error()), nil
	}

	switch params.Action {
	case "list":
		return fantasy.NewTextResponse(renderJobList(t.host.Jobs())), nil
	case "result":
		return t.result(ctx, params)
	case "cancel":
		if params.ID == "" {
			return fantasy.NewTextErrorResponse("cancel needs an id"), nil
		}
		if !t.host.jobs.cancel(params.ID) {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("job %q is not running", params.ID)), nil
		}
		return fantasy.NewTextResponse("Canceled " + params.ID + "."), nil
	default:
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"unknown action %q. Available: list, result, cancel", params.Action)), nil
	}
}

// result collects one job, or drains every finished one.
func (t *jobTool) result(ctx context.Context, params JobToolParams) (fantasy.ToolResponse, error) {
	if params.ID == "" {
		finished := t.host.jobs.drain()
		if len(finished) == 0 {
			return fantasy.NewTextResponse("No finished jobs are waiting to be collected."), nil
		}
		return fantasy.NewTextResponse(renderJobResults(finished)), nil
	}

	job, ok := t.host.jobs.get(params.ID)
	if !ok {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("no job %q", params.ID)), nil
	}

	if job.State == JobRunning && params.Wait > 0 {
		seconds := min(params.Wait, maxJobWaitSeconds)
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
		defer cancel()
		job, _ = t.host.jobs.wait(waitCtx, params.ID)
	} else if job.State != JobRunning {
		job, _ = t.host.jobs.collect(params.ID)
	}

	return fantasy.NewTextResponse(renderJobResults([]Job{job})), nil
}

// renderJobList renders the queue at a glance.
func renderJobList(jobs []Job) string {
	if len(jobs) == 0 {
		return "No extension jobs have been started."
	}
	var b strings.Builder
	for _, job := range jobs {
		fmt.Fprintf(
			&b,
			"%s  %s:%s  %s  %s",
			job.ID,
			job.Extension,
			job.Name,
			job.State,
			job.Age().Round(time.Second),
		)
		if job.Collected {
			b.WriteString("  (collected)")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderJobResults renders finished jobs with what they produced.
func renderJobResults(jobs []Job) string {
	var b strings.Builder
	for i, job := range jobs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%s  %s:%s  %s  %s\n",
			job.ID, job.Extension, job.Name, job.State, job.Age().Round(time.Second))
		switch {
		case job.State == JobRunning:
			b.WriteString("Still running.")
		case job.Err != "":
			b.WriteString(job.Err)
		case job.Result == "":
			b.WriteString("(no output)")
		default:
			b.WriteString(job.Result)
		}
	}
	return b.String()
}
