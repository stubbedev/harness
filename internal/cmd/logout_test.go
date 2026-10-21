package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogoutCmd_Aliases(t *testing.T) {
	t.Parallel()

	require.Equal(t, "signout", logoutCmd.Aliases[0])
}

func TestLogoutCmd_HasForceFlag(t *testing.T) {
	t.Parallel()

	flag := logoutCmd.Flags().Lookup("force")
	require.NotNil(t, flag)
	require.Equal(t, "f", flag.Shorthand)
	require.Equal(t, "false", flag.DefValue)
}

func TestLogoutCmd_ValidArgs(t *testing.T) {
	t.Parallel()

	validPlatforms := map[string]bool{}
	for _, p := range logoutCmd.ValidArgs {
		validPlatforms[p] = true
	}
	require.True(t, validPlatforms["copilot"])
	require.True(t, validPlatforms["github"])
	require.True(t, validPlatforms["github-copilot"])
}

func TestLogoutCmd_SharesCopilotAliases(t *testing.T) {
	t.Parallel()

	require.Equal(t, copilotAliases, logoutCmd.ValidArgs)
	require.NoError(t, logoutCmd.Args(logoutCmd, []string{"github"}))
}

func TestLogoutCmd_RejectsUnknownPlatformBeforePrompt(t *testing.T) {
	t.Parallel()

	err := logoutCmd.Args(logoutCmd, []string{"foo"})
	require.EqualError(t, err, `unknown platform "foo", valid platforms: copilot, github, github-copilot`)
}

func TestLogoutContext_CreatesValidContext(t *testing.T) {
	ctx := interactiveContext()
	require.NotNil(t, ctx)
	require.NoError(t, ctx.Err())
}
