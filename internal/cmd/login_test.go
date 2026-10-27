package cmd

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/oauth"
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

type stubDeviceFlow struct {
	code    *oauth.DeviceCode
	token   *oauth.Token
	request int
	poll    int
}

func (f *stubDeviceFlow) RequestDeviceCode(context.Context) (*oauth.DeviceCode, error) {
	f.request++
	return f.code, nil
}

func (f *stubDeviceFlow) PollForToken(context.Context, *oauth.DeviceCode) (*oauth.Token, error) {
	f.poll++
	return f.token, nil
}

func (f *stubDeviceFlow) Refresh(context.Context, string) (*oauth.Token, error) {
	return nil, errors.New("the test flow never refreshes")
}

// TestDeviceFlowAuthorizesWithoutTerminalInput proves the flow opens
// the verification page and polls on its own. It used to wait for a
// keypress first, so a user who opened the page themselves and approved
// saw the command sit there until they pressed enter.
func TestDeviceFlowAuthorizesWithoutTerminalInput(t *testing.T) {
	flow := &stubDeviceFlow{
		code: &oauth.DeviceCode{
			DeviceCode:      "device-code",
			UserCode:        "ABCD-EFGH",
			VerificationURI: "https://opencode.ai/console/device?user_code=ABCD-EFGH",
			ExpiresIn:       600,
			Interval:        5,
		},
		token: &oauth.Token{AccessToken: "access-token"},
	}

	// stdin stays open and idle: anything reading a key from it hangs.
	stdin := os.Stdin
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stdin = reader
	defer func() { os.Stdin = stdin }()
	defer writer.Close()
	defer reader.Close()

	var (
		copied []string
		opened []string
	)
	type result struct {
		token *oauth.Token
		err   error
	}
	done := make(chan result, 1)
	go func() {
		token, err := deviceFlow(context.Background(), "OpenCode Go", flow,
			func(code string) error { copied = append(copied, code); return nil },
			func(url string) error { opened = append(opened, url); return nil },
		)
		done <- result{token: token, err: err}
	}()

	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.Equal(t, "access-token", r.token.AccessToken)
	case <-time.After(5 * time.Second):
		t.Fatal("the flow waited for terminal input instead of authorizing")
	}

	require.Equal(t, []string{"ABCD-EFGH"}, copied)
	require.Equal(t, []string{flow.code.VerificationURI}, opened)
	require.Equal(t, 1, flow.request)
	require.Equal(t, 1, flow.poll)
}
