package config

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/oauth/copilot"
	"github.com/stubbedev/harness/internal/oauth/opencode"
)

func TestSignInFlowCoversTheOpenCodeGateways(t *testing.T) {
	t.Parallel()

	for _, id := range []catalog.InferenceProvider{catalog.InferenceProviderOpenCodeGo, catalog.InferenceProviderOpenCodeZen} {
		flow, ok := SignInFlow(id)
		require.True(t, ok, id)
		require.IsType(t, opencode.DeviceFlow{}, flow, id)
	}
	flow, ok := SignInFlow(catalog.InferenceProviderCopilot)
	require.True(t, ok)
	require.IsType(t, copilot.DeviceFlow{}, flow)

	_, ok = SignInFlow(catalog.InferenceProviderAnthropic)
	require.False(t, ok, "providers that take an API key have no sign-in")
}

func TestUseOAuthTokenSendsTheTokenHeaders(t *testing.T) {
	t.Parallel()

	shared := map[string]string{"X-Custom": "kept"}
	pc := ProviderConfig{ID: catalog.InferenceProviderOpenCodeGo, ExtraHeaders: shared}
	pc.UseOAuthToken(&oauth.Token{AccessToken: "st_access", Headers: map[string]string{opencode.OrgHeader: "org_a"}})

	require.Equal(t, "st_access", pc.ResolvedAPIKey)
	require.NoError(t, pc.APIKeyResolutionError)
	require.Equal(t, map[string]string{"X-Custom": "kept", opencode.OrgHeader: "org_a"}, pc.ExtraHeaders)
	require.Equal(t, map[string]string{"X-Custom": "kept"}, shared, "the shared header map is not written to")
}

func TestUseOAuthTokenAddsCopilotHeaders(t *testing.T) {
	t.Parallel()

	pc := ProviderConfig{ID: catalog.InferenceProviderCopilot}
	pc.UseOAuthToken(&oauth.Token{AccessToken: "tid"})
	for name, value := range copilot.Headers() {
		require.Equal(t, value, pc.ExtraHeaders[name], name)
	}
}

func TestRefreshKeepsTheHeadersOfTheReplacedToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "harness.yaml")
	store := newRefreshTestStore(t, configPath, func(context.Context, catalog.InferenceProvider, string) (*oauth.Token, error) {
		return &oauth.Token{AccessToken: "at1", RefreshToken: "rt1", ExpiresIn: 3600, ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
	})
	pc, _ := store.config.Providers.Get("rotator")
	pc.OAuthToken.Headers = map[string]string{opencode.OrgHeader: "org_a"}
	store.config.Providers.Set("rotator", pc)

	require.NoError(t, store.RefreshOAuthToken(t.Context(), ScopeGlobal, "rotator"))

	pc, _ = store.config.Providers.Get("rotator")
	require.Equal(t, "at1", pc.ResolvedAPIKey)
	require.Equal(t, map[string]string{opencode.OrgHeader: "org_a"}, pc.OAuthToken.Headers)
	require.Equal(t, "org_a", pc.ExtraHeaders[opencode.OrgHeader], "a refreshed token is still billed to the signed-in organization")
	persisted, err := readConfigJSON(configPath)
	require.NoError(t, err)
	require.Equal(t, "org_a", gjson.GetBytes(persisted, "providers.rotator.oauth.headers.x-org-id").String(), "the persisted token keeps its headers")
}
