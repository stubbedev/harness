package agent

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

func TestCustomProviderRefreshAfterUnauthorizedUsesResolvedCredentialVerbatim(t *testing.T) {
	const apiKeyTemplate = "$CUSTOM_API_KEY"
	specialKey := `literal$MISSING 'single' "double" \path`
	t.Setenv("CUSTOM_API_KEY", specialKey)

	var requests atomic.Int32
	gotAuthorization := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization <- r.Header.Get("Authorization")
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"unauthorized"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)

	providerCfg := offlineTestProvider("model-a")
	providerCfg.BaseURL = server.URL + "/v1"
	providerCfg.APIKey = apiKeyTemplate
	providerCfg.ResolvedAPIKey = "expired-key"
	coord, agent := newUpdateModelsCoordinator(t, testEnv(t), providerCfg)
	selectTestModels(t, coord, "model-a")
	require.NoError(t, coord.UpdateModels(t.Context()))

	call := fantasy.Call{Prompt: fantasy.Prompt{fantasy.NewUserMessage("hello")}}
	_, err := agent.model.Model.Generate(t.Context(), call)
	require.Error(t, err)
	var providerErr *fantasy.ProviderError
	require.ErrorAs(t, err, &providerErr)
	require.Equal(t, http.StatusUnauthorized, providerErr.StatusCode)
	require.Equal(t, "Bearer expired-key", <-gotAuthorization)

	refresh := coord.makeAuthRefreshCallback(providerCfg)
	require.NotNil(t, refresh)
	require.NoError(t, refresh(t.Context(), providerErr))

	updated, ok := coord.cfg.Config().Providers.Get(providerCfg.ID)
	require.True(t, ok)
	require.Equal(t, apiKeyTemplate, updated.APIKey)
	require.Equal(t, specialKey, updated.ResolvedAPIKey)
	require.NoError(t, updated.APIKeyResolutionError)
	_, err = agent.model.Model.Generate(t.Context(), call)
	require.NoError(t, err)
	require.Equal(t, "Bearer "+specialKey, <-gotAuthorization)
}

func TestBuildProviderSurfacesAPIKeyResolutionErrors(t *testing.T) {
	providerCfg := config.ProviderConfig{
		ID:                    "custom-provider",
		Type:                  catalog.TypeOpenAICompat,
		BaseURL:               "https://api.custom.com/v1",
		APIKey:                "$(false)",
		APIKeyResolutionError: errors.New("command failed"),
	}
	coord := newTestCoordinator(t, testEnv(t), providerCfg.ID, providerCfg)

	_, err := coord.buildProvider(providerCfg, config.SelectedModel{Provider: providerCfg.ID, Model: "model"}, false)
	require.EqualError(t, err, "failed to resolve API key for provider custom-provider")
}

func TestBuildProviderSurfacesBaseURLResolutionErrors(t *testing.T) {
	providerCfg := config.ProviderConfig{
		ID:             "custom-provider",
		Type:           catalog.TypeOpenAICompat,
		BaseURL:        "$(false)",
		APIKey:         "test-key",
		ResolvedAPIKey: "test-key",
	}
	coord := newTestCoordinator(t, testEnv(t), providerCfg.ID, providerCfg)

	_, err := coord.buildProvider(providerCfg, config.SelectedModel{Provider: providerCfg.ID, Model: "model"}, false)
	require.ErrorContains(t, err, "failed to resolve base URL for provider custom-provider")
}

func TestBuildProviderUsesResolvedAPIKeyInsteadOfExpandingTemplateAgain(t *testing.T) {
	specialKey := `literal$MISSING 'single' "double" \path`
	gotAuthorization := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)

	providerCfg := config.ProviderConfig{
		ID:             "custom-provider",
		Type:           catalog.TypeOpenAICompat,
		BaseURL:        server.URL + "/v1",
		APIKey:         specialKey,
		ResolvedAPIKey: specialKey,
		Models:         []catalog.Model{{ID: "model", DefaultMaxTokens: 4096}},
	}
	coord := newTestCoordinator(t, testEnv(t), providerCfg.ID, providerCfg)
	provider, err := coord.buildProvider(providerCfg, config.SelectedModel{Provider: providerCfg.ID, Model: "model"}, false)
	require.NoError(t, err)
	model, err := provider.LanguageModel(t.Context(), "model")
	require.NoError(t, err)
	_, err = model.Generate(t.Context(), fantasy.Call{Prompt: fantasy.Prompt{fantasy.NewUserMessage("hello")}})
	require.NoError(t, err)
	require.Equal(t, "Bearer "+specialKey, <-gotAuthorization)
}
