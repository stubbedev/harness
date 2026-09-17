package agent

import (
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openai"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/session"
)

func TestWithPromptCacheKey(t *testing.T) {
	t.Parallel()

	t.Run("sets the session hash on chat completions options", func(t *testing.T) {
		t.Parallel()
		opts := fantasy.ProviderOptions{
			openai.Name: &openai.ProviderOptions{},
		}
		got := withPromptCacheKey("s1", opts)
		chat, ok := got[openai.Name].(*openai.ProviderOptions)
		require.True(t, ok)
		require.NotNil(t, chat.PromptCacheKey)
		require.Equal(t, session.HashID("s1"), *chat.PromptCacheKey)
	})

	t.Run("sets the session hash on responses options", func(t *testing.T) {
		t.Parallel()
		opts := fantasy.ProviderOptions{
			openai.Name: &openai.ResponsesProviderOptions{},
		}
		got := withPromptCacheKey("s1", opts)
		responses, ok := got[openai.Name].(*openai.ResponsesProviderOptions)
		require.True(t, ok)
		require.NotNil(t, responses.PromptCacheKey)
		require.Equal(t, session.HashID("s1"), *responses.PromptCacheKey)
	})

	t.Run("keeps a user-configured key", func(t *testing.T) {
		t.Parallel()
		custom := "custom-key"
		opts := fantasy.ProviderOptions{
			openai.Name: &openai.ProviderOptions{PromptCacheKey: &custom},
		}
		got := withPromptCacheKey("s1", opts)
		chat, ok := got[openai.Name].(*openai.ProviderOptions)
		require.True(t, ok)
		require.Equal(t, "custom-key", *chat.PromptCacheKey)
	})

	t.Run("leaves non-openai options untouched", func(t *testing.T) {
		t.Parallel()
		opts := fantasy.ProviderOptions{
			anthropic.Name: &anthropic.ProviderCacheControlOptions{},
		}
	require.Equal(t, opts, withPromptCacheKey("s1", opts))
		_, hasOpenAI := opts[openai.Name]
		require.False(t, hasOpenAI)
	})
}

func TestWithPromptCacheKeyDisabledViaEnv(t *testing.T) {
	t.Setenv("HARNESS_DISABLE_PROMPT_CACHE_KEY", "1")
	opts := fantasy.ProviderOptions{
		openai.Name: &openai.ProviderOptions{},
	}
	got := withPromptCacheKey("s1", opts)
	chat, ok := got[openai.Name].(*openai.ProviderOptions)
	require.True(t, ok)
	require.Nil(t, chat.PromptCacheKey)
}
