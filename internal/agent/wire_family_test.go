package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/openai"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

// wireTestCoordinator returns a coordinator that can build providers from
// in-memory config, without reaching any of them.
func wireTestCoordinator(t *testing.T) *coordinator {
	t.Helper()
	return newCoordinatorForTest(&coordinator{
		cfg: config.NewTestStoreWithWorkingDir(&config.Config{Options: &config.Options{}}, t.TempDir()),
	})
}

// reasoningModel returns a reasoning model with id, configured at high
// effort.
func reasoningModel(providerID catalog.InferenceProvider, id string) Model {
	return Model{
		CatalogCfg: catalog.Model{ID: id, CanReason: true, ReasoningLevels: []string{"low", "medium", "high"}},
		ModelCfg:   config.SelectedModel{Provider: providerID, Model: id, ReasoningEffort: "high"},
	}
}

// optionsKey names the key under which the language models of a fantasy
// provider called name read their provider options. Fantasy's bedrock
// provider is its anthropic provider under another name and reads the
// anthropic options.
func optionsKey(name string) string {
	if name == bedrock.Name {
		return anthropic.Name
	}
	return name
}

// TestProviderOptionsKeyedForBuiltProvider pins getProviderOptions to the
// provider buildProvider builds for the same config and model: options
// keyed under another name are never read, which silently drops the
// reasoning effort among the rest.
func TestProviderOptionsKeyedForBuiltProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		providerCfg config.ProviderConfig
		modelID     string
	}{
		{"anthropic", config.ProviderConfig{ID: "anthropic", Type: catalog.TypeAnthropic}, "claude-sonnet-4-5"},
		{"custom anthropic", config.ProviderConfig{ID: "my-proxy", Type: catalog.TypeAnthropic}, "claude-sonnet-4-5"},
		{"openai", config.ProviderConfig{ID: "openai", Type: catalog.TypeOpenAI}, "gpt-5"},
		{"openai-compat", config.ProviderConfig{ID: catalog.InferenceProviderZAICoding, Type: catalog.TypeOpenAICompat}, "glm-5.3"},
		{"opencode-go on chat completions", openCodeProvider(catalog.InferenceProviderOpenCodeGo, "https://opencode.ai/inference/go"), "kimi-k3"},
		{"opencode-go on the responses api", openCodeProvider(catalog.InferenceProviderOpenCodeGo, "https://opencode.ai/inference/go"), "gpt-5.6-luna"},
		{"opencode-go on the responses api (grok)", openCodeProvider(catalog.InferenceProviderOpenCodeGo, "https://opencode.ai/inference/go"), "grok-4.5"},
		{"opencode-go on the responses api (muse spark)", openCodeProvider(catalog.InferenceProviderOpenCodeGo, "https://opencode.ai/inference/go"), "muse-spark-pro"},
		{"opencode-go on the messages api", openCodeProvider(catalog.InferenceProviderOpenCodeGo, "https://opencode.ai/inference/go"), "minimax-m3"},
		{"opencode-zen on the messages api", openCodeProvider(catalog.InferenceProviderOpenCodeZen, "https://opencode.ai/inference"), "claude-sonnet-4-5"},
		{"opencode-zen on the responses api", openCodeProvider(catalog.InferenceProviderOpenCodeZen, "https://opencode.ai/inference"), "gpt-5.5"},
		{"opencode-zen on the responses api (grok)", openCodeProvider(catalog.InferenceProviderOpenCodeZen, "https://opencode.ai/inference"), "grok-4.5"},
		{"opencode-zen on the responses api (muse spark)", openCodeProvider(catalog.InferenceProviderOpenCodeZen, "https://opencode.ai/inference"), "muse-spark-pro"},
		{"copilot on the responses api", config.ProviderConfig{ID: catalog.InferenceProviderCopilot, Type: catalog.TypeOpenAICompat}, "gpt-6-astra"},
		{"vertex claude", config.ProviderConfig{ID: "vertexai", Type: catalog.TypeVertexAI, ExtraParams: map[string]string{"project": "p", "location": "us-east5"}}, "claude-sonnet-4@20250514"},
		{"vertex gemini", config.ProviderConfig{ID: "vertexai", Type: catalog.TypeVertexAI, ExtraParams: map[string]string{"project": "p", "location": "us-east5"}}, "gemini-3-pro"},
		{"bedrock", config.ProviderConfig{ID: "bedrock", Type: catalog.TypeBedrock}, "anthropic.claude-sonnet-4-5"},
		{"known custom provider", config.ProviderConfig{ID: "ollama", Type: "ollama", BaseURL: "http://localhost:11434/v1"}, "qwen3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := reasoningModel(tc.providerCfg.ID, tc.modelID)
			provider, err := wireTestCoordinator(t).buildProvider(tc.providerCfg, model.ModelCfg, false)
			require.NoError(t, err)
			responsesAPIModel := isResponsesAPIModel(tc.providerCfg, tc.modelID)
			if responsesAPIModel {
				require.Equal(t, openai.Name, provider.Name())
			}

			opts := getProviderOptions(model, tc.providerCfg)
			require.Len(t, opts, 1)
			require.Contains(t, opts, optionsKey(provider.Name()))
			if responsesAPIModel {
				parsed, ok := opts[openai.Name].(*openai.ResponsesProviderOptions)
				require.True(t, ok, "Responses API routes must use Responses provider options")
				require.NotNil(t, parsed.ReasoningEffort)
				require.Equal(t, openai.ReasoningEffort("high"), *parsed.ReasoningEffort)
			}
		})
	}
}

