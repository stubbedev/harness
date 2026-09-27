package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/subagents"
)

// recordingAgent counts SetModels/SetTools so tests can watch UpdateModels
// rebuild or skip the coder agent's models and tool palette.
type recordingAgent struct {
	mockSessionAgent
	model     Model
	modelSets int
	toolsSets int
	lastTools []fantasy.AgentTool
}

func (r *recordingAgent) Model() Model { return r.model }

func (r *recordingAgent) SetModels(large, small Model) {
	r.model = large
	r.modelSets++
}

func (r *recordingAgent) SetTools(tools []fantasy.AgentTool) {
	r.toolsSets++
	r.lastTools = tools
}

// offlineTestProvider builds an openai-compatible provider whose models
// construct without network I/O, the same shape agenttest uses.
func offlineTestProvider(models ...string) config.ProviderConfig {
	cfg := config.ProviderConfig{
		ID:      "test-provider",
		Type:    "openai-compat",
		BaseURL: "http://127.0.0.1:0/v1",
		APIKey:  "test-key",
	}
	for _, id := range models {
		cfg.Models = append(cfg.Models, catalog.Model{ID: id, DefaultMaxTokens: 4096})
	}
	return cfg
}

// selectTestModels pins both selections on the test provider, swapping the
// published config the same way a real model switch does.
func selectTestModels(t *testing.T, coord *coordinator, model string) {
	t.Helper()
	coord.cfg.OverridePreferredModel(config.SelectedModelTypeLarge, config.SelectedModel{Provider: "test-provider", Model: model})
	coord.cfg.OverridePreferredModel(config.SelectedModelTypeSmall, config.SelectedModel{Provider: "test-provider", Model: model})
}

// newUpdateModelsCoordinator wires a coordinator whose currentAgent records
// the SetModels/SetTools calls UpdateModels makes.
func newUpdateModelsCoordinator(t *testing.T, env fakeEnv, providerCfg config.ProviderConfig) (*coordinator, *recordingAgent) {
	t.Helper()
	coord := newTestCoordinator(t, env, providerCfg.ID, providerCfg)
	coord.cfg.SetupAgents()
	coord.expandedMCPTools = csync.NewMap[string, map[string]bool]()
	coord.expandedBuiltins = csync.NewMap[string, bool]()
	agent := &recordingAgent{}
	coord.currentAgent = agent
	return coord, agent
}

func TestUpdateModels_SkipsRebuildWhenGenerationUnchanged(t *testing.T) {
	env := testEnv(t)
	coord, agent := newUpdateModelsCoordinator(t, env, offlineTestProvider("model-a", "model-b"))
	selectTestModels(t, coord, "model-a")

	require.NoError(t, coord.UpdateModels(t.Context()))
	require.Equal(t, 1, agent.modelSets)
	require.Equal(t, 1, agent.toolsSets)
	require.NotEmpty(t, agent.lastTools)
	firstTools := agent.lastTools

	require.NoError(t, coord.UpdateModels(t.Context()))
	require.Equal(t, 1, agent.modelSets, "unchanged generation must not rebuild models")
	require.Equal(t, 1, agent.toolsSets, "unchanged generation must not rebuild the palette")
	require.True(t, &firstTools[0] == &agent.lastTools[0], "the palette from the first build must stay installed")
}

func TestUpdateModels_RebuildsWhenConfigChanges(t *testing.T) {
	env := testEnv(t)
	coord, agent := newUpdateModelsCoordinator(t, env, offlineTestProvider("model-a", "model-b"))
	selectTestModels(t, coord, "model-a")

	require.NoError(t, coord.UpdateModels(t.Context()))
	require.Equal(t, 1, agent.modelSets)
	require.Equal(t, 1, agent.toolsSets)

	selectTestModels(t, coord, "model-b")
	require.NoError(t, coord.UpdateModels(t.Context()))
	require.Equal(t, 2, agent.modelSets, "a config change must rebuild models")
	require.Equal(t, 2, agent.toolsSets, "a config change must rebuild the palette")
	require.Equal(t, "model-b", agent.model.ModelCfg.Model)
}

