package config

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/env"
)

type recordedRequest struct {
	path    string
	query   string
	headers http.Header
}

func providerServer(t *testing.T, status int, body string) (*httptest.Server, *recordedRequest) {
	t.Helper()
	rec := &recordedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.URL.Path
		rec.query = r.URL.RawQuery
		rec.headers = r.Header.Clone()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func testResolver(vars map[string]string) VariableResolver {
	return NewShellVariableResolver(env.NewFromMap(vars))
}

func TestConnectionSurfacesProviderErrorMessage(t *testing.T) {
	t.Parallel()

	srv, _ := providerServer(t, http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	pc := ProviderConfig{ID: "anthropic", Type: catalog.TypeAnthropic, APIKey: "bad", BaseURL: srv.URL}

	err := pc.TestConnection(context.Background(), testResolver(nil))
	var connErr *ConnectionError
	require.True(t, errors.As(err, &connErr), "%v", err)
	require.Equal(t, http.StatusUnauthorized, connErr.Status)
	require.Equal(t, "invalid x-api-key", connErr.Message)
	require.True(t, connErr.CredentialsRejected())
	require.EqualError(t, err, "anthropic rejected the credentials: invalid x-api-key")
}

func TestConnectionDistinguishesForbiddenFromRejected(t *testing.T) {
	t.Parallel()

	srv, _ := providerServer(t, http.StatusForbidden, `{"error":{"message":"key cannot access workspace wrk_1"}}`)
	pc := ProviderConfig{ID: "anthropic", Type: catalog.TypeAnthropic, APIKey: "ok", BaseURL: srv.URL}

	err := pc.TestConnection(context.Background(), testResolver(nil))
	require.EqualError(t, err, "anthropic accepted the credentials but denied access: key cannot access workspace wrk_1")
}

func TestConnectionFallsBackToStatusText(t *testing.T) {
	t.Parallel()

	srv, _ := providerServer(t, http.StatusNotFound, `<html>nope</html>`)
	pc := ProviderConfig{ID: "custom", Type: catalog.TypeOpenAICompat, APIKey: "k", BaseURL: srv.URL}

	require.EqualError(t, pc.TestConnection(context.Background(), testResolver(nil)),
		"custom could not find the endpoint, project or model: Not Found")
}

func TestConnectionSendsResolvedHeadersAndKey(t *testing.T) {
	t.Parallel()

	srv, rec := providerServer(t, http.StatusOK, `{}`)
	pc := ProviderConfig{
		ID: "custom", Type: catalog.TypeOpenAICompat, APIKey: "$KEY", BaseURL: srv.URL + "/v1",
		ExtraHeaders: map[string]string{"X-Org": "$ORG"},
	}

	require.NoError(t, pc.TestConnection(context.Background(), testResolver(map[string]string{"KEY": "sk-1", "ORG": "acme"})))
	require.Equal(t, "/v1/models", rec.path)
	require.Equal(t, "Bearer sk-1", rec.headers.Get("Authorization"))
	require.Equal(t, "acme", rec.headers.Get("X-Org"), "header references are resolved, not sent verbatim")
}

func TestConnectionOpenCodeProbesGatewayModels(t *testing.T) {
	t.Parallel()

	for id, base := range map[catalog.InferenceProvider]string{
		catalog.InferenceProviderOpenCodeGo:  "/inference/go",
		catalog.InferenceProviderOpenCodeZen: "/inference",
	} {
		srv, rec := providerServer(t, http.StatusOK, `{}`)
		pc := ProviderConfig{ID: id, Type: catalog.TypeOpenAICompat, APIKey: "sk-1", BaseURL: srv.URL + base + "/openai/v1"}

		require.NoError(t, pc.TestConnection(context.Background(), testResolver(nil)), id)
		require.Equal(t, base+"/v1/models", rec.path, id)
		require.Equal(t, "Bearer sk-1", rec.headers.Get("Authorization"), id)
	}
}

func TestConnectionAzure(t *testing.T) {
	t.Parallel()

	srv, rec := providerServer(t, http.StatusOK, `{}`)
	pc := ProviderConfig{ID: "azure", Type: catalog.TypeAzure, APIKey: "az", BaseURL: srv.URL + "/", APIVersion: "$VER"}

	require.NoError(t, pc.TestConnection(context.Background(), testResolver(map[string]string{"VER": "2025-04-01"})))
	require.Equal(t, "/openai/models", rec.path)
	require.Equal(t, "api-version=2025-04-01", rec.query)
	require.Equal(t, "az", rec.headers.Get("api-key"))

	pc.BaseURL = ""
	require.ErrorIs(t, pc.TestConnection(context.Background(), testResolver(nil)), errMissingParam)
}

func TestConnectionVertexRequiresProjectAndLocation(t *testing.T) {
	t.Parallel()

	pc := ProviderConfig{ID: "google-vertex", Type: catalog.TypeVertexAI, Location: "us-east5"}
	require.ErrorIs(t, pc.TestConnection(context.Background(), testResolver(nil)), errMissingParam)
}

func TestConnectionBedrockKeyFormat(t *testing.T) {
	t.Parallel()

	pc := ProviderConfig{ID: "amazon-bedrock", Type: catalog.TypeBedrock, APIKey: "nope"}
	require.EqualError(t, pc.TestConnection(context.Background(), testResolver(nil)), "not a valid bedrock api key")
	pc.APIKey = "ABSKabc"
	require.NoError(t, pc.TestConnection(context.Background(), testResolver(nil)))
}

func TestResolveTypeParams(t *testing.T) {
	t.Parallel()

	r := testResolver(map[string]string{"VERTEXAI_PROJECT": "env-proj", "VERTEXAI_LOCATION": "env-loc", "AZURE_OPENAI_API_VERSION": "env-ver", "PROFILE": "work"})

	vertex := ProviderConfig{ID: "v", Type: catalog.TypeVertexAI, Project: "cfg-proj"}
	params, err := vertex.ResolveTypeParams(r)
	require.NoError(t, err)
	require.Equal(t, map[string]string{ProviderParamProject: "cfg-proj", ProviderParamLocation: "env-loc"}, params)

	azure := ProviderConfig{ID: "a", Type: catalog.TypeAzure}
	params, err = azure.ResolveTypeParams(r)
	require.NoError(t, err)
	require.Equal(t, map[string]string{ProviderParamAPIVersion: "env-ver"}, params)

	bedrock := ProviderConfig{ID: "b", Type: catalog.TypeBedrock, Region: "eu-west-1", Profile: "$PROFILE"}
	params, err = bedrock.ResolveTypeParams(r)
	require.NoError(t, err)
	require.Equal(t, map[string]string{ProviderParamRegion: "eu-west-1", ProviderParamProfile: "work"}, params)

	bare := ProviderConfig{ID: "b", Type: catalog.TypeBedrock}
	params, err = bare.ResolveTypeParams(r)
	require.NoError(t, err)
	require.Empty(t, params, "an unset region is left to the AWS SDK")
}

func TestConfigureProvidersReadsTypeSettingsFromConfig(t *testing.T) {
	t.Parallel()

	known := []catalog.Provider{
		{ID: catalog.InferenceProviderVertexAI, Type: catalog.TypeVertexAI, Models: []catalog.Model{{ID: "gemini"}}},
		{ID: catalog.InferenceProviderAzure, Type: catalog.TypeAzure, APIEndpoint: "$AZURE_OPENAI_API_ENDPOINT", Models: []catalog.Model{{ID: "gpt"}}},
		{ID: catalog.InferenceProviderBedrock, Type: catalog.TypeBedrock, APIKey: "$AWS_ACCESS_KEY_ID", Models: []catalog.Model{{ID: "claude"}}},
	}
	cfg := &Config{}
	cfg.setDefaults("/tmp", "")
	cfg.Providers.Set(catalog.InferenceProviderVertexAI, ProviderConfig{Project: "proj", Location: "$LOC"})
	cfg.Providers.Set(catalog.InferenceProviderAzure, ProviderConfig{BaseURL: "https://r.openai.azure.com", APIKey: "k", APIVersion: "2025-04-01"})
	cfg.Providers.Set(catalog.InferenceProviderBedrock, ProviderConfig{Region: "eu-west-1", Profile: "work"})
	e := env.NewFromMap(map[string]string{"LOC": "us-east5"})

	require.NoError(t, cfg.configureProviders(context.Background(), testStore(cfg), e, NewShellVariableResolver(e), known))

	vertex, ok := cfg.Providers.Get(catalog.InferenceProviderVertexAI)
	require.True(t, ok, "Vertex configured from the config, without VERTEXAI_* in the environment")
	require.Equal(t, "proj", vertex.ExtraParams[ProviderParamProject])
	require.Equal(t, "us-east5", vertex.ExtraParams[ProviderParamLocation])

	azure, ok := cfg.Providers.Get(catalog.InferenceProviderAzure)
	require.True(t, ok)
	require.Equal(t, "2025-04-01", azure.ExtraParams[ProviderParamAPIVersion])

	bedrock, ok := cfg.Providers.Get(catalog.InferenceProviderBedrock)
	require.True(t, ok, "a configured profile is a credential source")
	require.Equal(t, "eu-west-1", bedrock.ExtraParams[ProviderParamRegion])
	require.Equal(t, "work", bedrock.ExtraParams[ProviderParamProfile])
}
