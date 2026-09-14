package tools

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/lsp"
)

type DiagnosticsParams struct {
	FilePath string `json:"file_path,omitempty" description:"The path to the file to get diagnostics for (leave empty for project diagnostics)"`
}

const DiagnosticsToolName = "lsp_diagnostics"

// settleGrace is how long a report waits for a language server that is still
// answering for a change made moments ago. It is deliberately short: the point
// of reporting asynchronously is that an edit never pays for the server's
// analysis, and a server slower than this is reported by the next call rather
// than held for here.
const settleGrace = 250 * time.Millisecond

// maxReportedDiagnostics caps each section of a report. A project that is
// badly broken has hundreds of diagnostics, and spending the context window on
// all of them buys nothing the count does not already say.
const maxReportedDiagnostics = 10

//go:embed diagnostics.md
var diagnosticsDescription string

func NewDiagnosticsTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		DiagnosticsToolName,
		diagnosticsDescription,
		func(ctx context.Context, params DiagnosticsParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			// The one caller that exists to report diagnostics and nothing
			// else, so it is the one that waits: for a cold server to come up
			// rather than answering "no problems" because nothing is running,
			// and for the servers to finish answering rather than reporting
			// what they happened to have said so far.
			if params.FilePath != "" && lspManager != nil {
				lspManager.Start(ctx, params.FilePath)
				lspManager.NotifyChangeAsync(ctx, params.FilePath)
			} else {
				lspManager.NotifyWorkspaceChangeAsync(ctx)
			}
			lspManager.AwaitSettled(ctx, lsp.SettleTimeout)

			output := fullDiagnosticsReport(ctx, lspManager, params.FilePath)
			if output == "" {
				output = "No diagnostics reported."
			}
			return fantasy.NewTextResponse(output), nil
		},
	)
}

// DiagnosticsSweep reports whatever the language servers have worked out since
// the last report, for the whole project. Writes hand their file to the
// servers and return without waiting, so this is what closes the loop: run it
// before each model step and the analysis of an edit lands as soon as it is
// ready, without any edit having waited for it. It returns "" when there is
// nothing new to say, which is the usual case.
func DiagnosticsSweep(ctx context.Context, manager *lsp.Manager) string {
	return reportDiagnostics(ctx, manager)
}

