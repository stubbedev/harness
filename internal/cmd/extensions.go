package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/extensions"
)

var extensionsCmd = &cobra.Command{
	Use:   "extensions",
	Short: "Work with Lua extensions",
	Long: "Lua extensions run inside Harness and register tools, hook handlers\n" +
		"and commands. See docs/extensions for the API.",
}

var extensionsTypesCmd = &cobra.Command{
	Use:   "types",
	Short: "Print the Lua type definitions for the extension API",
	Long: "Print the lua-language-server definitions for the `harness` table, which\n" +
		"give completion and diagnostics while writing an extension.\n\n" +
		"With --write they are installed next to your extensions, alongside a\n" +
		".luarc.json that points the language server at them.",
	Example: `
# Print the definitions
harness extensions types

# Install them into the global extensions directory
harness extensions types --write

# Install them next to a project's extensions
harness extensions types --write --dir .harness/extensions`,
	RunE: func(cmd *cobra.Command, args []string) error {
		write, _ := cmd.Flags().GetBool("write")
		if !write {
			fmt.Fprint(cmd.OutOrStdout(), extensions.TypeDefinitions)
			return nil
		}

		dir, _ := cmd.Flags().GetString("dir")
		if dir == "" {
			dirs := config.GlobalExtensionsDirs()
			if len(dirs) == 0 {
				return fmt.Errorf("no global extensions directory is configured; pass --dir")
			}
			dir = dirs[0]
		}

		stub, err := extensions.WriteTypeDefinitions(dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", stub)
		return nil
	},
}

func init() {
	extensionsTypesCmd.Flags().Bool("write", false, "Write the definitions next to your extensions instead of printing them")
	extensionsTypesCmd.Flags().String("dir", "", "Directory to write into (default: the global extensions directory)")
	extensionsCmd.AddCommand(extensionsTypesCmd)
}
