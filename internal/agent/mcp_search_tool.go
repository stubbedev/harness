package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/config"
)

// mcpSearchTool implements defer-loaded MCP tool discovery for one server.
// Servers that expose many tools (see config MCPConfig.ToolSearch) are not
// expanded into the model's context; instead this single tool stands in
// for them. The model searches by keyword, then loads the tools it wants;
// loaded tools join the running agent's tool set on the next step.
type mcpSearchTool struct {
	server string
	coord  *coordinator
	opts   fantasy.ProviderOptions
}

// mcpSearchResultLimit caps how many matches one search returns. The point
// of defer-loading is a small context footprint; a search that returns
// hundreds of names defeats it.
const mcpSearchResultLimit = 20

func (s *mcpSearchTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name: fmt.Sprintf("mcp_%s_tool_search", s.server),
		Description: fmt.Sprintf(
			`Search and load tools from the "%s" MCP server, which exposes %d tools that are not listed here to save context.

Two-step usage:
1. Run with {"query": "keyword"} to list matching tools (matched against tool name and description).
2. Run with {"load": ["tool_name", ...]} to load the ones you need. Loaded tools appear in your tool list from your next step, with their full input schemas.

Both fields may be combined in one call. Prefer loading few tools at a time.`,
			s.server, s.coord.mcpServerToolCount(s.server),
		),
		Parameters: map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Keyword matched against tool names and descriptions. Empty matches nothing.",
			},
			"load": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Exact tool names (as returned by a query) to load into your tool list.",
			},
		},
	}
}

func (s *mcpSearchTool) ProviderOptions() fantasy.ProviderOptions        { return s.opts }
func (s *mcpSearchTool) SetProviderOptions(opts fantasy.ProviderOptions) { s.opts = opts }

func (s *mcpSearchTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var params struct {
		Query string   `json:"query"`
		Load  []string `json:"load"`
	}
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.NewTextErrorResponse("invalid parameters: "+err.Error()), nil
	}
	if params.Query == "" && len(params.Load) == 0 {
		return fantasy.NewTextErrorResponse(`provide "query" to search or "load" to load tools (both is fine)`), nil
	}

	var b strings.Builder
	if params.Query != "" {
		matches := s.search(params.Query)
		if len(matches) == 0 {
			fmt.Fprintf(&b, "No tools matching %q on server %q.\n", params.Query, s.server)
		} else {
			fmt.Fprintf(&b, "%d tool(s) matching %q (of %d total):\n", len(matches), params.Query, s.coord.mcpServerToolCount(s.server))
			for _, name := range matches {
				desc := s.coord.mcpToolDescription(s.server, name)
				if len(desc) > 200 {
					desc = desc[:200] + "…"
				}
				fmt.Fprintf(&b, "- %s: %s\n", name, desc)
			}
		}
	}

	if len(params.Load) > 0 {
		known := s.coord.mcpServerToolNames(s.server)
		var unknown []string
		for _, name := range params.Load {
			if !slices.Contains(known, name) {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			return fantasy.NewTextErrorResponse(
				fmt.Sprintf("unknown tool(s) on server %q: %s", s.server, strings.Join(unknown, ", "))), nil
		}
		if err := s.coord.expandMCPServerTools(ctx, s.server, params.Load); err != nil {
			return fantasy.NewTextErrorResponse("failed to load tools: "+err.Error()), nil
		}
		fmt.Fprintf(&b, "Loaded %d tool(s) from %q. They appear in your tool list from your next step: %s\n",
			len(params.Load), s.server, strings.Join(params.Load, ", "))
	}

	return fantasy.NewTextResponse(strings.TrimRight(b.String(), "\n")), nil
}

// search returns up to mcpSearchResultLimit tool names matching the query,
// ranked: name prefix, name substring, then description substring, each
// group alphabetically.
func (s *mcpSearchTool) search(query string) []string {
	q := strings.ToLower(query)
	var prefix, nameMatch, descMatch []string
	for _, name := range s.coord.mcpServerToolNames(s.server) {
		lower := strings.ToLower(name)
		switch {
		case strings.HasPrefix(lower, q):
			prefix = append(prefix, name)
		case strings.Contains(lower, q):
			nameMatch = append(nameMatch, name)
		case strings.Contains(strings.ToLower(s.coord.mcpToolDescription(s.server, name)), q):
			descMatch = append(descMatch, name)
		}
	}
	matches := append(append(prefix, nameMatch...), descMatch...)
	if len(matches) > mcpSearchResultLimit {
		matches = matches[:mcpSearchResultLimit]
	}
	return matches
}