// ForgetReportedDiagnostics drops the record of what a session has been shown,
// so the next report describes everything again. Call it when the transcript
// those reports were written into is gone — after a summarization — since
// otherwise a standing error stays suppressed as "already reported" against a
// context that no longer contains it.
func ForgetReportedDiagnostics(manager *lsp.Manager, sessionID string) {
	if manager == nil {
		return
	}
	manager.Ledger().Forget(sessionID)
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

// diagnosticLines collects every diagnostic every client currently holds,
// keyed by a fingerprint that identifies the problem rather than its position.
// Line and column are left out of the key on purpose: inserting a line above
// an existing warning moves every diagnostic below it, and keying on position
// would report the whole file as newly broken. The value is the formatted line
// including the current position, so what gets printed is still accurate.
func diagnosticLines(manager *lsp.Manager) (lines map[string]string, paths map[string]string) {
	lines = make(map[string]string)
	paths = make(map[string]string)
	if manager == nil {
		return lines, paths
	}
	for lspName, client := range manager.Clients().Seq2() {
		for location, diags := range client.GetDiagnostics() {
			path, err := location.Path()
			if err != nil {
				slog.Error("Failed to convert diagnostic location URI to path", "uri", location, "error", err)
				continue
			}
			for _, diag := range diags {
				key := fingerprint(path, diag, lspName)
				lines[key] = formatDiagnostic(path, diag, lspName)
				paths[key] = path
			}
		}
	}
	return lines, paths
}

// reportDiagnostics describes what the language servers have learned since the
// last report for this session, and nothing else. A problem the model has
// already been told about is not repeated; one that has gone away is named
// once, so the model can see that the fix landed.
//
// It never waits for a server beyond [settleGrace]: anything still being
// worked out shows up in the next report.
func reportDiagnostics(ctx context.Context, manager *lsp.Manager, focus ...string) string {
	if manager == nil {
		return ""
	}
	manager.AwaitSettled(ctx, settleGrace)
	if manager.Settling() {
		// A server is still answering. Reporting now would describe a file
		// mid-republish — problems it is about to restate read as resolved,
		// and then arrive again as new. The next report gets it right.
		return ""
	}

	lines, paths := diagnosticLines(manager)
	added, resolved := manager.Ledger().Diff(GetSessionFromContext(ctx), lines)
	if len(added) == 0 && len(resolved) == 0 {
		return ""
	}

	var output strings.Builder
	if len(focus) == 0 {
		// Nothing in hand to measure against — a sweep rather than a report
		// on a file the caller just wrote — so the file/project split has
		// nothing to say and one section reads better than two.
		writeDiagnosticSection(&output, "new_diagnostics", sortDiagnostics(added))
	} else {
		inFocus, elsewhere := splitByFocus(added, lines, paths, focus)
		writeDiagnosticSection(&output, "new_file_diagnostics", inFocus)
		writeDiagnosticSection(&output, "new_project_diagnostics", elsewhere)
	}
	writeDiagnosticSection(&output, "resolved_diagnostics", resolved)
	writeSummary(&output, lines, paths, focus)
	out := output.String()
	slog.Debug("Diagnostics", "output", out)
	return out
}

// fullDiagnosticsReport lists everything the servers currently hold, not just
// what changed, and records the whole list as reported so the next incremental
// report is measured against it.
func fullDiagnosticsReport(ctx context.Context, manager *lsp.Manager, focus ...string) string {
	if manager == nil {
		return ""
	}
	lines, paths := diagnosticLines(manager)
	manager.Ledger().Record(GetSessionFromContext(ctx), lines)

	all := make([]string, 0, len(lines))
	for _, line := range lines {
		all = append(all, line)
	}
	inFocus, elsewhere := splitByFocus(all, lines, paths, focus)

	var output strings.Builder
	writeDiagnosticSection(&output, "file_diagnostics", inFocus)
	writeDiagnosticSection(&output, "project_diagnostics", elsewhere)
	writeSummary(&output, lines, paths, focus)
	return output.String()
}

// splitByFocus divides formatted lines into the files the caller was working
// on and everything else, so a report leads with the consequences of the
// change that triggered it.
func splitByFocus(
	report []string,
	lines map[string]string,
	paths map[string]string,
	focus []string,
) (inFocus, elsewhere []string) {
	byLine := make(map[string]string, len(lines))
	for key, line := range lines {
		byLine[line] = paths[key]
	}
	for _, line := range report {
		if slices.Contains(focus, byLine[line]) {
			inFocus = append(inFocus, line)
			continue
		}
		elsewhere = append(elsewhere, line)
	}
	return sortDiagnostics(inFocus), sortDiagnostics(elsewhere)
}

// writeSummary states the current totals, which survive the deduplication: a
// model that is told nothing new appeared still needs to know whether the file
// it just wrote is clean.
func writeSummary(output *strings.Builder, lines, paths map[string]string, focus []string) {
	if len(lines) == 0 {
		return
	}
	var fileErrors, fileWarnings, projectErrors, projectWarnings int
	for key, line := range lines {
		inFocus := slices.Contains(focus, paths[key])
		switch {
		case strings.HasPrefix(line, "Error") && inFocus:
			fileErrors++
		case strings.HasPrefix(line, "Error"):
			projectErrors++
		case strings.HasPrefix(line, "Warn") && inFocus:
			fileWarnings++
		case strings.HasPrefix(line, "Warn"):
			projectWarnings++
		}
	}
	output.WriteString("\n<diagnostic_summary>\n")
	fmt.Fprintf(output, "Current file: %d errors, %d warnings\n", fileErrors, fileWarnings)
	fmt.Fprintf(output, "Project: %d errors, %d warnings\n", projectErrors, projectWarnings)
	output.WriteString("</diagnostic_summary>\n")
}

func writeDiagnosticSection(output *strings.Builder, tag string, in []string) {
	if len(in) == 0 {
		return
	}
	fmt.Fprintf(output, "\n<%s>\n", tag)
	if len(in) > maxReportedDiagnostics {
		output.WriteString(strings.Join(in[:maxReportedDiagnostics], "\n"))
		fmt.Fprintf(output, "\n... and %d more", len(in)-maxReportedDiagnostics)
	} else {
		output.WriteString(strings.Join(in, "\n"))
	}
	fmt.Fprintf(output, "\n</%s>\n", tag)
}

func sortDiagnostics(in []string) []string {
	slices.SortFunc(in, func(a, b string) int {
		aIsError := strings.HasPrefix(a, "Error")
		bIsError := strings.HasPrefix(b, "Error")
		if aIsError != bIsError {
			if aIsError {
				return -1 // Errors come first
			}
			return 1
		}
		return strings.Compare(a, b) // Then alphabetically
	})
	return in
}

// fingerprint identifies a problem independently of where it currently sits in
// the file. See [diagnosticLines] for why the position is excluded.
func fingerprint(pth string, diagnostic protocol.Diagnostic, source string) string {
	return fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%v\x00%s",
		pth,
		diagnostic.Severity,
		source,
		diagnostic.Source,
		diagnostic.Code,
		diagnostic.Message,
	)
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
