package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stubbedev/harness/internal/oauth"
)

const (
	clientID = "Iv1.b507a08c87ecfe98"

	deviceCodeURL   = "https://github.com/login/device/code"
	accessTokenURL  = "https://github.com/login/oauth/access_token"
	copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

var ErrNotAvailable = errors.New("github copilot not available")

// DeviceFlow is GitHub's device authorization grant for Copilot.
type DeviceFlow struct{}

var _ oauth.DeviceFlow = DeviceFlow{}

// RequestDeviceCode initiates the device code flow with GitHub.
func (DeviceFlow) RequestDeviceCode(ctx context.Context) (*oauth.DeviceCode, error) {
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("scope", "read:user")

	req, err := http.NewRequestWithContext(ctx, "POST", deviceCodeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("device code request failed: %s - %s", resp.Status, string(body))
	}

	var dc oauth.DeviceCode
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return nil, err
	}
	return &dc, nil
}

// PollForToken polls GitHub for the access token after user authorization.
func (DeviceFlow) PollForToken(ctx context.Context, dc *oauth.DeviceCode) (*oauth.Token, error) {
	return oauth.PollDeviceToken(ctx, dc, func(ctx context.Context) (*oauth.Token, error) {
		return tryGetToken(ctx, dc.DeviceCode)
	})
}

func tryGetToken(ctx context.Context, deviceCode string) (*oauth.Token, error) {
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("device_code", deviceCode)
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, "POST", accessTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	switch result.Error {
	case "":
		if result.AccessToken == "" {
			return nil, oauth.ErrAuthorizationPending
		}
		return getCopilotToken(ctx, result.AccessToken)
	case "authorization_pending":
		return nil, oauth.ErrAuthorizationPending
	case "slow_down":
		return nil, oauth.ErrSlowDown
	default:
		return nil, fmt.Errorf("authorization failed: %s", result.Error)
	}
}

func getCopilotToken(ctx context.Context, githubToken string) (*oauth.Token, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", copilotTokenURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", githubToken))
	for k, v := range Headers() {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusForbidden {
		return nil, ErrNotAvailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot token request failed: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	copilotToken := &oauth.Token{
		AccessToken:  result.Token,
		RefreshToken: githubToken,
		ExpiresAt:    result.ExpiresAt,
	}
	copilotToken.SetExpiresIn()

	return copilotToken, nil
}

// Refresh refreshes the Copilot token using the GitHub token.
func (DeviceFlow) Refresh(ctx context.Context, githubToken string) (*oauth.Token, error) {
	return getCopilotToken(ctx, githubToken)
}