func TestUpdateModels_RebuildsAfterInPlaceCredentialChange(t *testing.T) {
	env := testEnv(t)
	providerCfg := offlineTestProvider("model-a")
	coord, agent := newUpdateModelsCoordinator(t, env, providerCfg)
	selectTestModels(t, coord, "model-a")

	require.NoError(t, coord.UpdateModels(t.Context()))
	require.Equal(t, 1, agent.modelSets)

	// Rotate the credential the way the OAuth refresh and API-key
	// re-resolution paths do: written straight into the shared Providers
	// map, so the published config pointer does not change.
	rotated := providerCfg
	rotated.APIKey = "rotated-key"
	coord.cfg.Config().Providers.Set(providerCfg.ID, rotated)

	require.NoError(t, coord.UpdateModels(t.Context()))
	require.Equal(t, 2, agent.modelSets, "an in-place credential change must rebuild models")
}

func TestUpdateModels_RebuildsOnMCPRegistryChange(t *testing.T) {
	env := testEnv(t)
	coord, agent := newUpdateModelsCoordinator(t, env, offlineTestProvider("model-a"))
	selectTestModels(t, coord, "model-a")

	require.NoError(t, coord.UpdateModels(t.Context()))
	require.NotContains(t, toolNamesOf(agent.lastTools), "mcp_srv_foo")

	restore := mcp.RegisterToolsForTest("srv", []*mcp.Tool{{Name: "foo"}})
	require.NoError(t, coord.UpdateModels(t.Context()))
	require.Equal(t, 2, agent.toolsSets, "a registry change must rebuild the palette")
	require.Contains(t, toolNamesOf(agent.lastTools), "mcp_srv_foo")

	restore()
	require.NoError(t, coord.UpdateModels(t.Context()))
	require.Equal(t, 3, agent.toolsSets)
	require.NotContains(t, toolNamesOf(agent.lastTools), "mcp_srv_foo")
}

func TestModelToolGeneration_SensitiveToLiveInputs(t *testing.T) {
	env := testEnv(t)
	providerCfg := offlineTestProvider("model-a")
	coord, _ := newUpdateModelsCoordinator(t, env, providerCfg)
	selectTestModels(t, coord, "model-a")

	t.Run("stable when nothing changed", func(t *testing.T) {
		gen := coord.modelToolGeneration()
		require.Equal(t, gen, coord.modelToolGeneration())
	})

	t.Run("provider credential change", func(t *testing.T) {
		gen := coord.modelToolGeneration()
		rotated := providerCfg
		rotated.APIKey = "rotated-key"
		coord.cfg.Config().Providers.Set(providerCfg.ID, rotated)
		require.NotEqual(t, gen, coord.modelToolGeneration())
	})

	t.Run("model selection change", func(t *testing.T) {
		gen := coord.modelToolGeneration()
		selectTestModels(t, coord, "model-b")
		require.NotEqual(t, gen, coord.modelToolGeneration())
		selectTestModels(t, coord, "model-a")
	})

	t.Run("mcp registry change", func(t *testing.T) {
		gen := coord.modelToolGeneration()
		restore := mcp.RegisterToolsForTest("srv", []*mcp.Tool{{Name: "foo"}})
		require.NotEqual(t, gen, coord.modelToolGeneration())
		restore()
	})

	t.Run("tool search expansion change", func(t *testing.T) {
		gen := coord.modelToolGeneration()
		coord.expandedBuiltins.Set("view", true)
		coord.expandedMCPTools.Set("srv", map[string]bool{"foo": true})
		require.NotEqual(t, gen, coord.modelToolGeneration())
		coord.expandedBuiltins.Del("view")
		coord.expandedMCPTools.Del("srv")
	})

	t.Run("subagent change", func(t *testing.T) {
		gen := coord.modelToolGeneration()
		coord.activeSubagents = []*subagents.Subagent{{Name: "researcher", Description: "digs"}}
		require.NotEqual(t, gen, coord.modelToolGeneration())
		coord.activeSubagents = nil
	})

	t.Run("aws credential refresh forces model rebuild", func(t *testing.T) {
		awsProvider := providerCfg
		awsProvider.ID = "bedrock-like"
		awsProvider.AWSAuthRefresh = "aws sso login"
		coord.cfg.Config().Providers.Set(awsProvider.ID, awsProvider)
		selectTestModels(t, coord, "model-a")
		require.False(t, coord.modelToolGeneration().forceModelRebuild)

		coord.cfg.OverridePreferredModel(config.SelectedModelTypeLarge, config.SelectedModel{Provider: "bedrock-like", Model: "model-a"})
		require.True(t, coord.modelToolGeneration().forceModelRebuild)
	})
}

func toolNamesOf(tools []fantasy.AgentTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Info().Name)
	}
	return names
}
