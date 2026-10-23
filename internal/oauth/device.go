package oauth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DeviceCode is an RFC 8628 device authorization: the code the user
// enters at VerificationURI, and the device code polled for the token.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// DeviceFlow is a provider's device authorization grant, and the
// refresh that keeps the token it grants alive.
type DeviceFlow interface {
	RequestDeviceCode(ctx context.Context) (*DeviceCode, error)
	PollForToken(ctx context.Context, dc *DeviceCode) (*Token, error)
	Refresh(ctx context.Context, refreshToken string) (*Token, error)
}

// The pending states of a device token request (RFC 8628 section 3.5).
var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
)

const (
	defaultDevicePollInterval = 5 * time.Second
	slowDownIncrement         = 5 * time.Second
)

// PollDeviceToken calls exchange every interval until it returns a token,
// an error other than ErrAuthorizationPending or ErrSlowDown, or the
// device code expires.
func PollDeviceToken(ctx context.Context, dc *DeviceCode, exchange func(context.Context) (*Token, error)) (*Token, error) {
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = defaultDevicePollInterval
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}

		token, err := exchange(ctx)
		switch {
		case errors.Is(err, ErrAuthorizationPending):
		case errors.Is(err, ErrSlowDown):
			interval += slowDownIncrement
		case err != nil:
			return nil, err
		default:
			return token, nil
		}
		timer.Reset(interval)
	}
	return nil, fmt.Errorf("authorization timed out")
}
