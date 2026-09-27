package agent

import (
	"encoding/base64"
	"sync"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

// mediaTestModel is a non-Anthropic vision model, the shape that makes
// workaroundProviderMediaLimitations decode media payloads.
func mediaTestModel() Model {
	return Model{
		ModelCfg:   config.SelectedModel{Provider: "openai"},
		CatalogCfg: catalog.Model{SupportsImages: true},
	}
}

func mediaTestMessages(base64Data string) []fantasy.Message {
	return []fantasy.Message{
		{
			Role: fantasy.MessageRoleTool,
			Content: []fantasy.MessagePart{
				fantasy.ToolResultPart{
					ToolCallID: "call_1",
					Output: fantasy.ToolResultOutputContentMedia{
						Data:      base64Data,
						MediaType: "image/png",
					},
				},
			},
		},
	}
}

func TestDecodeMediaCached_ReusesDecode(t *testing.T) {
	env := testEnv(t)
	agent := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)

	payload := base64.StdEncoding.EncodeToString([]byte("fake-png-data"))

	first, err := agent.decodeMediaCached(payload)
	require.NoError(t, err)
	second, err := agent.decodeMediaCached(payload)
	require.NoError(t, err)

	require.Len(t, agent.mediaDecodeCache, 1, "the second decode must come from the cache")
	require.Same(t, &first[0], &second[0], "cached payloads must not be re-decoded")
	require.Equal(t, []byte("fake-png-data"), first)
}

func TestDecodeMediaCached_DistinctPayloadsCachedSeparately(t *testing.T) {
	env := testEnv(t)
	agent := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)

	first, err := agent.decodeMediaCached(base64.StdEncoding.EncodeToString([]byte("png-one")))
	require.NoError(t, err)
	second, err := agent.decodeMediaCached(base64.StdEncoding.EncodeToString([]byte("png-two")))
	require.NoError(t, err)

	require.Len(t, agent.mediaDecodeCache, 2)
	require.Equal(t, []byte("png-one"), first)
	require.Equal(t, []byte("png-two"), second)
}

func TestDecodeMediaCached_EvictsArbitraryEntryAtCap(t *testing.T) {
	env := testEnv(t)
	agent := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)

	for i := range maxDecodedMediaCacheEntries + 2 {
		payload := base64.StdEncoding.EncodeToString([]byte{byte(i)})
		_, err := agent.decodeMediaCached(payload)
		require.NoError(t, err)
	}
	require.Len(t, agent.mediaDecodeCache, maxDecodedMediaCacheEntries)

	// A payload that survived eviction still decodes to the right bytes.
	payload := base64.StdEncoding.EncodeToString([]byte{byte(maxDecodedMediaCacheEntries)})
	decoded, err := agent.decodeMediaCached(payload)
	require.NoError(t, err)
	require.Equal(t, []byte{byte(maxDecodedMediaCacheEntries)}, decoded)
	require.Len(t, agent.mediaDecodeCache, maxDecodedMediaCacheEntries)
}

func TestDecodeMediaCached_InvalidBase64NotCached(t *testing.T) {
	env := testEnv(t)
	agent := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)

	_, err := agent.decodeMediaCached("!!!not base64!!!")
	require.Error(t, err)
	require.Empty(t, agent.mediaDecodeCache)
}

func TestDecodeMediaCached_ConcurrentDecodesShareOneEntry(t *testing.T) {
	env := testEnv(t)
	agent := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)

	payload := base64.StdEncoding.EncodeToString([]byte("shared-png"))
	const goroutines = 8

	results := make([][]byte, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Go(func() {
			results[i], errs[i] = agent.decodeMediaCached(payload)
		})
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, agent.mediaDecodeCache, 1)
	for _, decoded := range results {
		require.Equal(t, []byte("shared-png"), decoded)
	}
}

func TestWorkaroundProviderMediaLimitations_DecodesOncePerPayload(t *testing.T) {
	env := testEnv(t)
	agent := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)
	messages := mediaTestMessages(base64.StdEncoding.EncodeToString([]byte("fake-png-data")))

	first := agent.workaroundProviderMediaLimitations(messages, mediaTestModel())
	require.Len(t, agent.mediaDecodeCache, 1, "the workaround must populate the decode cache")

	second := agent.workaroundProviderMediaLimitations(messages, mediaTestModel())
	require.Len(t, agent.mediaDecodeCache, 1)

	fileOf := func(msgs []fantasy.Message) fantasy.FilePart {
		require.Len(t, msgs, 2)
		require.Equal(t, fantasy.MessageRoleUser, msgs[1].Role)
		file, ok := fantasy.AsMessagePart[fantasy.FilePart](msgs[1].Content[1])
		require.True(t, ok)
		return file
	}
	firstFile := fileOf(first)
	secondFile := fileOf(second)
	require.Same(t, &firstFile.Data[0], &secondFile.Data[0], "the synthetic file must reuse the cached decode")
}
