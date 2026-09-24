package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPromptInvocationRoundTrip(t *testing.T) {
	t.Parallel()

	wrapped := FormatPromptInvocation("user:review", "Review the diff.\n\nBe thorough.")

	name, body, ok := ParsePromptInvocation(wrapped)
	require.True(t, ok)
	require.Equal(t, "user:review", name)
	require.Equal(t, "Review the diff.\n\nBe thorough.", body)
}

func TestPromptInvocationRoundTripBodyWithBlankLines(t *testing.T) {
	t.Parallel()

	body := "line one\n\nline two\n</not-the-close>\n\nline three"
	name, got, ok := ParsePromptInvocation(FormatPromptInvocation("/cmd", body))
	require.True(t, ok)
	require.Equal(t, "/cmd", name)
	require.Equal(t, body, got)
}

func TestParsePromptInvocationRejectsPlainAndMalformed(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"a plain user message",
		"<loaded_skill>\n  <name>jq</name>\n</loaded_skill>",
		"<invoked_prompt name=\"x\">no newline after head",
		"<invoked_prompt name=\"x\">\nbody without the closing element",
		"<invoked_prompt name=unquoted>\nbody\n</invoked_prompt>",
	}
	for _, content := range tests {
		_, _, ok := ParsePromptInvocation(content)
		require.False(t, ok, "content %q must not parse as an invocation", content)
	}
}
