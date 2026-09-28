package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/env"
)

// TestApplyEnvRestoresDroppedKeys covers a reload after a key left env:
// the process gets back what it held before the config set the key.
func TestApplyEnvRestoresDroppedKeys(t *testing.T) {
	t.Setenv("HARNESS_TEST_KEPT", "original")
	os.Unsetenv("HARNESS_TEST_ADDED")
	t.Cleanup(func() { os.Unsetenv("HARNESS_TEST_ADDED") })
	resolver := NewShellVariableResolver(env.New())

	(&Config{Env: map[string]string{"HARNESS_TEST_KEPT": "from-config", "HARNESS_TEST_ADDED": "x"}}).applyEnv(resolver)
	require.Equal(t, "from-config", os.Getenv("HARNESS_TEST_KEPT"))
	require.Equal(t, "x", os.Getenv("HARNESS_TEST_ADDED"))

	(&Config{Env: map[string]string{}}).applyEnv(resolver)
	require.Equal(t, "original", os.Getenv("HARNESS_TEST_KEPT"), "a dropped key gets its prior value back")
	_, set := os.LookupEnv("HARNESS_TEST_ADDED")
	require.False(t, set, "a key the config introduced is unset again")
}
