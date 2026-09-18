package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"

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
Available platforms are: copilot.`,
	Example: `
# Authenticate with GitHub Copilot
harness login copilot

# Force re-authentication even if already logged in
harness login -f copilot
  `,
	ValidArgs: []cobra.Completion{
		"copilot",
		"github",
		"github-copilot",
	},
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, cleanup, err := setupWorkspaceWithProgressBar(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		provider := "copilot"
		if len(args) > 0 {
			provider = args[0]
		}
		force, _ := cmd.Flags().GetBool("force")
		if copilotPlatform(provider) {
			return loginCopilot(ws, force)
		}
		return fmt.Errorf("unknown platform: %s", args[0])
	},
}

func init() {
	loginCmd.Flags().BoolP("force", "f", false, "Force re-authentication even if already logged in")
}

// copilotPlatform reports whether the platform argument names GitHub
// Copilot, in any of its accepted spellings. Single source for the
// login and logout switches and the logged-in provider picker, so the
// alias list cannot drift between them.
func copilotPlatform(provider string) bool {
	switch provider {
	case "copilot", "github", "github-copilot":
		return true
	}
	return false
}

func loginCopilot(ws workspace.Workspace, force bool) error {
	loginCtx := interactiveContext()

	if !force {
		cfg := ws.Config()
		if cfg != nil {
			if pc, ok := cfg.Providers.Get(string(catalog.InferenceProviderCopilot)); ok && pc.OAuthToken != nil {
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

		t, err := copilot.RefreshToken(loginCtx, diskToken)
		if err != nil {
			return fmt.Errorf("unable to refresh token from disk: %w", err)
		}
		token = t
	default:
		fmt.Println("Requesting device code from GitHub...")
		dc, err := copilot.RequestDeviceCode(loginCtx)
		if err != nil {
			return err
		}

		clipboard.WriteText(dc.UserCode)
		fmt.Println()
		fmt.Println("The following code should be on clipboard already:")
		fmt.Println()
		lipgloss.Println(lipgloss.NewStyle().Bold(true).Render(dc.UserCode))
		fmt.Println()
		fmt.Println("Press enter to open this URL and authenticate with GitHub Copilot:")
		fmt.Println()
		lipgloss.Println(lipgloss.NewStyle().Hyperlink(dc.VerificationURI, "id=copilot").Render(dc.VerificationURI))
		fmt.Println()
		waitEnter()
		if err := browser.OpenURL(dc.VerificationURI); err != nil {
			fmt.Println("Could not open the URL. You'll need to manually open the URL in your browser.")
		}

		fmt.Println("Waiting for authorization...")

		t, err := copilot.PollForToken(loginCtx, dc)
		if err == copilot.ErrNotAvailable {
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

	if err := ws.SetProviderAPIKey(config.ScopeGlobal, string(catalog.InferenceProviderCopilot), token); err != nil {
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
