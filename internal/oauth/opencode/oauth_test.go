package opencode

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/oauth"
)

type fakeConsole struct {
	mu           sync.Mutex
	pending      int
	tokenBody    string
	orgs         string
	config       string
	requests     []map[string]string
	orgsBearer   string
	configOrgIDs []string
}

func (c *fakeConsole) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /console/auth/device/code", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, clientID, body["client_id"])
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		_, _ = w.Write([]byte(`{"device_code":"dev-1","user_code":"ABCD-EFGH","verification_uri":"/console/device","verification_uri_complete":"/console/device?user_code=ABCD-EFGH&client_id=opencode-cli","expires_in":600,"interval":1}`))
	})
	mux.HandleFunc("POST /console/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		c.mu.Lock()
		defer c.mu.Unlock()
		c.requests = append(c.requests, body)
		if c.pending > 0 {
			c.pending--
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending","error_description":"waiting"}`))
			return
		}
		_, _ = w.Write([]byte(c.tokenBody))
	})
	mux.HandleFunc("GET /console/api/orgs", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.orgsBearer = r.Header.Get("Authorization")
		c.mu.Unlock()
		_, _ = w.Write([]byte(c.orgs))
	})
	mux.HandleFunc("GET /console/api/config", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.configOrgIDs = append(c.configOrgIDs, r.Header.Get("x-org-id"))
		c.mu.Unlock()
		_, _ = w.Write([]byte(c.config))
	})
	return mux
}

func newFakeConsole(t *testing.T, console *fakeConsole) DeviceFlow {
	t.Helper()
	srv := httptest.NewServer(console.handler(t))
	t.Cleanup(srv.Close)
	return DeviceFlow{Provider: "opencode-go", Server: srv.URL + "/console"}
}

func TestDeviceFlowSignsInAndPicksTheFirstOrganization(t *testing.T) {
	t.Parallel()

	console := &fakeConsole{
		pending:   1,
		tokenBody: `{"access_token":"st_access","refresh_token":"rt_1","token_type":"Bearer","expires_in":3600}`,
		orgs:      `[{"id":"org_z","name":"Zeta"},{"id":"org_b","name":"Acme"},{"id":"org_a","name":"Acme"}]`,
		config:    `{"config":{"provider":{"opencode":{"options":{"headers":{"x-opencode-org-id":"wrong-gateway"}}},"opencode-go":{"api":"https://opencode.ai/inference/go/openai/v1","options":{"apiKey":"{env:OPENCODE_CONSOLE_TOKEN}","headers":{"x-opencode-org-id":"org_a"}}}}}}`,
	}
	flow := newFakeConsole(t, console)

	dc, err := flow.RequestDeviceCode(t.Context())
	require.NoError(t, err)
	require.Equal(t, "ABCD-EFGH", dc.UserCode)
	require.Equal(t, flow.Server+"/device?user_code=ABCD-EFGH&client_id=opencode-cli", dc.VerificationURI,
		"the relative verification URL resolves against the console")

	token, err := flow.PollForToken(t.Context(), dc)
	require.NoError(t, err)
	require.Equal(t, "st_access", token.AccessToken)
	require.Equal(t, "rt_1", token.RefreshToken)
	require.False(t, token.IsExpired())
	require.Equal(t, map[string]string{"x-opencode-org-id": "org_a"}, token.Headers, "the token carries the headers the console configures for its gateway")
	require.Equal(t, []string{"org_a"}, console.configOrgIDs, "ties on name break on id, as the OpenCode CLI does")
	require.Equal(t, "Bearer st_access", console.orgsBearer)

	require.Len(t, console.requests, 2, "a pending answer is polled again")
	require.Equal(t, map[string]string{"grant_type": deviceCodeGrant, "device_code": "dev-1", "client_id": clientID}, console.requests[1])
}

func TestDeviceFlowRefreshUsesTheRefreshGrant(t *testing.T) {
	t.Parallel()

	console := &fakeConsole{tokenBody: `{"access_token":"st_new","refresh_token":"rt_2","expires_in":3600}`}
	flow := newFakeConsole(t, console)

	token, err := flow.Refresh(t.Context(), "rt_1")
	require.NoError(t, err)
	require.Equal(t, "st_new", token.AccessToken)
	require.Equal(t, "rt_2", token.RefreshToken)
	require.Nil(t, token.Headers, "the store keeps the headers of the token being replaced")
	require.Equal(t, []map[string]string{{"grant_type": refreshTokenGrant, "refresh_token": "rt_1", "client_id": clientID}}, console.requests)
}

func TestDeviceFlowReportsARevokedRefreshToken(t *testing.T) {
	t.Parallel()

	flow := newFakeConsole(t, &fakeConsole{tokenBody: `{"error":"invalid_grant","error_description":"refresh token revoked"}`})

	_, err := flow.Refresh(t.Context(), "rt_old")
	var exchangeErr *oauth.TokenExchangeError
	require.True(t, errors.As(err, &exchangeErr), "%v", err)
	require.True(t, exchangeErr.IsRefreshTokenRevoked(), "a revoked token sends the user back to sign in")
}

func TestDeviceFlowRejectsAWorkspaceWithoutTheGateway(t *testing.T) {
	t.Parallel()

	flow := newFakeConsole(t, &fakeConsole{
		tokenBody: `{"access_token":"st_access","refresh_token":"rt_1","expires_in":3600}`,
		orgs:      `[{"id":"org_a","name":"Acme"}]`,
		config:    `{"config":{"provider":{"opencode":{"options":{"headers":{"x-opencode-org-id":"org_a"}}}}}}`,
	})

	_, err := flow.PollForToken(t.Context(), &oauth.DeviceCode{DeviceCode: "dev-1", ExpiresIn: 60, Interval: 1})
	require.ErrorContains(t, err, "has no access to opencode-go")
}

func TestDeviceFlowRejectsHeadersItCannotResolve(t *testing.T) {
	t.Parallel()

	flow := newFakeConsole(t, &fakeConsole{
		tokenBody: `{"access_token":"st_access","refresh_token":"rt_1","expires_in":3600}`,
		orgs:      `[{"id":"org_a","name":"Acme"}]`,
		config:    `{"config":{"provider":{"opencode-go":{"options":{"headers":{"x-opencode-org-id":"{env:ORG}"}}}}}}`,
	})

	_, err := flow.PollForToken(t.Context(), &oauth.DeviceCode{DeviceCode: "dev-1", ExpiresIn: 60, Interval: 1})
	require.ErrorContains(t, err, "does not resolve")
}

func TestDeviceFlowRejectsAnAccountWithoutOrganizations(t *testing.T) {
	t.Parallel()

	flow := newFakeConsole(t, &fakeConsole{
		tokenBody: `{"access_token":"st_access","refresh_token":"rt_1","expires_in":3600}`,
		orgs:      `[]`,
	})

	_, err := flow.PollForToken(t.Context(), &oauth.DeviceCode{DeviceCode: "dev-1", ExpiresIn: 60, Interval: 1})
	require.ErrorContains(t, err, "no organization")
}

func TestDeviceFlowRejectsANonHTTPVerificationURL(t *testing.T) {
	t.Parallel()

	_, err := DeviceFlow{Server: "https://opencode.ai/console"}.absoluteURL("javascript:alert(1)")
	require.ErrorContains(t, err, "not HTTP(S)")
}
