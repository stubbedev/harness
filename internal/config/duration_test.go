package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDurationAcceptsStrings verifies the form people actually write in a
// config file.
func TestDurationAcceptsStrings(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{`"30s"`, 30 * time.Second},
		{`"2m"`, 2 * time.Minute},
		{`"1h30m"`, 90 * time.Minute},
		{`"500ms"`, 500 * time.Millisecond},
	} {
		var d Duration
		require.NoError(t, json.Unmarshal([]byte(tc.in), &d), tc.in)
		require.Equal(t, tc.want, d.Duration(), tc.in)
	}
}

// TestDurationAcceptsNanoseconds verifies the encoding/json representation of
// a plain time.Duration still loads.
func TestDurationAcceptsNanoseconds(t *testing.T) {
	t.Parallel()

	var d Duration
	require.NoError(t, json.Unmarshal([]byte("5000000000"), &d))
	require.Equal(t, 5*time.Second, d.Duration())
}

// TestDurationRejectsGarbage verifies a typo fails the load rather than
// silently becoming zero.
func TestDurationRejectsGarbage(t *testing.T) {
	t.Parallel()

	var d Duration
	require.Error(t, json.Unmarshal([]byte(`"30 seconds"`), &d))
	require.Error(t, json.Unmarshal([]byte(`true`), &d))
}

// TestDurationRoundTrip verifies a persisted duration reads back as the
// string a user would have typed.
func TestDurationRoundTrip(t *testing.T) {
	t.Parallel()

	out, err := json.Marshal(Duration(90 * time.Second))
	require.NoError(t, err)
	require.JSONEq(t, `"1m30s"`, string(out))
}

// TestToolTimeoutsFromYAML verifies the config path end to end: a duration
// string in YAML reaches the tool config.
func TestToolTimeoutsFromYAML(t *testing.T) {
	t.Parallel()

	jsonBytes, err := decodeConfig([]byte("tools:\n  grep:\n    timeout: 45s\n  glob:\n    timeout: 2m\n"))
	require.NoError(t, err)

	cfg, err := loadFromBytes([][]byte{jsonBytes})
	require.NoError(t, err)
	require.Equal(t, 45*time.Second, cfg.Tools.Grep.GetTimeout())
	require.Equal(t, 2*time.Minute, cfg.Tools.Glob.GetTimeout())
}

// TestToolTimeoutDefaults verifies an unset timeout keeps the built-in
// default rather than collapsing to zero.
func TestToolTimeoutDefaults(t *testing.T) {
	t.Parallel()

	var cfg Config
	require.Equal(t, 5*time.Second, cfg.Tools.Grep.GetTimeout())
	require.Equal(t, 30*time.Second, cfg.Tools.Glob.GetTimeout())
}
