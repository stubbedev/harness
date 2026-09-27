package model

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
)

// flipKM is a help.KeyMap whose hint description changes at runtime,
// the way the real UI's hints follow mode and focus. help.View output
// therefore changes without the keymap identity changing, which is
// exactly what the memo's binds token must catch.
type flipKM struct{ desc string }

func (m *flipKM) ShortHelp() []key.Binding {
	return []key.Binding{key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", m.desc))}
}

func (m *flipKM) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		m.ShortHelp(),
		{key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "more"))},
	}
}

// TestStatusHelpViewMemoInvalidation pins that the memoized help line
// re-renders whenever any input to it changes — the keymap's live hint
// state, ShowAll, the truncation width, or the styles — and is stable
// otherwise.
func TestStatusHelpViewMemoInvalidation(t *testing.T) {
	t.Parallel()

	com := common.DefaultCommon(nil)
	km := &flipKM{desc: "focus editor"}
	s := NewStatus(com, km)
	s.SetWidth(120)

	first := s.helpView(120)
	require.Contains(t, ansi.Strip(first), "focus editor")
	require.Equal(t, first, s.helpView(120), "an unchanged state must be served from the memo")

	// The keymap's hint text changed; the keymap itself did not.
	km.desc = "focus chat"
	changed := s.helpView(120)
	require.Contains(t, ansi.Strip(changed), "focus chat")
	require.NotContains(t, ansi.Strip(changed), "focus editor")

	// ShowAll flips to the full help view, which shows the extra
	// column the short view hides.
	s.ToggleHelp()
	full := s.helpView(120)
	require.NotEqual(t, changed, full)
	require.Contains(t, ansi.Strip(full), "?")

	// A width change re-renders (the help model truncates to it; the
	// theme pads the help view by one column on each side).
	s.ToggleHelp()
	s.SetWidth(12)
	truncated := s.helpView(12)
	require.NotEqual(t, changed, truncated, "the width is part of the memo key")
	require.Contains(t, ansi.Strip(truncated), "…", "the line truncates to the draw width")

	// A theme change moves the styles token, so the line is re-rendered
	// through the new styles even though its text is identical.
	s.SetWidth(120)
	styled := s.helpView(120)
	com.Styles.Status.Help = com.Styles.Status.Help.Foreground(lipgloss.Color("#123456"))
	s.help.Styles.ShortKey = s.help.Styles.ShortKey.Foreground(lipgloss.Color("#654321"))
	require.NotEqual(t, styled, s.helpView(120), "a style change must re-render the line")
	require.Equal(t, s.helpView(120), s.helpView(120))
}

// TestHeaderMemoInvalidation pins that the memoized header line is
// served unchanged while every input is unchanged and re-rendered when
// any input changes: the breadcrumb, diagnostics, width, and the theme
// refresh hook.
func TestHeaderMemoInvalidation(t *testing.T) {
	t.Parallel()

	com := headerTestCommon()
	h := newHeader(com)
	sess := &session.Session{ID: "s1"}

	draw := func(width int, diagnostics lsp.DiagnosticCounts, breadcrumb string) string {
		scr := uv.NewScreenBuffer(width, 1)
		h.drawHeader(scr, uv.Rect(0, 0, width, 1), sess, width, diagnostics, breadcrumb)
		return scr.Render()
	}

	plain := draw(120, lsp.DiagnosticCounts{}, "")
	require.Equal(t, plain, draw(120, lsp.DiagnosticCounts{}, ""), "an unchanged state must be served from the memo")

	// Diagnostics appear on the right half: a state change must
	// re-render, not serve the old line.
	withDiag := draw(120, lsp.DiagnosticCounts{Error: 1}, "")
	require.Contains(t, ansi.Strip(withDiag), "E1")

	// A child-session breadcrumb appears on the left half.
	withCrumb := draw(120, lsp.DiagnosticCounts{}, "parent")
	require.Contains(t, ansi.Strip(withCrumb), "parent")

	// A width change re-renders (padding and truncation differ).
	require.NotEqual(t, withDiag, draw(60, lsp.DiagnosticCounts{Error: 1}, ""))

	// The theme refresh hook invalidates the memo: the same state
	// redraws through the new styles and the ANSI output changes even
	// though the text does not.
	com.Styles.Header.Separator = com.Styles.Header.Separator.Foreground(lipgloss.Color("#123456"))
	h.refresh()
	require.NotEqual(t, draw(120, lsp.DiagnosticCounts{Error: 1}, ""), withDiag, "a theme change must re-render the line")
}

// TestHeaderMemoMatchesFreshRender guards the trivial non-invalidation:
// the memo serves byte-identical output for identical repeated frames,
// exactly like the drawHeaderLine helper's fresh-header render.
func TestHeaderMemoMatchesFreshRender(t *testing.T) {
	t.Parallel()

	com := headerTestCommon()
	sess := &session.Session{ID: "s1"}

	fresh := drawHeaderLine(t, com, sess, lsp.DiagnosticCounts{})

	h := newHeader(com)
	scr := uv.NewScreenBuffer(120, 1)
	h.drawHeader(scr, uv.Rect(0, 0, 120, 1), sess, 120, lsp.DiagnosticCounts{}, "")
	memo := strings.TrimRight(ansi.Strip(scr.Render()), " ")

	require.Equal(t, fresh, memo)
}
