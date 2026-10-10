package agent

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/openai"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

// Adaptive reasoning effort.
//
// Most of a coding turn's steps are the model reading a tool result and
// choosing the next call. The step that answers the user plans the work
// and runs at the configured effort; the steps that digest tool output
// mid-turn run at a lower one (options.tool_step_reasoning_effort). On a
// reasoning model at its strongest level the thinking before each step is
// most of the wall time of a session, and these steps need the least of
// it.

// toolStepEffort returns the reasoning effort for the steps that digest
// tool results, or "" when they keep the configured effort.
//
// Under auto the effort steps down one of the model's levels, never below
// "low" (see catalog.StepDownReasoningLevel), and only where changing the
// effort between requests leaves the prompt cache intact. An explicit
// level is honoured when the model supports it and it is weaker than the
// configured effort, whatever the cache costs: the user chose it. An
// explicit level the model does not have falls back to auto.
func toolStepEffort(setting string, model Model, providerType catalog.Type) string {
	effort := effectiveReasoningEffort(model)
	if effort == "" || setting == config.ToolStepReasoningEffortSame {
		return ""
	}
	levels := model.CatalogCfg.ReasoningLevels
	if setting != config.ToolStepReasoningEffortAuto && slices.Contains(levels, setting) {
		if catalog.WeakerReasoningLevel(setting, effort) {
			return setting
		}
		return ""
	}
	if effortChangeInvalidatesCache(providerType, model.CatalogCfg.ID) {
		return ""
	}
	return catalog.StepDownReasoningLevel(levels, effort)
}

// effortChangeInvalidatesCache reports whether sending one conversation's
// requests at different reasoning efforts costs the provider's prompt
// cache, which is what auto must not trade the speedup for.
//
// Anthropic documents it: a change to thinking or effort invalidates the
// cached message prefix, so alternating efforts would rewrite the whole
// conversation into the cache twice a turn (once for the step that
// answers the user, once for the first tool step after it). That holds
// for Claude wherever it is served, so Claude model ids are matched as
// well as the Anthropic and Bedrock option families. OpenAI's reasoning
// models take the effort in the system header of their prompt format
// (the open harmony format of gpt-oss shows it), ahead of everything
// cached, so OpenAI models are excluded the same way. Other providers
// render a thinking switch at the end of the prompt or outside it, where
// it costs nothing.
func effortChangeInvalidatesCache(providerType catalog.Type, modelID string) bool {
	if slices.Contains([]catalog.Type{anthropic.Name, bedrock.Name, openai.Name, azure.Name}, providerOptionsFamily(providerType, modelID)) {
		return true
	}
	id := strings.ToLower(modelID)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	for _, prefix := range []string{"claude", "gpt-", "o1", "o3", "o4"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

// toolStepProviderOptions returns the provider options for the steps that
// digest tool results, or nil when they are sent with the call's own. The
// options are built by the same getProviderOptions as the call's, from
// the model at the lower effort, so every provider's own way of spelling
// the effort applies. A provider_options entry that pins the effort wins
// there as it does for the call, and the two then come out identical,
// which is reported as nil.
func toolStepProviderOptions(model Model, providerCfg config.ProviderConfig, setting string) fantasy.ProviderOptions {
	effort := toolStepEffort(setting, model, providerCfg.Type)
	if effort == "" {
		return nil
	}
	lowered := model
	lowered.ModelCfg.ReasoningEffort = effort
	opts := getProviderOptions(lowered, providerCfg)
	if sameProviderOptions(opts, getProviderOptions(model, providerCfg)) {
		return nil
	}
	return opts
}

// sameProviderOptions reports whether a and b would send the same
// request options.
func sameProviderOptions(a, b fantasy.ProviderOptions) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// toolResultStep reports whether a step digests tool results: a step
// after the first of its turn whose input, as fantasy assembled it, ends
// in a tool-result message. A failed tool keeps the step at full effort.
// The model's plan did not survive contact, and choosing how to recover is
// where thinking pays; failures are a small share of steps, and a model
// that thinks too little after one tends to retry the same call. The
// caller also keeps full effort for a step that folds in a user prompt or
// a sub-agent message.
func toolResultStep(options fantasy.PrepareStepFunctionOptions) bool {
	if options.StepNumber == 0 || len(options.Messages) == 0 {
		return false
	}
	last := options.Messages[len(options.Messages)-1]
	if last.Role != fantasy.MessageRoleTool {
		return false
	}
	for _, part := range last.Content {
		result, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
		if !ok {
			continue
		}
		if _, failed := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result.Output); failed {
			return false
		}
	}
	return true
}
