package chat

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// linkStyles returns the active theme's styles for renderer tests.
func linkStyles() *styles.Styles {
	s := styles.ThemeForProvider("")
	return &s
}

// toolParamListParams builds the params slice toolHeader callers pass:
// the main param first, then key, value pairs.
func toolParamListParams(main string, kv ...string) []string {
	return append([]string{main}, kv...)
}

// TestToolParamListHyperlinksBareURLs: a fetch-style URL param renders as
// an OSC 8 hyperlink so terminals open it on click; the visible text is
// unchanged and non-URL params stay plain.
func TestToolParamListHyperlinksBareURLs(t *testing.T) {
	t.Parallel()

	sty := linkStyles()
	url := "https://example.com/docs?a=1"

	got := toolParamList(sty, toolParamListParams(url), 200, nil)
	require.Contains(t, got, "\x1b]8;;"+url+"\a", "URL param is hyperlinked:\n%q", got)
	require.Contains(t, got, url, "the URL is still the visible text:\n%q", got)

	plain := toolParamList(sty, toolParamListParams("cmd.go"), 200, nil)
	require.NotContains(t, plain, "\x1b]8;;", "non-URL params carry no hyperlink:\n%q", plain)}

// TestIsHTTPURL pins the strictness: only absolute http(s) URLs with a
// host link; whitespace or scheme-less lookalikes never do.
func TestIsHTTPURL(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"https://example.com",
		"http://localhost:8080/x",
		"https://example.com/a?q=1#frag",
	} {
		require.True(t, isHTTPURL(s), "expected URL: %q", s)
	}

	for _, s := range []string{
		"",
		"example.com",
		"ftp://example.com",
		"https://example.com/a b",
		"see https://example.com",
		"//example.com",
	} {
		require.False(t, isHTTPURL(s), "expected non-URL: %q", s)
	}
}

// TestToolParamListTruncationKeepsHyperlink: the OSC 8 wrapper survives
// ansi.Truncate, so a truncated URL still opens its full target.
func TestToolParamListTruncationKeepsHyperlink(t *testing.T) {
	t.Parallel()

	sty := linkStyles()
	url := "https://example.com/very/long/path/that/will/not/fit"

	got := toolParamList(sty, toolParamListParams(url), 30, nil)
	require.LessOrEqual(t, ansi.StringWidth(got), 30, "truncated to width:\n%q", got)
	require.Contains(t, got, "\x1b]8;;"+url, "hyperlink opens with the full target:\n%q", got)
	require.Contains(t, got, "\x1b]8;;\a", "hyperlink closes even when truncated:\n%q", got)
}
