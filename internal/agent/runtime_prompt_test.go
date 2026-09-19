package agent

import (
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/prompt"
)

func TestSplitRuntimePrompt(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"plain", "prefix\n<harness_runtime>\nmissing end", "prefix\n<harness_runtime>\ncontext\n</harness_runtime>\nsuffix"} {
		stable, runtime := splitRuntimePrompt(input)
		require.Equal(t, input, stable)
		require.Empty(t, runtime)
	}
	stable, runtime := splitRuntimePrompt("instructions\n<harness_runtime>\nenvironment\n</harness_runtime>\n")
	require.Equal(t, "instructions", stable)
	require.Equal(t, "environment", runtime)
}

func TestCoderRuntimeChangesLeaveStableInstructionsUnchanged(t *testing.T) {
	t.Parallel()
	store := newPromptTestStore(t)
	var stablePrompts []string
	for _, index := range []string{"first memory", "second memory"} {
		pr, err := coderPrompt(prompt.WithMemoryEnabled(true), prompt.WithMemoryIndex(index))
		require.NoError(t, err)
		rendered, err := pr.Build(t.Context(), "test", "test", store)
		require.NoError(t, err)
		stable, runtime := splitRuntimePrompt(rendered)
		require.NotContains(t, stable, index)
		require.NotContains(t, stable, "Today's date:")
		require.Contains(t, stable, "Save the moment you learn")
		require.Contains(t, runtime, index)
		require.Contains(t, runtime, "Today's date:")
		stablePrompts = append(stablePrompts, stable)
	}
	require.Equal(t, stablePrompts[0], stablePrompts[1])
}

func TestRuntimeContextIsEphemeralAndFollowsHistory(t *testing.T) {
	t.Parallel()
	history := []fantasy.Message{fantasy.NewUserMessage("task")}
	for range 2 {
		prepared := withRuntimeContext(history, "environment")
		require.Len(t, prepared, 2)
		require.Len(t, history, 1)
		text, ok := fantasy.AsMessagePart[fantasy.TextPart](prepared[1].Content[0])
		require.True(t, ok)
		require.Equal(t, 1, strings.Count(text.Text, "environment"))
	}
	require.Equal(t, history, withRuntimeContext(history, ""))
}
