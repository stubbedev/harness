package config

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
)

func TestProviderIDFieldsAreTyped(t *testing.T) {
	t.Parallel()

	want := reflect.TypeFor[catalog.InferenceProvider]()

	id, ok := reflect.TypeFor[ProviderConfig]().FieldByName("ID")
	require.True(t, ok)
	require.Equal(t, want, id.Type)

	provider, ok := reflect.TypeFor[SelectedModel]().FieldByName("Provider")
	require.True(t, ok)
	require.Equal(t, want, provider.Type)

	cfg := Config{}
	copyMethod, ok := reflect.TypeOf(cfg.Providers).MethodByName("Copy")
	require.True(t, ok)
	require.Equal(t, want, copyMethod.Type.Out(0).Key())
}

func TestProviderIDJSONShapeUnchanged(t *testing.T) {
	t.Parallel()

	const doc = `{"models":{"large":{"model":"claude-sonnet-4-5","provider":"my-proxy"}},` +
		`"providers":{"my-proxy":{"id":"my-proxy","type":"anthropic","base_url":"https://proxy.example/v1"}}}`

	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(doc), &cfg))

	large := cfg.Models[SelectedModelTypeLarge]
	require.Equal(t, catalog.InferenceProvider("my-proxy"), large.Provider)
	pc, ok := cfg.Providers.Get(large.Provider)
	require.True(t, ok)
	require.Equal(t, large.Provider, pc.ID)
	require.Equal(t, catalog.TypeAnthropic, pc.Type)

	out, err := json.Marshal(struct {
		Models    map[SelectedModelType]SelectedModel `json:"models"`
		Providers any                                 `json:"providers"`
	}{cfg.Models, cfg.Providers})
	require.NoError(t, err)
	require.JSONEq(t, doc, string(out))
}
