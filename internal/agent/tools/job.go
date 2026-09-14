package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"charm.land/fantasy"
)

const JobToolName = "job"

//go:embed job.md
var jobDescription string

// JobParams covers both things you can do with a background shell. They
// share the shell ID and differ by one boolean's worth of intent, which
// is not enough to justify two schemas.
type JobParams struct {
	Action  string `json:"action" description:"output (read what it has printed) or kill (terminate it)"`
	ShellID string `json:"shell_id" description:"The background shell's ID, returned when it was started"`
	Wait    bool   `json:"wait,omitempty" description:"output only: block until the shell finishes instead of returning what it has printed so far"`
}

// NewJobTool folds reading and killing a background shell into one tool,
// forwarding to the implementation each action had when it was its own.
func NewJobTool() fantasy.AgentTool {
	output, kill := NewJobOutputTool(), NewJobKillTool()
	return fantasy.NewAgentTool(
		JobToolName,
		jobDescription,
		func(ctx context.Context, params JobParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			var (
				tool  fantasy.AgentTool
				input any
			)
			switch params.Action {
			case "output":
				tool, input = output, JobOutputParams{ShellID: params.ShellID, Wait: params.Wait}
			case "kill":
				tool, input = kill, JobKillParams{ShellID: params.ShellID}
			default:
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"unknown action %q. Available: output, kill", params.Action)), nil
			}
			encoded, err := json.Marshal(input)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("job %s: %w", params.Action, err)
			}
			call.Input = string(encoded)
			call.Name = tool.Info().Name
			return tool.Run(ctx, call)
		},
	)
}
