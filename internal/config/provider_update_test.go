package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/stubbedev/harness/internal/catalog"
)

func newProviderUpdateStore(t *testing.T, state string) (*ConfigStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), stateConfigFile)
	require.NoError(t, os.WriteFile(path, []byte(state), 0o600))
	store := NewTestStore(&Config{}, path)
	store.globalDataPath = path
	return store, path
}

const providerState = `models:
  large:
    provider: anthropic
    model: claude
providers:
  anthropic:
    api_key: $ANTHROPIC_API_KEY
    base_url: https://proxy.example.com/v1
    extra_headers:
      X-Org: $ORG
    system_prompt_prefix: keep me
`

func TestUpdateProviderWritesOnlyChangedFields(t *testing.T) {
	t.Parallel()
	store, path := newProviderUpdateStore(t, providerState)

	err := store.UpdateProvider(context.Background(), ScopeGlobal, "anthropic", ProviderUpdate{
		Set:   map[ProviderField]any{ProviderFieldAPIKey: "$(op read op://p/k)"},
		Unset: []ProviderField{ProviderFieldBaseURL},
	})
	require.NoError(t, err)

	settings, ok, err := store.ProviderSettings("anthropic")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "$(op read op://p/k)", settings.APIKey, "references are written as typed")
	require.Empty(t, settings.BaseURL)
	require.Equal(t, map[string]string{"X-Org": "$ORG"}, settings.ExtraHeaders)
	require.Equal(t, "keep me", settings.SystemPromptPrefix, "fields the form does not know survive")

	raw, err := readConfigJSON(path)
	require.NoError(t, err)
	require.JSONEq(t, `{"provider":"anthropic","model":"claude"}`, string(gjsonGet(raw, "models.large")), "model selection is never touched")
}

func TestUpdateProviderCreatesNewProvider(t *testing.T) {
	t.Parallel()
	store, _ := newProviderUpdateStore(t, "")

	err := store.UpdateProvider(context.Background(), ScopeGlobal, "my.work", ProviderUpdate{Set: map[ProviderField]any{
		ProviderFieldType:    "openai-compat",
		ProviderFieldBaseURL: "https://llm.example.com/v1",
		ProviderFieldModels:  []catalog.Model{{ID: "big", Name: "big"}},
	}})
	require.NoError(t, err)

	settings, ok, err := store.ProviderSettings("my.work")
	require.NoError(t, err)
	require.True(t, ok, "provider IDs with dots are escaped in the path")
	require.Equal(t, catalog.TypeOpenAICompat, settings.Type)
	require.Equal(t, []catalog.Model{{ID: "big", Name: "big"}}, settings.Models)
}

func TestUpdateProviderRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	store, path := newProviderUpdateStore(t, providerState)

	require.ErrorContains(t, store.UpdateProvider(context.Background(), ScopeGlobal, "anthropic", ProviderUpdate{
		Set: map[ProviderField]any{"oauth": "x"},
	}), "cannot be written")
	require.ErrorContains(t, store.UpdateProvider(context.Background(), ScopeGlobal, "anthropic", ProviderUpdate{
		Set:   map[ProviderField]any{ProviderFieldAPIKey: "x"},
		Unset: []ProviderField{ProviderFieldAPIKey},
	}), "both set and unset")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "$ANTHROPIC_API_KEY")
}

func TestRemoveProviderDeletesOnlyThatProvider(t *testing.T) {
	t.Parallel()
	store, path := newProviderUpdateStore(t, providerState)

	require.NoError(t, store.RemoveProvider(context.Background(), ScopeGlobal, "anthropic"))

	_, ok, err := store.ProviderSettings("anthropic")
	require.NoError(t, err)
	require.False(t, ok)
	raw, err := readConfigJSON(path)
	require.NoError(t, err)
	require.NotEmpty(t, gjsonGet(raw, "models.large"))
}

func TestProviderSettingsMergesLoadedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	user := filepath.Join(dir, "config.yaml")
	state := filepath.Join(dir, stateConfigFile)
	require.NoError(t, os.WriteFile(user, []byte("providers:\n  openai:\n    api_key: $OPENAI_API_KEY\n    base_url: https://a\n"), 0o600))
	require.NoError(t, os.WriteFile(state, []byte("providers:\n  openai:\n    base_url: https://b\n"), 0o600))

	settings, ok, err := NewTestStore(&Config{}, user, state).ProviderSettings("openai")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "$OPENAI_API_KEY", settings.APIKey, "the reference, not the resolved secret")
	require.Equal(t, "https://b", settings.BaseURL)
}

func TestProviderUpdateApplyTo(t *testing.T) {
	t.Parallel()

	got, err := ProviderUpdate{
		Set:   map[ProviderField]any{ProviderFieldRegion: "eu-west-1"},
		Unset: []ProviderField{ProviderFieldAPIKey},
	}.ApplyTo(ProviderConfig{ID: "amazon-bedrock", APIKey: "ABSKx", Profile: "work"})
	require.NoError(t, err)
	require.Equal(t, ProviderConfig{ID: "amazon-bedrock", Region: "eu-west-1", Profile: "work"}, got)
}

func gjsonGet(data []byte, path string) []byte {
	return []byte(gjson.GetBytes(data, path).Raw)
}
