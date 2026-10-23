// Package opencode signs Harness in to an OpenCode Console account with
// the device authorization grant the OpenCode CLI uses. The resulting
// access token is the credential for both OpenCode gateways, Zen and Go.
package opencode

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/version"
)

const (
	defaultServer = "https://opencode.ai/console"
	clientID      = "opencode-cli"

	deviceCodeGrant   = "urn:ietf:params:oauth:grant-type:device_code"
	refreshTokenGrant = "refresh_token"

	// OrgHeader names the console organization (workspace) a request is
	// billed to. An account token spans every organization the account
	// belongs to, so the gateway needs it to pick one.
	OrgHeader = "x-org-id"

	requestTimeout = 30 * time.Second
	errorBodyLimit = 4096
)

// DeviceFlow is the OpenCode Console device authorization grant. Server
// is the console base URL; empty means https://opencode.ai/console.
type DeviceFlow struct {
	Server string
}

var _ oauth.DeviceFlow = DeviceFlow{}

func (f DeviceFlow) server() string {
	return strings.TrimSuffix(cmp.Or(f.Server, defaultServer), "/")
}

// RequestDeviceCode starts a sign-in and returns the code the user
// enters at the console.
func (f DeviceFlow) RequestDeviceCode(ctx context.Context) (*oauth.DeviceCode, error) {
	var device struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	resp, err := f.post(ctx, "/auth/device/code", map[string]string{"client_id": clientID})
	if err != nil {
		return nil, err
	}
	if err := decodeOK(resp, &device); err != nil {
		return nil, fmt.Errorf("requesting an OpenCode device code: %w", err)
	}
	verification, err := f.absoluteURL(device.VerificationURIComplete)
	if err != nil {
		return nil, err
	}
	return &oauth.DeviceCode{
		DeviceCode:      device.DeviceCode,
		UserCode:        device.UserCode,
		VerificationURI: verification,
		ExpiresIn:       device.ExpiresIn,
		Interval:        device.Interval,
	}, nil
}

func (f DeviceFlow) absoluteURL(ref string) (string, error) {
	base, err := url.Parse(f.server() + "/")
	if err != nil {
		return "", fmt.Errorf("parsing OpenCode console URL %q: %w", f.server(), err)
	}
	resolved, err := base.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("parsing OpenCode verification URL %q: %w", ref, err)
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", fmt.Errorf("OpenCode verification URL %q is not HTTP(S)", ref)
	}
	return resolved.String(), nil
}

// PollForToken waits for the user to approve the sign-in, then returns
// the account token carrying the organization header its requests need.
func (f DeviceFlow) PollForToken(ctx context.Context, dc *oauth.DeviceCode) (*oauth.Token, error) {
	token, err := oauth.PollDeviceToken(ctx, dc, func(ctx context.Context) (*oauth.Token, error) {
		return f.exchange(ctx, map[string]string{
			"grant_type":  deviceCodeGrant,
			"device_code": dc.DeviceCode,
			"client_id":   clientID,
		})
	})
	if err != nil {
		return nil, err
	}
	org, err := f.defaultOrg(ctx, token.AccessToken)
	if err != nil {
		return nil, err
	}
	token.Headers = map[string]string{OrgHeader: org}
	return token, nil
}

// Refresh exchanges a refresh token for a new access token. The result
// carries no headers; the caller keeps those of the token it replaces.
func (f DeviceFlow) Refresh(ctx context.Context, refreshToken string) (*oauth.Token, error) {
	return f.exchange(ctx, map[string]string{
		"grant_type":    refreshTokenGrant,
		"refresh_token": refreshToken,
		"client_id":     clientID,
	})
}

func (f DeviceFlow) exchange(ctx context.Context, body map[string]string) (*oauth.Token, error) {
	resp, err := f.post(ctx, "/auth/device/token", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("reading the OpenCode token response: %w", err)
	}
	var result struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, &oauth.TokenExchangeError{StatusCode: resp.StatusCode, Body: string(raw)}
	}
	switch result.Error {
	case "":
	case "authorization_pending":
		return nil, oauth.ErrAuthorizationPending
	case "slow_down":
		return nil, oauth.ErrSlowDown
	default:
		return nil, &oauth.TokenExchangeError{StatusCode: resp.StatusCode, Body: string(raw)}
	}
	if resp.StatusCode != http.StatusOK || result.AccessToken == "" {
		return nil, &oauth.TokenExchangeError{StatusCode: resp.StatusCode, Body: string(raw)}
	}
	token := &oauth.Token{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresIn:    result.ExpiresIn,
	}
	token.SetExpiresAt()
	return token, nil
}

type org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// defaultOrg picks the organization requests are billed to the way the
// OpenCode CLI does: the first by name, then by id.
func (f DeviceFlow) defaultOrg(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.server()+"/api/orgs", nil)
	if err != nil {
		return "", fmt.Errorf("building the OpenCode organizations request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := do(req)
	if err != nil {
		return "", err
	}
	var orgs []org
	if err := decodeOK(resp, &orgs); err != nil {
		return "", fmt.Errorf("listing OpenCode organizations: %w", err)
	}
	if len(orgs) == 0 {
		return "", fmt.Errorf("the OpenCode account belongs to no organization")
	}
	return slices.MinFunc(orgs, func(a, b org) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	}).ID, nil
}

func (f DeviceFlow) post(ctx context.Context, path string, body map[string]string) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding the OpenCode request to %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.server()+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("building the OpenCode request to %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return do(req)
}

func do(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", "Harness/"+version.Version)
	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling OpenCode %s: %w", req.URL.Path, err)
	}
	return resp, nil
}

func decodeOK(resp *http.Response, into any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("decoding the response: %w", err)
	}
	return nil
}
