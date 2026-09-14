package tools

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/lsp"
)

type DiagnosticsParams struct {
	FilePath string `json:"file_path,omitempty" description:"The path to the file to get diagnostics for (leave empty for project diagnostics)"`
}

const DiagnosticsToolName = "lsp_diagnostics"

// writeDiagnosticsTimeout is the ceiling on how long a write (edit, write,
// rename) waits for a language server to report on what it just changed. It is
// only reached by a server that keeps republishing; one that answers and goes
// quiet is waited out by the settle window instead, and one that says nothing
// at all gives up after the shorter first-change window.
const writeDiagnosticsTimeout = 5 * time.Second

//go:embed diagnostics.md
var diagnosticsDescription string

func NewDiagnosticsTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		DiagnosticsToolName,
		diagnosticsDescription,
		func(ctx context.Context, params DiagnosticsParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			// The one caller that exists to report diagnostics and nothing
			// else, so it is the one that waits for a cold server to come up
			// rather than answering "no problems" because nothing is running.
			if params.FilePath != "" && lspManager != nil {
				lspManager.Start(ctx, params.FilePath)
			}
			notifyLSPs(ctx, lspManager, params.FilePath)
			output := getDiagnostics(params.FilePath, lspManager)
			return fantasy.NewTextResponse(output), nil
		},
	)
}

// openInLSPs makes the LSP servers aware of the file without blocking on any
// part of it: servers that are not up yet are started in the background, and
// the file is opened — a one-way notification — only on clients that are
// already running. Nothing here waits for a diagnostic.
//
// Use this for read-only operations like view. The file content hasn't
// changed, so a server has nothing new to say about it, and a read that waits
// on one pays that cost on every file the model looks at.
func openInLSPs(
	ctx context.Context,
	manager *lsp.Manager,
	filepath string,
) {
	if filepath == "" || manager == nil {
		return
	}

	// Detached from the tool call's context: the call returns long before a
	// cold server finishes starting, and cancelling it then would leave the
	// server half-initialized.
	manager.StartAsync(context.WithoutCancel(ctx), filepath)

	for client := range manager.Clients().Seq() {
		if !client.HandlesFile(filepath) {
			continue
		}
		_ = client.OpenFileOnDemand(ctx, filepath)
	}
}

// notifyLSPs notifies LSP servers that a file has changed and waits for
// updated diagnostics. Use this after edit operations.
// When filepath is empty, refreshes all open files across all LSP clients
// and sends a workspace-level change notification for full re-analysis.
func notifyLSPs(
	ctx context.Context,
	manager *lsp.Manager,
	filepath string,
) {
	if manager == nil {
		return
	}
	if filepath == "" {
		// No specific file — refresh all open files for all clients.
		var wg sync.WaitGroup
		for client := range manager.Clients().Seq() {
			wg.Go(func() {
				client.RefreshOpenFiles(ctx)
				if err := client.NotifyWorkspaceChange(ctx); err != nil {
					slog.WarnContext(ctx, "Failed to notify workspace change", "error", err)
				}
				client.WaitForDiagnostics(ctx, writeDiagnosticsTimeout)
			})
		}
		wg.Wait()
		return
	}

	// Start servers in the background. An edit waits for what a running
	// server has to say about it, but never for a cold server to come up:
	// that is seconds of process spawn and workspace indexing, and the
	// diagnostics it would eventually produce arrive on the next edit anyway.
	manager.StartAsync(context.WithoutCancel(ctx), filepath)

	var wg sync.WaitGroup
	for client := range manager.Clients().Seq() {
		if !client.HandlesFile(filepath) {
			continue
		}
		_ = client.OpenFileOnDemand(ctx, filepath)
		_ = client.NotifyChange(ctx, filepath)
		wg.Go(func() {
			client.WaitForDiagnostics(ctx, writeDiagnosticsTimeout)
		})
	}
	wg.Wait()
}

