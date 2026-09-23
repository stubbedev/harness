package agent

import (
	"context"
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

// mcpSearchNameBudget bounds how many characters of a server's tool-name
// list the search tool's own description carries. The names are what make
// a deferred server discoverable at all: a model that cannot see that a
// capability exists will not think to search for it. They are cheap next
// to the schemas they stand in for, but a server exposing hundreds of
// tools must not spend the context the deferral was meant to save.
const mcpSearchNameBudget = 2000

func (s *mcpSearchTool) Info() fantasy.ToolInfo {
	names := s.coord.mcpServerToolNames(s.server)
	return fantasy.ToolInfo{
		Name: fmt.Sprintf("mcp_%s_tool_search", s.server),
		Description: fmt.Sprintf(
			`Tools from the "%s" MCP server. Its %d tools are named below; their input schemas load on demand.

Tools: %s

`+"`query`"+` lists matching tools with a short description each (keywords fuzzy-matched against names and descriptions, all must match). `+"`load`"+` puts the named tools in your tool list from the next step, with schemas and the server's usage instructions. Both fields may be combined; a name above can be loaded without searching first. Load few at a time.`,
			s.server, len(names), nameList(names, mcpSearchNameBudget),
		),
		Parameters: map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Keywords, space-separated; all must fuzzy-match a name or description.",
			},
			"load": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Exact tool names to load into your tool list.",
			},
		},
		// Both fields are optional, but the empty slice has to be
		// explicit: a nil Required marshals to JSON null, which the
		// OpenAI Responses API rejects as not an array.
		Required: []string{},
	}
}

func (s *mcpSearchTool) ProviderOptions() fantasy.ProviderOptions        { return s.opts }
func (s *mcpSearchTool) SetProviderOptions(opts fantasy.ProviderOptions) { s.opts = opts }

func (s *mcpSearchTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	params, resp, ok := decodeSearchLoadParams(call, "load tools")
	if !ok {
		return resp, nil
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
		unknown := unknownNamesFunc(params.Load, func(name string) bool { return slices.Contains(s.coord.mcpServerToolNames(s.server), name) })
		if len(unknown) > 0 {
			return fantasy.NewTextErrorResponse(
				fmt.Sprintf("unknown tool(s) on server %q: %s", s.server, joinNames(unknown))), nil
		}
		if err := s.coord.expandMCPServerTools(ctx, s.server, params.Load); err != nil {
			return fantasy.NewTextErrorResponse("failed to load tools: " + err.Error()), nil
		}
		// The loaded tools join the tool list for the next step, which is
		// also when the system prompt starts carrying this server's usage
		// instructions: the gate there keys off the server having tools in
		// the request, so nothing needs to be repeated here.
		fmt.Fprintf(&b, "Loaded %d tool(s) from %q. They appear in your tool list from your next step, with this server's usage instructions: %s\n",
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

// search ranks the server's tools against the query and returns the top
// mcpSearchResultLimit names plus how many matched in total. Names are
// already sorted alphabetically, which is what breaks score ties.
func (s *mcpSearchTool) search(query string) ([]string, int) {
	names := s.coord.mcpServerToolNames(s.server)
	candidates := make([]searchCandidate, len(names))
	for i, name := range names {
		candidates[i] = searchCandidate{name: name, desc: s.coord.mcpToolDescription(s.server, name)}
	}
	return rankCandidates(query, candidates, mcpSearchResultLimit)
}

// liveMCPServers reports which MCP servers have at least one of their own
// tools in a built tool list. A server standing behind its tool_search stub
// has none: the stub is not one of its tools, it is the placeholder for all
// of them.
func liveMCPServers(agentTools []fantasy.AgentTool) map[string]bool {
	live := make(map[string]bool)
	for _, tool := range agentTools {
		mcpTool, ok := tool.(interface{ MCP() string })
		if !ok {
			continue
		}
		if server := mcpTool.MCP(); server != "" {
			live[server] = true
		}
	}
	return live
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
// for the top-level agent. Every configured server defers unless its
// config opts out (tool_search: false) or the agent has curated it with a
// specific AllowedMCP list — a hand-picked subset is already bounded, so
// deferring it would only add a hop.
func (c *coordinator) deferredMCPServers(agent config.Agent) map[string]bool {
	deferred := make(map[string]bool)
	cfg := c.cfg.Config()
	for server := range mcp.Tools() {
		mcpCfg, ok := cfg.MCP[server]
		if !ok || !mcpCfg.DeferToolSearch() {
			continue
		}
		if allowed, ok := agent.AllowedMCP[server]; ok && len(allowed) > 0 {
			continue
		}
		deferred[server] = true
	}
	return deferred
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
	built, err := c.buildTools(ctx, agentCfg, false, nil)
	if err != nil {
		return err
	}
	c.currentAgent.SetTools(built)
	return nil
}
