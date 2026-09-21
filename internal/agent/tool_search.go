package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools"
)

// deferredBuiltinTools are the built-in tools whose schemas do not ride
// in every request. Every schema the model is handed costs tokens on
// every turn whether it is used or not, and these are the long tail: a
// session that never asks the language server or reads an MCP resource
// still paid for both on every call. Behind tool_search the model sees
// their names and one line each, and loads a schema the moment it needs
// the tool; from then on it stays in the tool list for the session.
//
// Memory is deliberately not here: the coder prompt tells the model to
// save what it learns the moment it learns it, which only happens if the
// tool is already in hand.
var deferredBuiltinTools = []string{
	tools.BatchToolName,
	tools.HarnessToolName,
	tools.LSPToolName,
	tools.MCPResourceToolName,
	tools.ResearchToolName,
}

// ToolSearchToolName is the tool the model calls to load a deferred tool.
const ToolSearchToolName = "tool_search"

// toolSearchTool stands in for the deferred built-in tools: it names them
// and loads the ones asked for into the running agent's tool set.
type toolSearchTool struct {
	coord *coordinator
	// deferred is what stands behind the search right now: the tools
	// built for this agent that are hidden until loaded.
	deferred []fantasy.ToolInfo
	opts     fantasy.ProviderOptions
}

func (s *toolSearchTool) Info() fantasy.ToolInfo {
	var names, lines []string
	for _, info := range s.deferred {
		names = append(names, info.Name)
		lines = append(lines, fmt.Sprintf("- %s: %s", info.Name, firstSentence(info.Description, 140)))
	}
	return fantasy.ToolInfo{
		Name: ToolSearchToolName,
		Description: fmt.Sprintf(
			"More tools, loaded on demand: %s. `load` puts the named tools in your tool list from your next step, with their full schemas, for the rest of the session.\n\n%s",
			strings.Join(names, ", "), strings.Join(lines, "\n"),
		),
		Parameters: map[string]any{
			"load": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Exact tool names to load into your tool list.",
			},
		},
		Required: []string{"load"},
	}
}

func (s *toolSearchTool) ProviderOptions() fantasy.ProviderOptions        { return s.opts }
func (s *toolSearchTool) SetProviderOptions(opts fantasy.ProviderOptions) { s.opts = opts }

func (s *toolSearchTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var params struct {
		Load []string `json:"load"`
	}
	if resp, ok := decodeToolParams(call, &params); !ok {
		return resp, nil
	}
	if len(params.Load) == 0 {
		return fantasy.NewTextErrorResponse(`provide "load" with the tool names to load`), nil
	}
	unknown := unknownNamesFunc(params.Load, func(name string) bool {
		return slices.ContainsFunc(s.deferred, func(info fantasy.ToolInfo) bool { return info.Name == name })
	})
	if len(unknown) > 0 {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("unknown tool(s): %s", joinNames(unknown))), nil
	}
	if err := s.coord.expandBuiltinTools(ctx, params.Load); err != nil {
		return fantasy.NewTextErrorResponse("failed to load tools: " + err.Error()), nil
	}
	return fantasy.NewTextResponse(fmt.Sprintf(
		"Loaded %d tool(s): %s. They appear in your tool list from your next step.",
		len(params.Load), strings.Join(params.Load, ", "))), nil
}

// firstSentence is the opening of a tool description, cut at the first
// sentence end or at limit runes, for the one-line listing.
func firstSentence(s string, limit int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".\n"); i > 0 {
		s = s[:i]
	}
	if runes := []rune(s); len(runes) > limit {
		s = string(runes[:limit-1]) + "…"
	}
	return s
}

// builtinToolExpanded reports whether a deferred built-in tool has been
// loaded into the coder agent's tool set this session.
func (c *coordinator) builtinToolExpanded(name string) bool {
	if c.expandedBuiltins == nil {
		return false
	}
	_, ok := c.expandedBuiltins.Get(name)
	return ok
}

// expandBuiltinTools marks deferred built-in tools as loaded and rebuilds
// the coder agent's tool set so they are live from the next step.
func (c *coordinator) expandBuiltinTools(ctx context.Context, names []string) error {
	added := false
	for _, name := range names {
		if _, loaded := c.expandedBuiltins.Get(name); slices.Contains(deferredBuiltinTools, name) && !loaded {
			c.expandedBuiltins.Set(name, true)
			added = true
		}
	}
	if !added || c.cfg == nil || c.currentAgent == nil {
		// Nothing new, or no running agent to rebuild for yet: the next
		// build picks the marks up.
		return nil
	}
	return c.refreshCoderTools(ctx)
}

// deferBuiltinTools splits a built tool list into the tools sent inline
// and the search tool standing in for the rest. Only the top-level agent
// defers: a sub-agent's dispatch is one short run with no next step to
// load anything into. A deferred tool the config has already removed is
// simply absent, so the allow and disable lists keep their say.
func (c *coordinator) deferBuiltinTools(list []fantasy.AgentTool, isSubAgent bool) []fantasy.AgentTool {
	if isSubAgent {
		return list
	}
	var kept []fantasy.AgentTool
	var deferred []fantasy.ToolInfo
	for _, tool := range list {
		name := tool.Info().Name
		if slices.Contains(deferredBuiltinTools, name) && !c.builtinToolExpanded(name) {
			deferred = append(deferred, tool.Info())
			continue
		}
		kept = append(kept, tool)
	}
	if len(deferred) == 0 {
		return kept
	}
	slices.SortFunc(deferred, func(a, b fantasy.ToolInfo) int { return strings.Compare(a.Name, b.Name) })
	// Wrapped like every other tool, so a PreToolUse hook sees loads too.
	return append(kept, wrapToolsWithHooks(wrapToolsResilient([]fantasy.AgentTool{&toolSearchTool{coord: c, deferred: deferred}}), c.hooks, c.queueArrivalEpoch)...)
}
