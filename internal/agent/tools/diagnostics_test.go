package tools

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

func diag(line uint32, severity protocol.DiagnosticSeverity, message string) protocol.Diagnostic {
	return protocol.Diagnostic{
		Severity: severity,
		Message:  message,
		Range: protocol.Range{
			Start: protocol.Position{Line: line, Character: 0},
		},
	}
}

// The fingerprint is what decides whether a diagnostic is news. Moving down
// the file is not news: an edit near the top shifts everything below it, and
// re-reporting the lot would undo the point of deduplicating at all.
func TestFingerprintIgnoresPosition(t *testing.T) {
	t.Parallel()
	a := fingerprint("/x/f.go", diag(10, protocol.SeverityError, "boom"), "gopls")
	b := fingerprint("/x/f.go", diag(99, protocol.SeverityError, "boom"), "gopls")
	if a != b {
		t.Fatalf("same problem at a different line fingerprinted differently:\n%q\n%q", a, b)
	}
}

func TestFingerprintSeparatesDifferentProblems(t *testing.T) {
	t.Parallel()
	base := fingerprint("/x/f.go", diag(10, protocol.SeverityError, "boom"), "gopls")
	for name, other := range map[string]string{
		"message":  fingerprint("/x/f.go", diag(10, protocol.SeverityError, "bang"), "gopls"),
		"severity": fingerprint("/x/f.go", diag(10, protocol.SeverityWarning, "boom"), "gopls"),
		"path":     fingerprint("/x/g.go", diag(10, protocol.SeverityError, "boom"), "gopls"),
		"server":   fingerprint("/x/f.go", diag(10, protocol.SeverityError, "boom"), "golangci"),
	} {
		if other == base {
			t.Errorf("%s did not change the fingerprint", name)
		}
	}
}

func TestSplitByFocusSeparatesTheFileInHand(t *testing.T) {
	t.Parallel()
	lines := map[string]string{
		"k1": "Error: /x/f.go:1:1 [gopls] boom",
		"k2": "Warn: /x/g.go:1:1 [gopls] meh",
	}
	paths := map[string]string{"k1": "/x/f.go", "k2": "/x/g.go"}

	inFocus, elsewhere := splitByFocus([]string{lines["k1"], lines["k2"]}, lines, paths, []string{"/x/f.go"})
	if len(inFocus) != 1 || inFocus[0] != lines["k1"] {
		t.Fatalf("inFocus = %v", inFocus)
	}
	if len(elsewhere) != 1 || elsewhere[0] != lines["k2"] {
		t.Fatalf("elsewhere = %v", elsewhere)
	}
}

// A rename touches many files and all of them are the file in hand.
func TestSplitByFocusAcceptsSeveralFocusPaths(t *testing.T) {
	t.Parallel()
	lines := map[string]string{
		"k1": "Error: /x/f.go:1:1 [gopls] boom",
		"k2": "Error: /x/g.go:1:1 [gopls] bang",
		"k3": "Warn: /x/h.go:1:1 [gopls] meh",
	}
	paths := map[string]string{"k1": "/x/f.go", "k2": "/x/g.go", "k3": "/x/h.go"}

	inFocus, elsewhere := splitByFocus(
		[]string{lines["k1"], lines["k2"], lines["k3"]},
		lines, paths,
		[]string{"/x/f.go", "/x/g.go"},
	)
	if len(inFocus) != 2 {
		t.Fatalf("inFocus = %v", inFocus)
	}
	if len(elsewhere) != 1 {
		t.Fatalf("elsewhere = %v", elsewhere)
	}
}

func TestSortDiagnosticsPutsErrorsFirst(t *testing.T) {
	t.Parallel()
	got := sortDiagnostics([]string{"Warn: b", "Hint: a", "Error: z", "Error: a"})
	want := []string{"Error: a", "Error: z", "Hint: a", "Warn: b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted = %v, want %v", got, want)
		}
	}
}

func TestWriteSummaryCountsFocusAndProjectApart(t *testing.T) {
	t.Parallel()
	lines := map[string]string{
		"k1": "Error: /x/f.go:1:1 boom",
		"k2": "Warn: /x/f.go:2:1 meh",
		"k3": "Error: /x/g.go:1:1 bang",
		"k4": "Hint: /x/g.go:2:1 fyi",
	}
	paths := map[string]string{"k1": "/x/f.go", "k2": "/x/f.go", "k3": "/x/g.go", "k4": "/x/g.go"}

	var b strings.Builder
	writeSummary(&b, lines, paths, []string{"/x/f.go"})
	out := b.String()

	if !strings.Contains(out, "Current file: 1 errors, 1 warnings") {
		t.Errorf("file counts wrong:\n%s", out)
	}
	if !strings.Contains(out, "Project: 1 errors, 0 warnings") {
		t.Errorf("project counts wrong:\n%s", out)
	}
}

func TestWriteSummarySaysNothingWithoutDiagnostics(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	writeSummary(&b, nil, nil, []string{"/x/f.go"})
	if b.String() != "" {
		t.Fatalf("summary of nothing = %q", b.String())
	}
}

func TestWriteDiagnosticSectionCapsTheList(t *testing.T) {
	t.Parallel()
	in := make([]string, maxReportedDiagnostics+5)
	for i := range in {
		in[i] = "Error: boom"
	}

	var b strings.Builder
	writeDiagnosticSection(&b, "new_diagnostics", in)
	out := b.String()

	if strings.Count(out, "Error: boom") != maxReportedDiagnostics {
		t.Errorf("listed %d lines, want %d:\n%s", strings.Count(out, "Error: boom"), maxReportedDiagnostics, out)
	}
	if !strings.Contains(out, "... and 5 more") {
		t.Errorf("missing overflow count:\n%s", out)
	}
}

func TestWriteDiagnosticSectionSkipsAnEmptyList(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	writeDiagnosticSection(&b, "new_diagnostics", nil)
	if b.String() != "" {
		t.Fatalf("empty section rendered %q", b.String())
	}
}

// Every reporting entry point has to survive a session with no LSP at all.
func TestReportingWithoutAManager(t *testing.T) {
	t.Parallel()
	if got := reportDiagnostics(t.Context(), nil, settleGrace, "/x/f.go"); got != "" {
		t.Errorf("reportDiagnostics = %q", got)
	}
	if got := fullDiagnosticsReport(t.Context(), nil, "/x/f.go"); got != "" {
		t.Errorf("fullDiagnosticsReport = %q", got)
	}
	if got := DiagnosticsSweep(t.Context(), nil); got != "" {
		t.Errorf("DiagnosticsSweep = %q", got)
	}
	ForgetReportedDiagnostics(nil, "s1")
}

func TestFormatDiagnosticNamesSeverityAndPosition(t *testing.T) {
	t.Parallel()
	got := formatDiagnostic("/x/f.go", diag(9, protocol.SeverityWarning, "unused"), "gopls")
	if !strings.HasPrefix(got, "Warn: /x/f.go:10:1 [gopls] unused") {
		t.Fatalf("formatted = %q", got)
	}
}
