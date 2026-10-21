package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoginCmd_Aliases(t *testing.T) {
	t.Parallel()

	require.Equal(t, "auth", loginCmd.Aliases[0])
}

func TestLoginCmd_AcceptsCopilotAliases(t *testing.T) {
	t.Parallel()

	require.Equal(t, copilotAliases, loginCmd.ValidArgs)
	require.NoError(t, loginCmd.Args(loginCmd, nil))
	for _, alias := range copilotAliases {
		require.NoError(t, loginCmd.Args(loginCmd, []string{alias}))
	}
}

func TestLoginCmd_RejectsUnknownPlatformBeforeRun(t *testing.T) {
	t.Parallel()

	err := loginCmd.Args(loginCmd, []string{"foo"})
	require.EqualError(t, err, `unknown platform "foo", valid platforms: copilot, github, github-copilot`)
	require.Error(t, loginCmd.Args(loginCmd, []string{"copilot", "github"}))
}

func TestLoginCmd_ForceFlag(t *testing.T) {
	t.Parallel()

	flag := loginCmd.Flags().Lookup("force")
	require.NotNil(t, flag)
	require.Equal(t, "f", flag.Shorthand)
}
