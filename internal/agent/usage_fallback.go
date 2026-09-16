package agent

import (
	"fmt"
	"hash/fnv"
	"sync"

	"charm.land/fantasy"
	"github.com/tiktoken-go/tokenizer"
)

func usageIsZero(usage fantasy.Usage) bool {
	return usage.InputTokens == 0 &&
		usage.OutputTokens == 0 &&
		usage.TotalTokens == 0 &&
		usage.ReasoningTokens == 0 &&
		usage.CacheCreationTokens == 0 &&
		usage.CacheReadTokens == 0
}

func fallbackStepUsage(messages []fantasy.Message, step fantasy.StepResult) (fantasy.Usage, bool) {
	if !usageIsZero(step.Usage) {
		return step.Usage, false
	}

	inputTokens := estimateMessageTokens(messages)
	outputTokens := estimateStepCompletionTokens(step)
	if inputTokens == 0 && outputTokens == 0 {
		return fantasy.Usage{}, false
	}

	return fantasy.Usage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
	}, true
}

func cloneFantasyMessages(messages []fantasy.Message) []fantasy.Message {
	cloned := make([]fantasy.Message, len(messages))
	for i, msg := range messages {
		cloned[i] = msg
		cloned[i].Content = append([]fantasy.MessagePart(nil), msg.Content...)
	}
	return cloned
}

func estimateMessageTokens(messages []fantasy.Message) int64 {
	var tokens int64
	for _, msg := range messages {
		tokens += approxTokenCount(string(msg.Role))
		for _, part := range msg.Content {
			tokens += estimateMessagePartTokens(part)
		}
	}
	return tokens
}

func estimateStepCompletionTokens(step fantasy.StepResult) int64 {
	var tokens int64
	for _, content := range step.Content {
		switch c := content.(type) {
		case fantasy.TextContent:
			tokens += approxTokenCount(c.Text)
		case *fantasy.TextContent:
			tokens += approxTokenCount(c.Text)
		case fantasy.ReasoningContent:
			tokens += approxTokenCount(c.Text)
		case *fantasy.ReasoningContent:
			tokens += approxTokenCount(c.Text)
		case fantasy.FileContent:
			tokens += estimateGeneratedFileTokens(c)
		case *fantasy.FileContent:
			tokens += estimateGeneratedFileTokens(*c)
		case fantasy.SourceContent:
			tokens += estimateSourceTokens(c)
		case *fantasy.SourceContent:
			tokens += estimateSourceTokens(*c)
		case fantasy.ToolCallContent:
			tokens += estimateToolCallTokens(c.ToolName, c.Input)
		case *fantasy.ToolCallContent:
			tokens += estimateToolCallTokens(c.ToolName, c.Input)
		case fantasy.ToolResultContent:
			if c.ProviderExecuted {
				tokens += estimateToolResultContentTokens(c.ToolCallID, c.ToolName, c.ClientMetadata, c.Result)
			}
		case *fantasy.ToolResultContent:
			if c.ProviderExecuted {
				tokens += estimateToolResultContentTokens(c.ToolCallID, c.ToolName, c.ClientMetadata, c.Result)
			}
		}
	}
	return tokens
}

func estimateMessagePartTokens(part fantasy.MessagePart) int64 {
	switch p := part.(type) {
	case fantasy.TextPart:
		return approxTokenCount(p.Text)
	case *fantasy.TextPart:
		return approxTokenCount(p.Text)
	case fantasy.ReasoningPart:
		return approxTokenCount(p.Text)
	case *fantasy.ReasoningPart:
		return approxTokenCount(p.Text)
	case fantasy.FilePart:
		return estimateFilePartTokens(p)
	case *fantasy.FilePart:
		return estimateFilePartTokens(*p)
	case fantasy.ToolCallPart:
		return estimateToolCallTokens(p.ToolName, p.Input)
	case *fantasy.ToolCallPart:
		return estimateToolCallTokens(p.ToolName, p.Input)
	case fantasy.ToolResultPart:
		return estimateToolResultContentTokens(p.ToolCallID, "", "", p.Output)
	case *fantasy.ToolResultPart:
		return estimateToolResultContentTokens(p.ToolCallID, "", "", p.Output)
	default:
		return 0
	}
}

func estimateToolCallTokens(toolName, input string) int64 {
	return approxTokenCount(toolName) + approxTokenCount(input)
}

