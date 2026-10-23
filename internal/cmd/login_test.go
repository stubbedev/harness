package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

func TestLoginCmd_Aliases(t *testing.T) {
	t.Parallel()

	require.Equal(t, "auth", loginCmd.Aliases[0])
}

func TestLoginCmd_AcceptsEveryPlatform(t *testing.T) {
	t.Parallel()

	require.Equal(t, loginPlatforms, loginCmd.ValidArgs)
	require.NoError(t, loginCmd.Args(loginCmd, nil))
	for _, alias := range loginPlatforms {
		require.NoError(t, loginCmd.Args(loginCmd, []string{alias}))
	}
}

func TestLoginCmd_RejectsUnknownPlatformBeforeRun(t *testing.T) {
	t.Parallel()

	err := loginCmd.Args(loginCmd, []string{"foo"})
	require.EqualError(t, err, `unknown platform "foo", valid platforms: copilot, github, github-copilot, opencode-go, opencode`)
	require.Error(t, loginCmd.Args(loginCmd, []string{"copilot", "github"}))
}

func TestLoginCmd_ForceFlag(t *testing.T) {
	t.Parallel()

	flag := loginCmd.Flags().Lookup("force")
	require.NotNil(t, flag)
	require.Equal(t, "f", flag.Shorthand)
}

func TestLoginProviderIDMapsPlatforms(t *testing.T) {
	t.Parallel()

	for platform, want := range map[string]catalog.InferenceProvider{
		"copilot":        catalog.InferenceProviderCopilot,
		"github":         catalog.InferenceProviderCopilot,
		"github-copilot": catalog.InferenceProviderCopilot,
		"opencode-go":    catalog.InferenceProviderOpenCodeGo,
		"opencode":       catalog.InferenceProviderOpenCodeZen,
	} {
		require.Equal(t, want, loginProviderID(platform), platform)
		_, signsIn := config.SignInFlow(want)
		require.True(t, signsIn, "every login platform has a sign-in: %s", platform)
	}
}