// mcpServerToolCount returns how many tools the named server exposes in
// the live registry.
func (c *coordinator) mcpServerToolCount(server string) int {
	return len(c.mcpServerToolNames(server))
}

// mcpServerToolNames returns the sorted raw tool names a server exposes.
func (c *coordinator) mcpServerToolNames(server string) []string {
	var names []string
	for name, tools := range mcp.Tools() {
		if name != server {
			continue
		}
		for _, t := range tools {
			names = append(names, t.Name)
		}
		break
	}
	slices.Sort(names)
	return names
}

// mcpToolDescription returns a tool's description, or "" when unknown.
func (c *coordinator) mcpToolDescription(server, toolName string) string {
	for name, tools := range mcp.Tools() {
		if name != server {
			continue
		}
		for _, t := range tools {
			if t.Name == toolName {
				return t.Description
			}
		}
		break
	}
	return ""
}

// deferredMCPServers reports which servers have their tools defer-loaded
// for the top-level agent: a server qualifies when its config asks for it
// (explicitly or via the automatic threshold) and the agent has no
// tool-level curation for it — a specific AllowedMCP list is already a
// hand-picked subset, deferring it would only add a hop.
func (c *coordinator) deferredMCPServers(agent config.Agent) map[string]bool {
	deferred := make(map[string]bool)
	cfg := c.cfg.Config()
	for server, names := range c.mcpServerRegistryCounts() {
		mcpCfg, ok := cfg.MCP[server]
		if !ok || !mcpCfg.DeferToolSearch(names) {
			continue
		}
		if allowed, ok := agent.AllowedMCP[server]; ok && len(allowed) > 0 {
			continue
		}
		deferred[server] = true
	}
	return deferred
}

// mcpServerRegistryCounts counts registry tools per server.
func (c *coordinator) mcpServerRegistryCounts() map[string]int {
	counts := make(map[string]int)
	for server, tools := range mcp.Tools() {
		counts[server] = len(tools)
	}
	return counts
}

// mcpToolExpanded reports whether a defer-loaded server tool was already
// loaded into the agent's tool set.
func (c *coordinator) mcpToolExpanded(server, toolName string) bool {
	expanded, ok := c.expandedMCPTools.Get(server)
	return ok && expanded[toolName]
}

// expandMCPServerTools marks tools from a defer-loaded server as expanded
// and rebuilds the coder agent's tool set so they are live. Unknown names
// are ignored; callers validate first.
func (c *coordinator) expandMCPServerTools(ctx context.Context, server string, names []string) error {
	existing, _ := c.expandedMCPTools.Get(server)
	next := make(map[string]bool, len(existing)+len(names))
	for k := range existing {
		next[k] = true
	}
	known := c.mcpServerToolNames(server)
	added := false
	for _, name := range names {
		if slices.Contains(known, name) && !next[name] {
			next[name] = true
			added = true
		}
	}
	c.expandedMCPTools.Set(server, next)
	if !added {
		return nil
	}
	return c.refreshCoderTools(ctx)
}

// refreshCoderTools rebuilds the coder agent's tool list in place. Cheap
// enough to call mid-turn: PrepareStep re-reads the tool slice every step,
// so new tools are picked up without restarting the stream.
func (c *coordinator) refreshCoderTools(ctx context.Context) error {
	agentCfg, ok := c.cfg.Config().Agents[config.AgentCoder]
	if !ok {
		return errCoderAgentNotConfigured
	}
	modelID := c.currentAgent.Model().CatwalkCfg.ID
	built, err := c.buildTools(ctx, agentCfg, false, modelID)
	if err != nil {
		return err
	}
	c.currentAgent.SetTools(built)
	return nil
}
