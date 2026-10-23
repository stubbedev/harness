package cmd

import (
	"cmp"
	"fmt"
	"os"
	"slices"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/client"
	"github.com/stubbedev/harness/internal/config"
)

var (
	logoutHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	logoutItemStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	logoutPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
)

var logoutCmd = &cobra.Command{
	Aliases: []string{"signout"},
	Use:     "logout [platform]",
	Short:   "Logout Harness from a platform",
	Long: `Logout Harness from a specified platform, removing stored credentials.
The platform should be provided as an argument.
If no argument is given, a list of logged-in platforms will be shown.
Available platforms are: copilot, opencode-go and opencode.`,
	Example: `
# Sign out from GitHub Copilot
harness logout copilot
  `,
	ValidArgs: loginPlatforms,
	Args:      platformArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, ws, cleanup, err := connectToServer(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		progressEnabled := ws.Config.Options.ProgressEnabled()
		if progressEnabled && supportsProgressBar() {
			_, _ = fmt.Fprintf(os.Stderr, ansi.SetIndeterminateProgressBar)
			defer func() { _, _ = fmt.Fprintf(os.Stderr, ansi.ResetProgressBar) }()
		}

		var provider catalog.InferenceProvider
		if len(args) == 0 {
			provider, err = pickLoggedInProvider(c, ws.ID)
			if err != nil {
				return err
			}
			if provider == "" {
				return nil
			}
		} else {
			provider = loginProviderID(args[0])
		}

		force, _ := cmd.Flags().GetBool("force")
		if !force {
			prompt := logoutPromptStyle.Render(fmt.Sprintf("Are you sure you want to logout %s? (y/N) ", provider))
			if !confirmPrompt(cmd.InOrStdin(), cmd.OutOrStdout(), prompt) {
				fmt.Println(logoutHeaderStyle.Render("Logout cancelled."))
				return nil
			}
		}

		return logout(c, ws.ID, provider)
	},
}

func logout(c *client.Client, wsID string, provider catalog.InferenceProvider) error {
	ctx := interactiveContext()

	if err := cmp.Or(
		c.RemoveConfigField(ctx, wsID, config.ScopeGlobal, config.ProviderFieldPath(provider, "api_key")),
		c.RemoveConfigField(ctx, wsID, config.ScopeGlobal, config.ProviderFieldPath(provider, "oauth")),
	); err != nil {
		return err
	}

	fmt.Println(logoutHeaderStyle.Render(fmt.Sprintf("Successfully logged out of %s.", provider)))
	return nil
}

func pickLoggedInProvider(c *client.Client, wsID string) (catalog.InferenceProvider, error) {
	ctx := interactiveContext()

	cfg, err := c.GetConfig(ctx, wsID)
	if err != nil {
		return "", fmt.Errorf("failed to get config: %w", err)
	}

	type loggedInProvider struct {
		id   catalog.InferenceProvider
		name string
	}

	var loggedIn []loggedInProvider
	for p := range cfg.Providers.Seq() {
		if _, signsIn := config.SignInFlow(p.ID); signsIn && p.OAuthToken != nil {
			loggedIn = append(loggedIn, loggedInProvider{id: p.ID, name: cmp.Or(p.Name, string(p.ID))})
		}
	}
	slices.SortFunc(loggedIn, func(a, b loggedInProvider) int { return cmp.Compare(a.id, b.id) })

	if len(loggedIn) == 0 {
		fmt.Println(logoutPromptStyle.Render("You are not logged in to any platform."))
		return "", nil
	}

	if len(loggedIn) == 1 {
		return loggedIn[0].id, nil
	}

	fmt.Println(logoutHeaderStyle.Render("Logged-in platforms:"))
	for i, p := range loggedIn {
		fmt.Println(logoutItemStyle.Render(fmt.Sprintf("  %d. %s", i+1, p.name)))
	}
	fmt.Print(logoutPromptStyle.Render(fmt.Sprintf("Select a platform to logout (1-%d): ", len(loggedIn))))

	var choice int
	_, err = fmt.Scanln(&choice)
	if err != nil || choice < 1 || choice > len(loggedIn) {
		fmt.Println(logoutHeaderStyle.Render("Logout cancelled."))
		return "", nil //nolint:nilerr // bad input cancels the prompt
	}

	return loggedIn[choice-1].id, nil
}

func init() {
	logoutCmd.Flags().BoolP("force", "f", false, "Skip logout confirmation prompt")
}
