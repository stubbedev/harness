package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/verification"
)

const VerificationToolName = "verify"

type verificationTool struct {
	runner       *verification.Runner
	changedPaths func(context.Context) ([]string, error)
	retain       func(context.Context, verification.Result) error
	opts         fantasy.ProviderOptions
}

func NewVerificationTool(runner *verification.Runner, changedPaths func(context.Context) ([]string, error), retain func(context.Context, verification.Result) error) fantasy.AgentTool {
	return &verificationTool{runner: runner, changedPaths: changedPaths, retain: retain}
}

func (t *verificationTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        VerificationToolName,
		Description: "Run explicitly configured verification checks applicable to the files changed in this session. Commands, arguments, timeouts and input paths come only from trusted configuration and the execution ledger, not tool arguments. Returns passed, failed, skipped or blocked with bounded output and workspace revision fingerprints. Run again after relevant edits; never claim skipped or blocked checks passed.",
		Parameters:  map[string]any{},
	}
}

func (t *verificationTool) ProviderOptions() fantasy.ProviderOptions        { return t.opts }
func (t *verificationTool) SetProviderOptions(opts fantasy.ProviderOptions) { t.opts = opts }

func (t *verificationTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var params map[string]json.RawMessage
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil || params == nil || len(params) != 0 {
		return fantasy.NewTextErrorResponse("verify accepts an empty object only; commands and changed paths cannot be supplied by the model"), nil
	}
	result := verification.Result{Status: verification.Blocked, StartedAt: time.Now()}
	if t.runner == nil || t.changedPaths == nil {
		result.Reason = "verification is not configured"
	} else if paths, err := t.changedPaths(ctx); err != nil {
		result.Reason = fmt.Sprintf("determine changed paths: %v", err)
	} else {
		result = t.runner.Run(ctx, paths)
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
	if t.retain != nil {
		if err := t.retain(ctx, result); err != nil {
			result.Status = verification.Blocked
			result.Reason = fmt.Sprintf("retain verification result: %v", err)
		}
	}
	content, err := json.Marshal(result)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	response := fantasy.NewTextResponse(string(content))
	response.IsError = result.Status == verification.Failed || result.Status == verification.Blocked
	response.Metadata = result.Metadata()
	return response, nil
}
