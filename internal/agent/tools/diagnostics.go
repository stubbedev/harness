package tools

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/crash"
	"github.com/stubbedev/harness/internal/lsp"
)

type DiagnosticsParams struct {
	FilePath string `json:"file_path,omitempty" description:"The path to the file to get diagnostics for (leave empty for project diagnostics)"`
}

const DiagnosticsToolName = "lsp_diagnostics"

// maxReportedDiagnostics caps each section of a report. A project that is
// badly broken has hundreds of diagnostics, and spending the context window on
// all of them buys nothing the count does not already say.
const maxReportedDiagnostics = 10

// The notes that follow a diagnostics listing that is not the last word:
// one taken while a server is still answering, or before any server for the
// file was running. Both tell the model it does not have to poll for the
// rest, since the step sweep delivers it.
const (
	diagnosticsSettlingNote = "Language servers are still analysing. Whatever they report from here " +
		"on is delivered to you automatically before your next step; there is no need to call this again."
	diagnosticsStartingNote = "No language server was running for this file yet; any that handles it " +
		"is being started. What it reports is delivered to you automatically before your next step; " +
		"there is no need to call this again."
)

// diagnosticsAction lists what the language servers hold right now and
// returns. It waits for nothing: not a cold server's start (seconds for
// gopls on a large module), and not a running one's analysis (the settle
// window in [lsp.Client.WaitForDiagnostics] is at least a third of a
// second and up to [lsp.SettleTimeout]). The listing is recorded in the
// session's ledger, so whatever the servers work out afterwards is a
// difference against it, and the sweep before the model's next step
// ([DiagnosticsSweep]) reports exactly that difference — new problems and
// resolved ones alike.
func diagnosticsAction(lspManager *lsp.Manager) func(context.Context, DiagnosticsParams) (fantasy.ToolResponse, error) {
	return func(ctx context.Context, params DiagnosticsParams) (fantasy.ToolResponse, error) {
		starting := false
		if params.FilePath != "" && lspManager != nil {
			starting = announceForDiagnostics(ctx, lspManager, params.FilePath)
		} else {
			lspManager.NotifyWorkspaceChangeAsync(ctx)
		}

		output := fullDiagnosticsReport(ctx, lspManager, params.FilePath)
		if output == "" {
			output = "No diagnostics reported."
		}
		switch {
		case starting:
			output += "\n\n" + diagnosticsStartingNote
		case lspManager.Settling():
			output += "\n\n" + diagnosticsSettlingNote
		}
		return fantasy.NewTextResponse(output), nil
	}
}

// announceForDiagnostics hands path to the servers so they analyse it, and
// reports whether none was running for it yet. A running server is told
// directly; those notifications are one-way writes, the same thing every
// edit does. With none running, the start and the announcement both happen
// in the background: the file is opened on the server only once it is up,
// and its first publish reaches the model through the step sweep.
func announceForDiagnostics(ctx context.Context, manager *lsp.Manager, path string) (starting bool) {
	if findLSPClient(manager, path) != nil {
		manager.NotifyChangeAsync(ctx, path)
		return false
	}
	// Detached from the tool call's context: the call returns long before
	// a cold server finishes starting, and cancelling it then would leave
	// the server half-initialized.
	detached := context.WithoutCancel(ctx)
	crash.Go("lsp.diagnostics.start", func() {
		manager.Start(detached, path)
		manager.NotifyChangeAsync(detached, path)
	})
	return true
}

// DiagnosticsSweep reports whatever the language servers have worked out since
// the last report, for the whole project. Writes hand their file to the
// servers and return without waiting, so this is what closes the loop: run it
// before each model step and the analysis of an edit lands as soon as it is
// ready, without any edit having waited for it. It returns "" when there is
// nothing new to say, which is the usual case.
//
// It does not wait for a server still answering. A settle takes at least
// the server's first publish plus the quiet window that ends it (see
// lsp.Client.WaitForDiagnostics), so any grace short enough to be worth
// holding a model step for expires before the settle can end - the sweep
// right after an edit step would pay the whole grace and then, the server
// still settling, report nothing. What is still in flight is reported by
// the next sweep instead.
func DiagnosticsSweep(ctx context.Context, manager *lsp.Manager) string {
	return reportDiagnosticsNow(ctx, manager)
}