func getDiagnostics(filePath string, manager *lsp.Manager) string {
	if manager == nil {
		return ""
	}

	var fileDiagnostics []string
	var projectDiagnostics []string

	for lspName, client := range manager.Clients().Seq2() {
		for location, diags := range client.GetDiagnostics() {
			path, err := location.Path()
			if err != nil {
				slog.Error("Failed to convert diagnostic location URI to path", "uri", location, "error", err)
				continue
			}
			isCurrentFile := path == filePath
			for _, diag := range diags {
				formattedDiag := formatDiagnostic(path, diag, lspName)
				if isCurrentFile {
					fileDiagnostics = append(fileDiagnostics, formattedDiag)
				} else {
					projectDiagnostics = append(projectDiagnostics, formattedDiag)
				}
			}
		}
	}

	sortDiagnostics(fileDiagnostics)
	sortDiagnostics(projectDiagnostics)

	var output strings.Builder
	writeDiagnostics(&output, "file_diagnostics", fileDiagnostics)
	writeDiagnostics(&output, "project_diagnostics", projectDiagnostics)

	if len(fileDiagnostics) > 0 || len(projectDiagnostics) > 0 {
		fileErrors := countSeverity(fileDiagnostics, "Error")
		fileWarnings := countSeverity(fileDiagnostics, "Warn")
		projectErrors := countSeverity(projectDiagnostics, "Error")
		projectWarnings := countSeverity(projectDiagnostics, "Warn")
		output.WriteString("\n<diagnostic_summary>\n")
		fmt.Fprintf(&output, "Current file: %d errors, %d warnings\n", fileErrors, fileWarnings)
		fmt.Fprintf(&output, "Project: %d errors, %d warnings\n", projectErrors, projectWarnings)
		output.WriteString("</diagnostic_summary>\n")
	}

	out := output.String()
	slog.Debug("Diagnostics", "output", out)
	return out
}

func writeDiagnostics(output *strings.Builder, tag string, in []string) {
	if len(in) == 0 {
		return
	}
	output.WriteString("\n<" + tag + ">\n")
	if len(in) > 10 {
		output.WriteString(strings.Join(in[:10], "\n"))
		fmt.Fprintf(output, "\n... and %d more diagnostics", len(in)-10)
	} else {
		output.WriteString(strings.Join(in, "\n"))
	}
	output.WriteString("\n</" + tag + ">\n")
}

func sortDiagnostics(in []string) []string {
	sort.Slice(in, func(i, j int) bool {
		iIsError := strings.HasPrefix(in[i], "Error")
		jIsError := strings.HasPrefix(in[j], "Error")
		if iIsError != jIsError {
			return iIsError // Errors come first
		}
		return in[i] < in[j] // Then alphabetically
	})
	return in
}

func formatDiagnostic(pth string, diagnostic protocol.Diagnostic, source string) string {
	severity := "Info"
	switch diagnostic.Severity {
	case protocol.SeverityError:
		severity = "Error"
	case protocol.SeverityWarning:
		severity = "Warn"
	case protocol.SeverityHint:
		severity = "Hint"
	}

	location := fmt.Sprintf("%s:%d:%d", pth, diagnostic.Range.Start.Line+1, diagnostic.Range.Start.Character+1)

	sourceInfo := source
	if diagnostic.Source != "" {
		sourceInfo += " " + diagnostic.Source
	}

	codeInfo := ""
	if diagnostic.Code != nil {
		codeInfo = fmt.Sprintf("[%v]", diagnostic.Code)
	}

	tagsInfo := ""
	if len(diagnostic.Tags) > 0 {
		var tags []string
		for _, tag := range diagnostic.Tags {
			switch tag {
			case protocol.Unnecessary:
				tags = append(tags, "unnecessary")
			case protocol.Deprecated:
				tags = append(tags, "deprecated")
			}
		}
		if len(tags) > 0 {
			tagsInfo = fmt.Sprintf(" (%s)", strings.Join(tags, ", "))
		}
	}

	return fmt.Sprintf("%s: %s [%s]%s%s %s",
		severity,
		location,
		sourceInfo,
		codeInfo,
		tagsInfo,
		diagnostic.Message)
}

func countSeverity(diagnostics []string, severity string) int {
	count := 0
	for _, diag := range diagnostics {
		if strings.HasPrefix(diag, severity) {
			count++
		}
	}
	return count
}
