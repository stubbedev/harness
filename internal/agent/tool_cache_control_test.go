package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"github.com/stretchr/testify/require"
)

func TestStampToolCacheControl(t *testing.T) {
	t.Parallel()

	newTool := func(name string) fantasy.AgentTool {
		return fantasy.NewAgentTool(name, "test", func(ctx context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("ok"), nil
		})
	}

	t.Run("stamps only the last tool and clears the rest", func(t *testing.T) {
		t.Parallel()
		first := newTool("first")
		first.SetProviderOptions(fantasy.ProviderOptions{
			anthropic.Name: &anthropic.ProviderCacheControlOptions{},
		})
		middle := newTool("middle")
		last := newTool("last")

		stampToolCacheControl([]fantasy.AgentTool{first, middle, last}, getCacheControlOptionsForTest())

		require.Empty(t, first.ProviderOptions())
		require.Empty(t, middle.ProviderOptions())
		require.Contains(t, last.ProviderOptions(), anthropic.Name)
	})

	t.Run("empty tool list is a no-op", func(t *testing.T) {
		t.Parallel()
		stampToolCacheControl(nil, getCacheControlOptionsForTest())
	})
}

func getCacheControlOptionsForTest() fantasy.ProviderOptions {
	return fantasy.ProviderOptions{
		anthropic.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
	}
}
