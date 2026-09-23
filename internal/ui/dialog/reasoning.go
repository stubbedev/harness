package dialog

import (
	"errors"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
)

// ReasoningID is the identifier for the reasoning effort dialog.
const ReasoningID ID = "reasoning"

const (
	reasoningDialogMinHeight = 8
	reasoningDialogMaxHeight = 16
)

// NewReasoning creates a new reasoning effort dialog for the coder
// agent's current model.
func NewReasoning(com *common.Common) (Dialog, error) {
	cfg := com.Config()
	agentCfg, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return nil, errors.New("agent configuration not found")
	}

	selectedModel := cfg.Models[agentCfg.Model]
	model := cfg.GetModelByType(agentCfg.Model)
	if model == nil {
		return nil, errors.New("model configuration not found")
	}

	if len(model.ReasoningLevels) == 0 {
		return nil, errors.New("no reasoning levels available")
	}

	current := selectedModel.ReasoningEffort
	if current == "" {
		current = catalog.DefaultReasoningLevel(model.ReasoningLevels)
	}

	options := make([]pickerOption, 0, len(model.ReasoningLevels))
	for _, effort := range model.ReasoningLevels {
		options = append(options, pickerOption{value: effort, title: common.FormatReasoningEffort(effort)})
	}

	return newSimplePicker(com, ReasoningID, "Select Reasoning Effort", reasoningDialogMinHeight, reasoningDialogMaxHeight,
		options, current, func(effort string) Action {
			return ActionSelectReasoningEffort{Effort: effort}
		}), nil
}
