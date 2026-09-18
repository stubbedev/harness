package envtools

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func lookPathOf(installed ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(installed, name) {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

// TestFilterOnPathKeepsListOrder: the summary only names what resolves,
// and the order is the curated list order, not map or PATH order.
func TestFilterOnPathKeepsListOrder(t *testing.T) {
	t.Parallel()

	got := FilterOnPath(modernTools, lookPathOf("bat", "rg", "tokei"))
	names := make([]string, 0, len(got))
	for _, t := range got {
		names = append(names, t.Name)
	}
	require.Equal(t, []string{"rg", "bat", "tokei"}, names)
}

func TestFilterOnPathEmptyWhenNothingResolves(t *testing.T) {
	t.Parallel()

	require.Empty(t, FilterOnPath(modernTools, func(string) (string, error) {
		return "", errors.New("not found")
	}))
}

// TestSummaryFormatsNameAndNote: every installed tool renders as
// "name (note)", comma-joined, so the prompt and the tool description
// read as one line.
func TestSummaryFormatsNameAndNote(t *testing.T) {
	t.Parallel()

	got := summary(FilterOnPath(modernTools, lookPathOf("fd", "jq")))
	require.Equal(t, "fd (find), jq (JSON)", got)
	require.Empty(t, summary(nil), "no tools means no claim at all")
}

// TestSummaryCoversEveryListedTool pins the format invariant: a summary
// of the whole list contains every tool exactly once.
func TestSummaryCoversEveryListedTool(t *testing.T) {
	t.Parallel()

	full := summary(modernTools)
	for _, tool := range modernTools {
		require.Equal(t, 1, strings.Count(full, tool.Name+" ("+tool.Note+")"), "tool %q", tool.Name)
	}
}
