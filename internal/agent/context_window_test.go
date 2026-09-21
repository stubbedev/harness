package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
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

func TestWithResultCap(t *testing.T) {
	// t.TempDir is 0775 under a umask of 002, which the scratch privacy
	// check rejects; tighten it to 0700.
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	t.Setenv("HARNESS_SCRATCH_DIR", root)
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, "cap-session")
	for _, name := range []string{"builtin", "mcp_server_tool"} {
		t.Run(name, func(t *testing.T) {
			response := fantasy.ToolResponse{
				Type: "text", Content: strings.Repeat("x", maxToolResultChars+1),
				IsError: true, StopTurn: true, Metadata: `{"status":42}`,
			}
			call := fantasy.ToolCall{ID: "call", Name: name, Input: "{}"}
			original := fantasy.NewAgentTool(name, "description", func(gotCtx context.Context, _ struct{}, gotCall fantasy.ToolCall) (fantasy.ToolResponse, error) {
				require.Equal(t, ctx, gotCtx)
				require.Equal(t, call, gotCall)
				return response, nil
			})
			wrapped := withResultCap([]fantasy.AgentTool{original})
			require.Equal(t, original.Info(), wrapped[0].Info())
			got, err := wrapped[0].Run(ctx, call)
			require.NoError(t, err)
			require.LessOrEqual(t, len(got.Content), maxToolResultChars)
			require.Contains(t, got.Content, "saved in full to:")
			got.Content = response.Content
			require.Equal(t, response, got)
		})
	}
	t.Run("run error passes through", func(t *testing.T) {
		wantErr := errors.New("tool failed")
		response := fantasy.NewTextResponse("failure details")
		original := fantasy.NewAgentTool("failure", "description", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return response, wantErr
		})
		wantResponse, wantRunErr := original.Run(ctx, fantasy.ToolCall{Input: "{}"})
		got, err := withResultCap([]fantasy.AgentTool{original})[0].Run(ctx, fantasy.ToolCall{Input: "{}"})
		require.Equal(t, wantRunErr, err)
		require.Equal(t, wantResponse, got)
	})
	require.Nil(t, withResultCap(nil))
}
