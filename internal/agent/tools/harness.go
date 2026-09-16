package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/skills"
)

const HarnessToolName = "harness"

//go:embed harness.md
var harnessDescription string

// HarnessParams selects between the two ways of asking Harness about
// itself: its current state, or what it has been logging.
type HarnessParams struct {
	Action string `json:"action" description:"state (the current configuration and service health) or logs (recent internal log entries)"`
	Lines  int    `json:"lines,omitempty" description:"logs only: how many recent entries to return"`
}

// NewHarnessTool folds the two self-inspection tools into one. They are
// asked the same question from the same place -- something is not working,
// and the cause is in Harness rather than in the code -- and the answer is
// usually in both.
func NewHarnessTool(
	cfg *config.ConfigStore,
	lspManager *lsp.Manager,
	allSkills []*skills.Skill,
	activeSkills []*skills.Skill,
	skillTracker *skills.Tracker,
	host *extensions.Host,
	logFile string,
) fantasy.AgentTool {
	info, logs := NewHarnessInfoTool(cfg, lspManager, allSkills, activeSkills, skillTracker, host), NewHarnessLogsTool(logFile)
	return fantasy.NewParallelAgentTool(
		HarnessToolName,
		harnessDescription,
		func(ctx context.Context, params HarnessParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			var (
				tool  fantasy.AgentTool
				input any
			)
			switch params.Action {
			case "state":
				tool, input = info, HarnessInfoParams{}
			case "logs":
				tool, input = logs, HarnessLogsParams{Lines: params.Lines}
			default:
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"unknown action %q. Available: state, logs", params.Action)), nil
			}
			encoded, err := json.Marshal(input)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("harness %s: %w", params.Action, err)
			}
			call.Input = string(encoded)
			call.Name = tool.Info().Name
			return tool.Run(ctx, call)
		},
	)
}
