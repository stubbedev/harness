package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
)

func TestUsableContextWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		contextWindow int64
		defaultMax    int64
		modelMax      int64
		want          int64
	}{
		{"unknown window", 0, 0, 0, 0},
		{"no output budget", 200_000, 0, 0, 200_000},
		{"default max tokens reserved", 200_000, 8_192, 0, 191_808},
		{"configured max tokens wins", 262_144, 8_192, 131_072, 131_072},
		{"max tokens at window is ignored", 200_000, 200_000, 0, 200_000},
		{"max tokens above window is ignored", 200_000, 0, 400_000, 200_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := Model{
				CatalogCfg: catalog.Model{
					ContextWindow:    tt.contextWindow,
					DefaultMaxTokens: tt.defaultMax,
				},
			}
			model.ModelCfg.MaxTokens = tt.modelMax
			require.Equal(t, tt.want, usableContextWindow(model))
		})
	}
}

func TestIsContextLengthError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("connection reset by peer"), false},
		{errors.New("bad request"), false},
		{errors.New("prompt is too long: 300000 tokens > 200000 maximum"), true},
		{errors.New("This request's input length of 210000 tokens exceeds the model's context window"), true},
		{errors.New("Error code: 400 - {'type': 'error', 'code': 'context_length_exceeded'}"), true},
		{fmt.Errorf("wrapped: %w", errContextWindowExceeded), true},
		{errors.New("maximum context length is 200000 tokens"), true},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, isContextLengthError(tt.err))
	}
}

func TestTruncateToolResponse(t *testing.T) {
	t.Parallel()
	t.Run("small response passes through", func(t *testing.T) {
		t.Parallel()
		response := fantasy.NewTextResponse("small")
		require.Equal(t, response, truncateToolResponse(response))
	})
	t.Run("media response passes through", func(t *testing.T) {
		t.Parallel()
		response := fantasy.ToolResponse{Content: strings.Repeat("a", maxToolResultChars+1), Data: []byte{1}}
		require.Equal(t, response, truncateToolResponse(response))
	})
	t.Run("oversized response is capped with a visible marker", func(t *testing.T) {
		t.Parallel()
		content := strings.Repeat("ab", maxToolResultChars) + "xyz"
		truncated := truncateToolResponse(fantasy.NewTextResponse(content))
		expected := strings.Repeat("ab", maxToolResultChars/2) + fmt.Sprintf(
			"\n\n(result truncated: %d of %d characters shown — narrow the request (filter, offset, or paginate) to see the rest)",
			maxToolResultChars, len(content),
		)
		require.Equal(t, expected, truncated.Content)
	})
	t.Run("cut does not split a rune", func(t *testing.T) {
		t.Parallel()
		content := strings.Repeat("é", maxToolResultChars+10)
		truncated := truncateToolResponse(fantasy.NewTextResponse(content))
		require.Contains(t, truncated.Content, "result truncated")
		for _, r := range truncated.Content {
			require.NotEqual(t, 0xFFFD, r)
		}
	})
}
