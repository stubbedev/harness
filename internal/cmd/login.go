package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/clipboard"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/oauth"
	"github.com/stubbedev/harness/internal/oauth/copilot"
	"github.com/stubbedev/harness/internal/workspace"
)

var loginCmd = &cobra.Command{
	Aliases: []string{"auth"},
	Use:     "login [platform]",
	Short:   "Login Harness to a platform",
	Long: `Login Harness to a specified platform.
The platform should be provided as an argument.
Available platforms are: copilot, opencode-go and opencode.`,
	Example: `
# Authenticate with GitHub Copilot
harness login copilot

# Sign in to OpenCode Go with your OpenCode Console account
harness login opencode-go

# Force re-authentication even if already logged in
harness login -f copilot
  `,
	ValidArgs: loginPlatforms,
	Args:      platformArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, cleanup, err := setupWorkspaceWithProgressBar(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		force, _ := cmd.Flags().GetBool("force")
		providerID := catalog.InferenceProviderCopilot
		if len(args) > 0 {
			providerID = loginProviderID(args[0])
		}
		if providerID == catalog.InferenceProviderCopilot {
			return loginCopilot(ws, force)
		}
		return loginWithDeviceFlow(ws, providerID, force)
	},
}

func init() {
	loginCmd.Flags().BoolP("force", "f", false, "Force re-authentication even if already logged in")
}

var copilotAliases = []cobra.Completion{"copilot", "github", "github-copilot"}

var loginPlatforms = append(slices.Clone(copilotAliases),
	cobra.Completion(catalog.InferenceProviderOpenCodeGo),
	cobra.Completion(catalog.InferenceProviderOpenCodeZen),
)

var platformArgs = cobra.MatchAll(cobra.MaximumNArgs(1), validPlatformArg)

func validPlatformArg(cmd *cobra.Command, args []string) error {
	for _, arg := range args {
		if !slices.Contains(cmd.ValidArgs, arg) {
			return fmt.Errorf("unknown platform %q, valid platforms: %s", arg, strings.Join(cmd.ValidArgs, ", "))
		}
	}
	return nil
}

func loginCopilot(ws workspace.Workspace, force bool) error {
	loginCtx := interactiveContext()

	if !force {
		cfg := ws.Config()
		if cfg != nil {
			if pc, ok := cfg.Providers.Get(catalog.InferenceProviderCopilot); ok && pc.OAuthToken != nil {
				fmt.Println("You are already logged in to GitHub Copilot.")
				fmt.Println("Use --force to re-authenticate.")
				return nil
			}
		}
	}

	diskToken, hasDiskToken := copilot.RefreshTokenFromDisk()
	var token *oauth.Token

	switch {
	case hasDiskToken:
		fmt.Println("Found existing GitHub Copilot token on disk. Using it to authenticate...")

		t, err := copilot.DeviceFlow{}.Refresh(loginCtx, diskToken)
		if err != nil {
			return fmt.Errorf("unable to refresh token from disk: %w", err)
		}
		token = t
	default:
		t, err := runDeviceFlow(loginCtx, "GitHub Copilot", copilot.DeviceFlow{})
		if errors.Is(err, copilot.ErrNotAvailable) {
			fmt.Println()
			fmt.Println("GitHub Copilot is unavailable for this account. To signup, go to the following page:")
			fmt.Println()
			lipgloss.Println(lipgloss.NewStyle().Hyperlink(copilot.SignupURL, "id=copilot-signup").Render(copilot.SignupURL))
			fmt.Println()
			fmt.Println("You may be able to request free access if eligible. For more information, see:")
			fmt.Println()
			lipgloss.Println(lipgloss.NewStyle().Hyperlink(copilot.FreeURL, "id=copilot-free").Render(copilot.FreeURL))
		}
		if err != nil {
			return err
		}
		token = t
	}

	if err := ws.SetProviderAPIKey(config.ScopeGlobal, catalog.InferenceProviderCopilot, token); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("You're now authenticated with GitHub Copilot!")
	return nil
}

// interactiveContext returns a context cancelled on SIGINT/SIGKILL,
// exiting the process when it fires. The forced exit is deliberate:
// these interactive OAuth flows cannot be resumed, so a second signal
// must not unwind through cobra's error handling. Shared by login and
// logout.
func interactiveContext() context.Context {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	go func() {
		<-ctx.Done()
		cancel()
		os.Exit(1)
	}()
	return ctx
}

func waitEnter() {
	_, _ = fmt.Scanln()
}

func loginWithDeviceFlow(ws workspace.Workspace, providerID catalog.InferenceProvider, force bool) error {
	flow, ok := config.SignInFlow(providerID)
	if !ok {
		return fmt.Errorf("provider %s has no sign-in", providerID)
	}
	name := string(providerID)
	providers, _ := config.Providers(ws.Config())
	if known := config.KnownProviderByID(providers, providerID); known != nil {
		name = known.Name
	}
	if !force {
		if pc, ok := ws.Config().Providers.Get(providerID); ok && pc.OAuthToken != nil {
			fmt.Printf("You are already logged in to %s.\n", name)
			fmt.Println("Use --force to re-authenticate.")
			return nil
		}
	}
	token, err := runDeviceFlow(interactiveContext(), name, flow)
	if err != nil {
		return err
	}
	if err := ws.SetProviderAPIKey(config.ScopeGlobal, providerID, token); err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("You're now authenticated with %s!\n", name)
	return nil
}

// runDeviceFlow walks the user through a device authorization grant on
// the terminal: the code goes to the clipboard, enter opens the
// verification page, and the token is returned once the user approves.
func runDeviceFlow(ctx context.Context, name string, flow oauth.DeviceFlow) (*oauth.Token, error) {
	fmt.Printf("Requesting a device code for %s...\n", name)
	dc, err := flow.RequestDeviceCode(ctx)
	if err != nil {
		return nil, err
	}

	clipboard.WriteText(dc.UserCode)
	fmt.Println()
	fmt.Println("The following code should be on clipboard already:")
	fmt.Println()
	lipgloss.Println(lipgloss.NewStyle().Bold(true).Render(dc.UserCode))
	fmt.Println()
	fmt.Printf("Press enter to open this URL and authenticate with %s:\n", name)
	fmt.Println()
	lipgloss.Println(lipgloss.NewStyle().Hyperlink(dc.VerificationURI, "id=device-login").Render(dc.VerificationURI))
	fmt.Println()
	waitEnter()
	if err := browser.OpenURL(dc.VerificationURI); err != nil {
		fmt.Println("Could not open the URL. You'll need to manually open the URL in your browser.")
	}

	fmt.Println("Waiting for authorization...")
	return flow.PollForToken(ctx, dc)
}

// loginProviderID maps a login platform argument to its provider.
func loginProviderID(platform string) catalog.InferenceProvider {
	if slices.Contains(copilotAliases, platform) {
		return catalog.InferenceProviderCopilot
	}
	return catalog.InferenceProvider(platform)
}
