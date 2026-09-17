package agent

import (
	"encoding/json"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
)

// TestToolSchemas_AreProviderValid pins the shape of every tool harness
// advertises, across the agents and the sub-agent split that change which
// tools are built.
//
// The tool list is sent whole, so a tool the provider will not parse does
// not degrade to a missing tool: it fails the request, every turn, with an
// error that names a schema rather than the tool's source. The nil
// Required that used to ship on the dispatcher is the case in point --
// fantasy renders a ToolInfo as {"type":"object","properties":…,
// "required":Required}, a nil slice marshals to null, and OpenAI answered
// with `Invalid schema for function 'agent': None is not of type 'array'`.
// Tools built by fantasy.NewAgentTool get the empty slice for free; the
// hand-written ToolInfos in this package have to spell it out.
func TestToolSchemas_AreProviderValid(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	coord := newTestCoordinator(t, env, "p", config.ProviderConfig{ID: "p"})

	for _, agentID := range []string{config.AgentCoder, config.AgentTask} {
		for _, isSubAgent := range []bool{false, true} {
			agentCfg, ok := coord.cfg.Config().Agents[agentID]
			require.True(t, ok, "agent %s should be configured", agentID)

			built, err := coord.buildTools(t.Context(), agentCfg, isSubAgent)
			require.NoError(t, err)
			require.NotEmpty(t, built)

			for _, tool := range built {
				requireValidToolSchema(t, tool)
			}
		}
	}

	// The search tools surface only behind deferred MCP servers and
	// model-invocable skills, neither of which this offline config has,
	// so they are checked directly rather than through buildTools.
	for _, tool := range []fantasy.AgentTool{
		&mcpSearchTool{server: "some-server", coord: coord},
		&skillSearchTool{coord: coord},
		&sendMessageTool{coord: coord},
	} {
		requireValidToolSchema(t, tool)
	}
}

// requireValidToolSchema asserts the invariants a provider enforces on one
// advertised tool: a usable name, an object of properties rather than
// null, an array of required names rather than null, and no required name
// missing from the properties it points into.
func requireValidToolSchema(t *testing.T, tool fantasy.AgentTool) {
	t.Helper()

	info := tool.Info()
	assert.Empty(t, toolNameProblem(info.Name, nil), "tool %q has an unusable name", info.Name)
	assert.NotEmpty(t, info.Description, "tool %q has no description", info.Name)

	// Mirror how fantasy renders a ToolInfo for the wire (see its
	// agent.prepareTools), then read the JSON back: only the round trip
	// shows a nil slice or map as the null the provider rejects.
	wire := map[string]any{
		"type":       "object",
		"properties": info.Parameters,
		"required":   info.Required,
	}
	schema.Normalize(wire)
	raw, err := json.Marshal(wire)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	properties, ok := decoded["properties"].(map[string]any)
	assert.True(t, ok, "tool %q must send properties as an object, got %v", info.Name, decoded["properties"])
	_, ok = decoded["required"].([]any)
	assert.True(t, ok, "tool %q must send required as an array, got %v", info.Name, decoded["required"])

	for _, name := range info.Required {
		assert.Contains(t, properties, name, "tool %q requires %q, which is not one of its properties", info.Name, name)
	}
}
