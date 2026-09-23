package workspace

import (
	"log/slog"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/lsp"
)

// countDiagnostics adds each diagnostic to its severity bucket.
func countDiagnostics(counts *lsp.DiagnosticCounts, diags []protocol.Diagnostic) {
	for _, d := range diags {
		switch d.Severity {
		case protocol.SeverityError:
			counts.Error++
		case protocol.SeverityWarning:
			counts.Warning++
		case protocol.SeverityInformation:
			counts.Information++
		case protocol.SeverityHint:
			counts.Hint++
		}
	}
}

// foldFileDiagnostics adds one server's diagnostics to per-file counts,
// keyed by filesystem path.
func foldFileDiagnostics(counts map[string]lsp.DiagnosticCounts, diags map[protocol.DocumentURI][]protocol.Diagnostic) {
	for uri, fileDiags := range diags {
		path, err := uri.Path()
		if err != nil {
			slog.Error("Failed to convert diagnostic URI to path", "uri", uri, "error", err)
			continue
		}
		file := counts[path]
		countDiagnostics(&file, fileDiags)
		counts[path] = file
	}
}
