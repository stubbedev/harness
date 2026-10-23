package oauth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPollDeviceTokenWaitsWhileAuthorizationIsPending(t *testing.T) {
	t.Parallel()

	answers := []error{ErrAuthorizationPending, ErrAuthorizationPending, nil}
	calls := 0
	token, err := PollDeviceToken(t.Context(), &DeviceCode{ExpiresIn: 60, Interval: 1}, func(context.Context) (*Token, error) {
		err := answers[calls]
		calls++
		if err != nil {
			return nil, err
		}
		return &Token{AccessToken: "granted"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, "granted", token.AccessToken)
	require.Equal(t, 3, calls)
}

func TestPollDeviceTokenStopsOnADenial(t *testing.T) {
	t.Parallel()

	denied := errors.New("access_denied")
	calls := 0
	_, err := PollDeviceToken(t.Context(), &DeviceCode{ExpiresIn: 60, Interval: 1}, func(context.Context) (*Token, error) {
		calls++
		return nil, denied
	})
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, calls)
}

func TestPollDeviceTokenHonoursCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := PollDeviceToken(ctx, &DeviceCode{ExpiresIn: 60, Interval: 1}, func(context.Context) (*Token, error) {
		t.Fatal("a cancelled poll must not call the token endpoint")
		return nil, nil
	})
	require.ErrorIs(t, err, context.Canceled)
}