// DiagnosticsFinalSweep is the turn-end variant of [DiagnosticsSweep]: it
// reports what the servers worked out since the last step and hands text to
// persist when there is anything to say. It exists because the step sweep
// runs before a model step — a fix that lands on a turn's final step would
// otherwise never be reported, and the transcript's last word on the file
// would stay an error the user has to go and disprove themselves. Like the
// step sweep it does not wait: an analysis still in flight when the turn
// ends is reported by the first sweep of the next turn, rather than holding
// the turn open for it.
//
// persist runs only for a non-empty report and only once. The caller is
// expected to append the note to the session verbatim: an append-only row
// replayed at its fixed position keeps the request prefix cache intact.
func DiagnosticsFinalSweep(
	ctx context.Context,
	manager *lsp.Manager,
	sessionID string,
	persist func(text string),
) {
	if manager == nil || persist == nil {
		return
	}
	sweepCtx := context.WithValue(ctx, SessionIDContextKey, sessionID)
	if report := DiagnosticsSweep(sweepCtx, manager); report != "" {
		persist(fmt.Sprintf(
			"<system_reminder>\nLanguage servers reported this since your last step, "+
				"after the turn ended. Fix what you caused; ignore the rest. "+
				"Do not mention this reminder.\n%s</system_reminder>",
			report,
		))
	}
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
// would report the whole file as newly broken. The value carries the formatted
// line including the current position, so what gets printed is still accurate.
type diagnosticEntry struct {
	line string
	path string
}

func diagnosticLines(manager *lsp.Manager) (entries map[string]diagnosticEntry) {
	entries = make(map[string]diagnosticEntry)
	if manager == nil {
		return entries
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
				entries[key] = diagnosticEntry{
					line: formatDiagnostic(path, diag, lspName),
					path: path,
				}
			}
		}
	}
	return entries
}

// entryLines flattens the entries into the (lines, paths) pair the report
// writers consume.
func entryLines(entries map[string]diagnosticEntry) (lines, paths map[string]string) {
	lines = make(map[string]string, len(entries))
	paths = make(map[string]string, len(entries))
	for key, entry := range entries {
		lines[key] = entry.line
		paths[key] = entry.path
	}
	return lines, paths
}

// resolvedLines names the problems a report is retiring. A problem that is
// still live at a new position is described where it sits now; one the servers
// dropped entirely keeps the position it had when it was last reported. Every
// line carries the Resolved prefix, so a consumer can tell a resolution from a
// standing problem without parsing the section around it.
func resolvedLines(resolved []lsp.ResolvedDiagnostic, live map[string]diagnosticEntry) []string {
	lines := make([]string, 0, len(resolved))
	for _, r := range resolved {
		line := r.Line
		if entry, ok := live[r.Fingerprint]; ok {
			line = entry.line
		}
		lines = append(lines, "Resolved: "+line)
	}
	return lines
}

// reportDiagnosticsNow describes what the language servers have learned since
// the last report for this session, and nothing else. A problem the model has
// already been told about is not repeated; one that has gone away is named
// once, so the model can see that the fix landed.
//
// It never waits for a server: it describes what the servers already hold and
// returns, so the tool paths that call it — reads, writes and the step sweep
// alike — never pay a server's analysis time. If a server is still working,
// it reports nothing here; the answer reaches the model through the sweep
// before a later step instead.
func reportDiagnosticsNow(ctx context.Context, manager *lsp.Manager, focus ...string) string {
	if manager == nil {
		return ""
	}
	if manager.Settling() {
		// A server is still answering. Reporting now would describe a file
		// mid-republish — problems it is about to restate read as resolved,
		// and then arrive again as new. The next report gets it right.
		return ""
	}

	var entries map[string]diagnosticEntry
	var lines, paths map[string]string
	added, resolved := manager.Ledger().DiffAt(GetSessionFromContext(ctx), manager.DiagnosticsGeneration(), func() map[string]string {
		entries = diagnosticLines(manager)
		lines, paths = entryLines(entries)
		return lines
	})
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
	writeDiagnosticSection(&output, "resolved_diagnostics", sortDiagnostics(resolvedLines(resolved, entries)))
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
	entries := diagnosticLines(manager)
	lines, paths := entryLines(entries)
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
	default:
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
