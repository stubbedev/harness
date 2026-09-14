package cmd

import (
	"fmt"
	"log/slog"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/spf13/cobra"
	"github.com/stubbedev/harness/internal/config"
)

var updateProvidersCmd = &cobra.Command{
	Use:   "update-providers [path-or-url]",
	Short: "Update the model catalog",
	Long: `Refresh the provider and model catalog stored on this machine.

With no argument the catalog is fetched live from models.dev, with the
OpenRouter provider entry taken from OpenRouter's own model API. A path
or URL is read as either a models.dev api.json document or a plain
provider list. The catalog is shared by every workspace, so one refresh
covers them all; sessions already running pick it up on their next
start.`,
	Example: `
# Refresh the catalog from models.dev (default)
harness update-providers

# Update the catalog from a custom URL
harness update-providers https://example.com/api.json

# Update the catalog from a local file
harness update-providers /path/to/providers.json
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// NOTE: We want to skip logging output to stdout here.
		slog.SetDefault(slog.New(slog.DiscardHandler))

		var pathOrURL string
		if len(args) > 0 {
			pathOrURL = args[0]
		}

		if err := config.UpdateProviders(pathOrURL); err != nil {
			return err
		}

		// NOTE: This style is more-or-less copied from Fang's
		// error message, adapted for success.
		headerStyle := lipgloss.NewStyle().
			Foreground(charmtone.Butter).
			Background(charmtone.Guac).
			Bold(true).
			Padding(0, 1).
			Margin(1).
			MarginLeft(2).
			SetString("SUCCESS")
		textStyle := lipgloss.NewStyle().
			MarginLeft(2).
			SetString("Provider catalog updated successfully.")

		fmt.Printf("%s\n%s\n\n", headerStyle.Render(), textStyle.Render())
		return nil
	},
}

func init() {
	updateProvidersCmd.Flags().String("source", "", "Deprecated and ignored")
	_ = updateProvidersCmd.Flags().MarkHidden("source")
}
