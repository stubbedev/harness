package tools

import (
	"context"
	_ "embed"

	"github.com/stubbedev/harness/internal/toolname"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/skills"
	"github.com/stubbedev/harness/internal/toolaction"
)

const HarnessToolName = toolname.Harness

//go:embed harness.md
var harnessDescription string

// HarnessParams selects between the two ways of asking Harness about
// itself: its current state, or what it has been logging.
type HarnessParams struct {
	Action HarnessAction `json:"action" description:"state (the current configuration and service health) or logs (recent internal log entries)"`
	Lines  int           `json:"lines,omitempty" description:"logs only: how many recent entries to return"`
}

type HarnessAction string

const (
	HarnessActionState HarnessAction = "state"
	HarnessActionLogs  HarnessAction = "logs"
)

type harnessActionFunc func(HarnessParams) fantasy.ToolResponse

// NewHarnessTool folds the two self-inspection tools into one. They are
// asked the same question from the same place -- something is not working,
// and the cause is in Harness rather than in the code -- and the answer is
// usually in both.
func NewHarnessTool(
	cfg *config.ConfigStore,
	lspManager *lsp.Manager,
	skillLists SkillLists,
	skillTracker *skills.Tracker,
	host *extensions.Host,
	logFile string,
) fantasy.AgentTool {
	actions := toolaction.New("action", map[HarnessAction]harnessActionFunc{
		HarnessActionState: func(HarnessParams) fantasy.ToolResponse {
			allSkills, activeSkills := skillLists()
			return fantasy.NewTextResponse(buildHarnessInfo(cfg, lspManager, allSkills, activeSkills, skillTracker, host))
		},
		HarnessActionLogs: func(params HarnessParams) fantasy.ToolResponse {
			return fantasy.NewTextResponse(runHarnessLogs(logFile, HarnessLogsParams{Lines: params.Lines}))
		},
	})
	return toolaction.Advertise(fantasy.NewParallelAgentTool(
		HarnessToolName,
		harnessDescription,
		func(ctx context.Context, params HarnessParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			action, err := actions.Lookup(params.Action)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return action(params), nil
		},
	), actions.Enum())
}
