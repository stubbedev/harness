package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/fantasy"
)

// decodeToolParams unmarshals a tool call's input into params and
// renders the shared invalid-parameters failure. Single source for the
// hand-rolled unmarshal guards in the deferred-load search tools, the
// dispatcher, and send_message.
func decodeToolParams(call fantasy.ToolCall, params any) (fantasy.ToolResponse, bool) {
	if err := json.Unmarshal([]byte(call.Input), params); err != nil {
		return fantasy.NewTextErrorResponse("invalid parameters: " + err.Error()), false
	}
	return fantasy.ToolResponse{}, true
}

// searchLoadParams is the request shape of the search-and-load tools:
// an optional query to rank and optional names to load.
type searchLoadParams struct {
	Query string   `json:"query"`
	Load  []string `json:"load"`
}

// decodeSearchLoadParams decodes a search tool's call input and
// enforces the shared nothing-to-do guard. purpose completes
// "load to ..." in the guidance ("load tools", "read skills").
func decodeSearchLoadParams(call fantasy.ToolCall, purpose string) (searchLoadParams, fantasy.ToolResponse, bool) {
	var params searchLoadParams
	if resp, ok := decodeToolParams(call, &params); !ok {
		return params, resp, false
	}
	if params.Query == "" && len(params.Load) == 0 {
		return params, fantasy.NewTextErrorResponse(fmt.Sprintf(`provide "query" to search or "load" to %s (both is fine)`, purpose)), false
	}
	return params, fantasy.ToolResponse{}, true
}

// unknownNamesFunc returns the requested names that fail the known
// predicate.
func unknownNamesFunc(load []string, known func(string) bool) []string {
	var unknown []string
	for _, name := range load {
		if !known(name) {
			unknown = append(unknown, name)
		}
	}
	return unknown
}

// joinNames renders a comma-joined name list for the unknown-name errors.
func joinNames(names []string) string {
	return strings.Join(names, ", ")
}
