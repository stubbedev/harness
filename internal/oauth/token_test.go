package oauth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestToOAuth2_ZeroExpiryMeansNoExpiry(t *testing.T) {
	t.Parallel()

	tok := (&Token{AccessToken: "a", RefreshToken: "r"}).ToOAuth2()
	require.True(t, tok.Expiry.IsZero(), "a zero ExpiresAt must not become 1970")
	require.True(t, tok.Valid())
}

func TestOAuth2RoundTrip(t *testing.T) {
	t.Parallel()

	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	cfg := &oauth2.Config{
		ClientID: "id",
		Endpoint: oauth2.Endpoint{TokenURL: "https://example.com/token", AuthStyle: oauth2.AuthStyleInHeader},
	}
	saved := FromOAuth2(cfg, &oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: expiry})
	require.Equal(t, expiry.Unix(), saved.ExpiresAt)
	require.Positive(t, saved.ExpiresIn)

	restored := saved.ToOAuth2()
	require.Equal(t, "a", restored.AccessToken)
	require.Equal(t, "r", restored.RefreshToken)
	require.True(t, expiry.Equal(restored.Expiry))
	require.Equal(t, cfg, saved.Client.Config())

	noExpiry := FromOAuth2(nil, &oauth2.Token{AccessToken: "a"})
	require.Zero(t, noExpiry.ExpiresAt)
	require.Nil(t, noExpiry.Client)
}
