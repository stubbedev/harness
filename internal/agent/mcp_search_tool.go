package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/sahilm/fuzzy"
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
1. Run with {"query": "keyword ..."} to list matching tools. Keywords are matched fuzzily against tool names and descriptions; every keyword must match, and name matches rank higher.
2. Run with {"load": ["tool_name", ...]} to load the ones you need. Loaded tools appear in your tool list from your next step, with their full input schemas.

Both fields may be combined in one call. Prefer loading few tools at a time.`,
			s.server, s.coord.mcpServerToolCount(s.server),
		),
		Parameters: map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Keywords, space-separated. Every keyword must fuzzy-match the tool's name or description; name matches rank higher. Empty matches nothing.",
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
		return fantasy.NewTextErrorResponse("invalid parameters: " + err.Error()), nil
	}
	if params.Query == "" && len(params.Load) == 0 {
		return fantasy.NewTextErrorResponse(`provide "query" to search or "load" to load tools (both is fine)`), nil
	}

	var b strings.Builder
	if params.Query != "" {
		matches, matched := s.search(params.Query)
		switch {
		case matched == 0:
			fmt.Fprintf(&b, "No tools matching %q on server %q.\n", params.Query, s.server)
		case matched > len(matches):
			fmt.Fprintf(&b, "%d tool(s) match %q (of %d on the server); showing the best %d. Narrow the query to see the others:\n",
				matched, params.Query, s.coord.mcpServerToolCount(s.server), len(matches))
			s.writeMatches(&b, matches)
		default:
			fmt.Fprintf(&b, "%d tool(s) matching %q (of %d total):\n", matched, params.Query, s.coord.mcpServerToolCount(s.server))
			s.writeMatches(&b, matches)
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
			return fantasy.NewTextErrorResponse("failed to load tools: " + err.Error()), nil
		}
		fmt.Fprintf(&b, "Loaded %d tool(s) from %q. They appear in your tool list from your next step: %s\n",
			len(params.Load), s.server, strings.Join(params.Load, ", "))
	}

	return fantasy.NewTextResponse(strings.TrimRight(b.String(), "\n")), nil
}

// writeMatches lists one "- name: description" line per tool, eliding
// descriptions past 200 runes so one search stays context-cheap.
func (s *mcpSearchTool) writeMatches(b *strings.Builder, names []string) {
	for _, name := range names {
		desc := s.coord.mcpToolDescription(s.server, name)
		if len(desc) > 200 {
			desc = desc[:200] + "…"
		}
		fmt.Fprintf(b, "- %s: %s\n", name, desc)
	}
}

// search scores the server's tools with the fzf algorithm and returns the
// top mcpSearchResultLimit names plus how many matched in total. The query
// is split on whitespace and every term must match somewhere, the way fzf's
// extended search AND's its terms. Each term scores against the name and
// the description — a name match counts double — and a tool's score is the
// sum over terms; ties break alphabetically.
func (s *mcpSearchTool) search(query string) ([]string, int) {
	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		name  string
		score int
	}
	var matches []scored
	for _, name := range s.coord.mcpServerToolNames(s.server) {
		desc := strings.ToLower(s.coord.mcpToolDescription(s.server, name))
		lower := strings.ToLower(name)
		total := 0
		allTerms := true
		for _, term := range terms {
			best, found := 0, false
			for _, m := range fuzzy.Find(term, []string{lower}) {
				if !found || m.Score*2 > best {
					best, found = m.Score*2, true
				}
			}
			for _, m := range fuzzy.Find(term, []string{desc}) {
				if !found || m.Score > best {
					best, found = m.Score, true
				}
			}
			if !found {
				allTerms = false
				break
			}
			total += best
		}
		if allTerms {
			matches = append(matches, scored{name: name, score: total})
		}
	}
	slices.SortStableFunc(matches, func(a, b scored) int { return b.score - a.score })
	total := len(matches)
	if total > mcpSearchResultLimit {
		matches = matches[:mcpSearchResultLimit]
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.name
	}
	return names, total
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
