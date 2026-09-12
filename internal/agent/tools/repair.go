package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/jsonrepair"
)

// validationErrPrefix is how the agent reports a call that arrived
// without one of the tool's required parameters.
const validationErrPrefix = "missing required parameter: "

// RepairToolCall is the second chance a malformed tool call gets before
// it is handed back to the model as an error. A rejected call costs a
// whole turn - the model has to read the complaint, guess what shape
// was wanted, and send again - so anything that can be fixed from the
// call itself is fixed here instead.
//
// Two things are repaired. Malformed JSON goes through jsonrepair,
// which is what the agent does by default and what registering this
// function would otherwise switch off. Missing labels are filled in:
// parameters that only name a call for the user (description) carry no
// meaning for the tool, and refusing to run because a label is absent
// helps nobody.
//
// Anything else is left alone. A missing parameter the tool actually
// acts on - a path, a pattern, a command - is the model's to supply,
// and inventing a value for it would run something nobody asked for.
func RepairToolCall(_ context.Context, opts fantasy.ToolCallRepairOptions) (*fantasy.ToolCallContent, error) {
	call := opts.OriginalToolCall

	input := map[string]any{}
	if err := json.Unmarshal([]byte(call.Input), &input); err != nil {
		repaired, repairErr := jsonrepair.RepairJSON(call.Input)
		if repairErr != nil {
			return nil, fmt.Errorf("tool call input is not JSON: %w", err)
		}
		if err := json.Unmarshal([]byte(repaired), &input); err != nil {
			return nil, fmt.Errorf("tool call input is not JSON: %w", err)
		}
		call.Input = repaired
	}

	missing := missingParams(call.ToolName, input, opts.AvailableTools)
	if len(missing) == 0 {
		// The JSON repair above may already have been the fix.
		return &call, nil
	}
	filled := false
	for _, name := range missing {
		if !labelParams[name] {
			continue
		}
		input[name] = ""
		filled = true
	}
	if !filled {
		return nil, fmt.Errorf("%s%s", validationErrPrefix, strings.Join(missing, ", "))
	}

	patched, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("re-encoding repaired tool call: %w", err)
	}
	call.Input = string(patched)
	return &call, nil
}

// labelParams are parameters that exist to name a call in the UI rather
// than to tell the tool what to do, so an absent one can be filled in
// with nothing at all.
var labelParams = map[string]bool{
	"description": true,
}

// missingParams lists the tool's required parameters that the call did
// not carry, in the order the tool declares them.
func missingParams(name string, input map[string]any, available []fantasy.AgentTool) []string {
	info, ok := toolInfo(name, available)
	if !ok {
		return nil
	}
	var missing []string
	for _, required := range info.Required {
		if _, exists := input[required]; !exists {
			missing = append(missing, required)
		}
	}
	return missing
}

func toolInfo(name string, available []fantasy.AgentTool) (fantasy.ToolInfo, bool) {
	for _, t := range available {
		if info := t.Info(); info.Name == name {
			return info, true
		}
	}
	return fantasy.ToolInfo{}, false
}

// IsValidationError reports whether an error is the agent refusing a
// tool call for its shape rather than a tool failing to do its job.
func IsValidationError(err error) bool {
	return err != nil && strings.Contains(err.Error(), validationErrPrefix)
}

// ValidationHint turns "missing required parameter: x" - true, and
// useless on its own - into something a caller can act on: which
// parameters the tool insists on, which ones it also takes, and a call
// shaped the way this tool wants one.
func ValidationHint(info fantasy.ToolInfo, err error) string {
	if !IsValidationError(err) {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s requires: %s.", info.Name, strings.Join(info.Required, ", "))
	if optional := optionalParams(info); len(optional) > 0 {
		fmt.Fprintf(&sb, " It also takes: %s.", strings.Join(optional, ", "))
	}
	if example := exampleCall(info); example != "" {
		fmt.Fprintf(&sb, " Send the call again with every required parameter, e.g. %s", example)
	}
	return sb.String()
}

// optionalParams are the tool's parameters that are not required, named
// in a stable order so the same tool always reads the same way.
func optionalParams(info fantasy.ToolInfo) []string {
	optional := make([]string, 0, len(info.Parameters))
	for name := range info.Parameters {
		if !slices.Contains(info.Required, name) {
			optional = append(optional, name)
		}
	}
	slices.Sort(optional)
	return optional
}

// exampleCall renders the smallest call the tool would accept: every
// required parameter, each with a placeholder naming itself.
func exampleCall(info fantasy.ToolInfo) string {
	if len(info.Required) == 0 {
		return ""
	}
	parts := make([]string, 0, len(info.Required))
	for _, name := range info.Required {
		parts = append(parts, fmt.Sprintf("%q: %q", name, "<"+name+">"))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