// TestProviderOptionsForMessagesAPIModels pins the reasoning effort of a
// model an OpenAI-compat provider serves over the Anthropic Messages API
// to the anthropic options its anthropic provider reads.
func TestProviderOptionsForMessagesAPIModels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		providerID catalog.InferenceProvider
		modelID    string
	}{
		{catalog.InferenceProviderOpenCodeGo, "minimax-m3"},
		{catalog.InferenceProviderOpenCodeZen, "claude-sonnet-4-5"},
	} {
		t.Run(string(tc.providerID), func(t *testing.T) {
			t.Parallel()

			providerCfg := openCodeProvider(tc.providerID, "https://opencode.ai/inference")
			parsed, ok := getProviderOptions(reasoningModel(tc.providerID, tc.modelID), providerCfg)[anthropic.Name].(*anthropic.ProviderOptions)
			require.True(t, ok, "options must be keyed for the anthropic provider")
			require.NotNil(t, parsed.Effort)
			require.Equal(t, anthropic.Effort("high"), *parsed.Effort)
		})
	}
}

// TestBuildProviderInterleavedThinkingForMessagesAPIModels pins the
// interleaved-thinking beta to every model called over the Anthropic
// Messages API, not only those on a provider of anthropic type. The
// requests go to a local server that records their headers.
func TestBuildProviderInterleavedThinkingForMessagesAPIModels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		providerCfg config.ProviderConfig
		modelID     string
	}{
		{"anthropic", config.ProviderConfig{ID: "anthropic", Type: catalog.TypeAnthropic, APIKey: "k"}, "claude-sonnet-4-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			beta := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case beta <- r.Header.Get("anthropic-beta"):
				default:
				}
				http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"stop"}}`, http.StatusBadRequest)
			}))
			t.Cleanup(srv.Close)

			providerCfg := tc.providerCfg
			providerCfg.BaseURL = srv.URL + "/v1"
			selected := config.SelectedModel{Provider: providerCfg.ID, Model: tc.modelID, Think: true}
			provider, err := wireTestCoordinator(t).buildProvider(providerCfg, selected, false)
			require.NoError(t, err)
			lm, err := provider.LanguageModel(t.Context(), tc.modelID)
			require.NoError(t, err)
			_, _ = lm.Generate(t.Context(), fantasy.Call{Prompt: fantasy.Prompt{fantasy.NewUserMessage("hi")}})

			require.Contains(t, <-beta, "interleaved-thinking-2025-05-14")
		})
	}
}

// TestWorkaroundProviderMediaLimitationsFollowsWire keeps media in the tool
// results of every model called over the Anthropic protocol, whatever the
// provider is called.
func TestWorkaroundProviderMediaLimitationsFollowsWire(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	messages := mediaTestMessages("ZmFrZS1wbmctZGF0YQ==")
	for _, providerCfg := range []config.ProviderConfig{
		{ID: "my-proxy", Type: catalog.TypeAnthropic},
		{ID: "vertexai", Type: catalog.TypeVertexAI},
		openCodeProvider(catalog.InferenceProviderOpenCodeZen, "https://opencode.ai/inference"),
	} {
		model := Model{
			ModelCfg:   config.SelectedModel{Provider: providerCfg.ID},
			CatalogCfg: catalog.Model{SupportsImages: true},
			Wire:       wireFamily(providerCfg, "claude-sonnet-4-5"),
		}
		result := agent.workaroundProviderMediaLimitations(messages, model)
		require.Equal(t, messages, result, providerCfg.ID)
	}
}

// TestBuildModelRecordsWire pins Model.Wire to the protocol the model was
// built for.
func TestBuildModelRecordsWire(t *testing.T) {
	t.Parallel()

	providerCfg := openCodeProvider(catalog.InferenceProviderOpenCodeGo, "https://opencode.ai/inference/go")
	m, err := wireTestCoordinator(t).buildModel(t.Context(), providerCfg,
		config.SelectedModel{Provider: providerCfg.ID, Model: "minimax-m3"},
		catalog.Model{ID: "minimax-m3"}, false)
	require.NoError(t, err)
	require.Equal(t, catalog.TypeAnthropic, m.Wire)
	require.Equal(t, anthropic.Name, m.Model.Provider())
}

func openCodeProvider(id catalog.InferenceProvider, root string) config.ProviderConfig {
	messages := root + "/anthropic/v1"
	return config.ProviderConfig{
		ID:             id,
		Type:           catalog.TypeOpenAICompat,
		BaseURL:        root + "/openai/v1",
		APIKey:         "k",
		ResolvedAPIKey: "k",
		Models: []catalog.Model{
			{ID: "kimi-k3"},
			{ID: "gpt-5.5", Wire: catalog.TypeOpenAI},
			{ID: "gpt-5.6-luna", Wire: catalog.TypeOpenAI},
			{ID: "grok-4.5", Wire: catalog.TypeOpenAI},
			{ID: "muse-spark-pro", Wire: catalog.TypeOpenAI},
			{ID: "minimax-m3", Wire: catalog.TypeAnthropic, APIEndpoint: messages},
			{ID: "claude-sonnet-4-5", Wire: catalog.TypeAnthropic, APIEndpoint: messages},
			{ID: "gemini-4-argon-contributor", Wire: catalog.TypeGoogle, APIEndpoint: root + "/google/v1beta"},
		},
	}
}

func TestBuildProviderSendsOpenCodeModelsToTheirEndpoints(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		modelID string
		path    string
	}{
		{"kimi-k3", "/inference/go/openai/v1/chat/completions"},
		{"gpt-5.6-luna", "/inference/go/openai/v1/responses"},
		{"minimax-m3", "/inference/go/anthropic/v1/messages"},
		{"gemini-4-argon-contributor", "/inference/go/google/v1beta/models/gemini-4-argon-contributor:generateContent"},
	} {
		t.Run(tc.modelID, func(t *testing.T) {
			t.Parallel()

			type seen struct{ path, beta string }
			requests := make(chan seen, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case requests <- seen{r.URL.Path, r.Header.Get("anthropic-beta")}:
				default:
				}
				http.Error(w, `{"error":{"message":"stop"}}`, http.StatusBadRequest)
			}))
			t.Cleanup(srv.Close)

			providerCfg := openCodeProvider(catalog.InferenceProviderOpenCodeGo, srv.URL+"/inference/go")
			selected := config.SelectedModel{Provider: providerCfg.ID, Model: tc.modelID, Think: true}
			provider, err := wireTestCoordinator(t).buildProvider(providerCfg, selected, false)
			require.NoError(t, err)
			lm, err := provider.LanguageModel(t.Context(), tc.modelID)
			require.NoError(t, err)
			_, _ = lm.Generate(t.Context(), fantasy.Call{Prompt: fantasy.Prompt{fantasy.NewUserMessage("hi")}})

			got := <-requests
			require.Equal(t, tc.path, got.path)
			if tc.modelID == "minimax-m3" {
				require.Contains(t, got.beta, "interleaved-thinking-2025-05-14")
			}
		})
	}
}

func TestSessionHeadersIdentifyTheOpenCodeSession(t *testing.T) {
	t.Parallel()

	headers := sessionHeaders("session-1")
	require.NotEmpty(t, headers["x-opencode-session"])
	require.Equal(t, headers["x-session-id"], headers["x-opencode-session"])
	require.Equal(t, headers, sessionHeaders("session-1"), "the session id is stable across requests")
	require.NotEqual(t, headers["x-opencode-session"], sessionHeaders("session-2")["x-opencode-session"])
}
