package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stubbedev/harness/internal/crash"
)

var crashesCmd = &cobra.Command{
	Use:   "crashes [report]",
	Short: "List captured crash reports",
	Long: `List the panic reports Harness persisted for crashes it recovered from
or exited on, newest first. Reports live in a single global directory and
the newest 50 are kept. Pass a report's file name to print it in full.`,
	Example: `
# List crash reports
harness crashes

# Print one report
harness crashes 20260922-143005.123-tui.log
  `,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			return printCrashReport(args[0])
		}
		return listCrashReports()
	},
}

func listCrashReports() error {
	reports, err := crash.List()
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("No crash reports yet. Reports are saved to %s\n", crash.Dir())
			return nil
		}
		return fmt.Errorf("failed to list crash reports: %v", err)
	}
	if len(reports) == 0 {
		fmt.Printf("No crash reports yet. Reports are saved to %s\n", crash.Dir())
		return nil
	}

	fmt.Printf("Crash reports in %s (newest first):\n\n", crash.Dir())
	for _, report := range reports {
		summary := report.Panic
		if summary == "" {
			summary = "(no panic recorded)"
		}
		fmt.Printf("%s  %-32s  %s\n", report.Time.Local().Format("2006-01-02 15:04:05"), report.Component, summary)
	}
	return nil
}

func printCrashReport(name string) error {
	path := name
	if !strings.ContainsRune(name, os.PathSeparator) {
		path = filepath.Join(crash.Dir(), strings.TrimSuffix(name, ".log")+".log")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read crash report: %v", err)
	}
	_, err = os.Stdout.Write(data)
	return err
}
