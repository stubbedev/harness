package memory

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScrubRedactsKnownSecretShapes(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		notWant string
	}{
		{"anthropic key", "key sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "sk-ant-api03"},
		{"openai key", "key sk-proj-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "sk-proj"},
		{"github token", "token ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "ghp_"},
		{"github pat", "token github_pat_11AAAAAAA0aaaaaaaaaaaaaaaaaa", "github_pat_"},
		{"slack token", "token xoxb-123456789-abcdefghij", "xoxb-"},
		{"aws access key", "aws AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE"},
		{"google api key", "key AIzaSyA1234567890abcdefghijklmnopqrstuv", "AIza"},
		{"bearer header", "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9", "eyJhbGci"},
		{
			"password assignment",
			"db_password: hunter2hunter2",
			"hunter2hunter2",
		},
		{
			"api key assignment",
			`api_key = "abcdefghijklmnop"`,
			"abcdefghijklmnop",
		},
		{
			"pem block",
			"-----BEGIN RSA PRIVATE KEY-----\nMIIEow...\n-----END RSA PRIVATE KEY-----",
			"MIIEow",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, count := Scrub(tt.in)
			require.NotContains(t, got, tt.notWant)
			require.Greater(t, count, 0)
			require.Contains(t, got, "REDACTED")
		})
	}
}

func TestScrubLeavesBenignTextAlone(t *testing.T) {
	benign := []string{
		"The password field is required",
		"token: <none>",
		"secret sauce makes the pasta",
		"the author sketched out a plan",
		"AKIA is an AWS prefix",
		"password policy: rotate every 90 days",
	}
	for _, in := range benign {
		got, count := Scrub(in)
		require.Equal(t, in, got, in)
		require.Zero(t, count, in)
	}
}

func TestScrubKeepsKeyNamesUsable(t *testing.T) {
	got, count := Scrub("deploy with password: supersecretpw")
	require.Equal(t, 1, count)
	require.True(t, strings.HasPrefix(got, "deploy with password: "), got)
	require.NotContains(t, got, "supersecretpw")
}