func estimateToolResultContentTokens(toolCallID, toolName, metadata string, output fantasy.ToolResultOutputContent) int64 {
	tokens := approxTokenCount(toolCallID) + approxTokenCount(toolName) + approxTokenCount(metadata)
	switch result := output.(type) {
	case fantasy.ToolResultOutputContentText:
		tokens += approxTokenCount(result.Text)
	case *fantasy.ToolResultOutputContentText:
		tokens += approxTokenCount(result.Text)
	case fantasy.ToolResultOutputContentError:
		if result.Error != nil {
			tokens += approxTokenCount(result.Error.Error())
		}
	case *fantasy.ToolResultOutputContentError:
		if result.Error != nil {
			tokens += approxTokenCount(result.Error.Error())
		}
	case fantasy.ToolResultOutputContentMedia:
		tokens += estimateMediaTokens(result.MediaType, result.Text, len(result.Data))
	case *fantasy.ToolResultOutputContentMedia:
		tokens += estimateMediaTokens(result.MediaType, result.Text, len(result.Data))
	}
	return tokens
}

func estimateFilePartTokens(file fantasy.FilePart) int64 {
	return estimateMediaTokens(file.MediaType, file.Filename, len(file.Data))
}

func estimateGeneratedFileTokens(file fantasy.FileContent) int64 {
	return estimateMediaTokens(file.MediaType, "", len(file.Data))
}

func estimateMediaTokens(mediaType, text string, dataBytes int) int64 {
	if dataBytes == 0 {
		return approxTokenCount(mediaType) + approxTokenCount(text)
	}
	return approxTokenCount(fmt.Sprintf("%s %s %d bytes", mediaType, text, dataBytes))
}

func estimateSourceTokens(source fantasy.SourceContent) int64 {
	return approxTokenCount(string(source.SourceType)) +
		approxTokenCount(source.ID) +
		approxTokenCount(source.URL) +
		approxTokenCount(source.Title) +
		approxTokenCount(source.MediaType) +
		approxTokenCount(source.Filename)
}

// tokenCodec is the tokenizer every estimate goes through: cl100k_base,
// the BPE behind GPT-4 and a close proxy for the rest. No provider
// publishes its exact tokenizer for every model, but a real BPE is
// within a few percent on code and non-Latin text where the old
// characters-over-four rule was off by half, and the compaction
// thresholds are only as sharp as this count.
var tokenCodec = sync.OnceValues(func() (tokenizer.Codec, error) {
	return tokenizer.Get(tokenizer.Cl100kBase)
})

// tokenCountCache remembers counts for strings large enough to be worth
// the tokenizer's time. The same history is estimated on every step, so
// nearly every string seen is one that was seen before; without this
// the estimate would re-tokenize the whole session each time.
var tokenCountCache = struct {
	sync.Mutex
	counts map[uint64]int64
}{counts: map[uint64]int64{}}

const (
	// tokenCacheMinBytes is the size below which counting is cheaper
	// than hashing and caching.
	tokenCacheMinBytes = 128
	// tokenCacheMaxEntries bounds the cache; past it the map is dropped
	// and rebuilt from the strings still in use.
	tokenCacheMaxEntries = 16384
)

// approxTokenCount counts the tokens in s with the BPE tokenizer,
// falling back to the four-characters rule if the tokenizer is
// unavailable.
func approxTokenCount(s string) int64 {
	if s == "" {
		return 0
	}
	codec, err := tokenCodec()
	if err != nil {
		return int64((len(s) + 3) / 4)
	}
	if len(s) < tokenCacheMinBytes {
		return countTokens(codec, s)
	}
	key := fnvHash(s)
	tokenCountCache.Lock()
	n, ok := tokenCountCache.counts[key]
	tokenCountCache.Unlock()
	if ok {
		return n
	}
	n = countTokens(codec, s)
	tokenCountCache.Lock()
	if len(tokenCountCache.counts) >= tokenCacheMaxEntries {
		tokenCountCache.counts = map[uint64]int64{}
	}
	tokenCountCache.counts[key] = n
	tokenCountCache.Unlock()
	return n
}

func countTokens(codec tokenizer.Codec, s string) int64 {
	n, err := codec.Count(s)
	if err != nil {
		return int64((len(s) + 3) / 4)
	}
	return int64(n)
}

// fnvHash keys the count cache. Collisions cost an estimate that is off
// for one string, never anything worse, so a 64-bit hash is plenty.
func fnvHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}
