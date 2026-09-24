package agent

import (
	"fmt"
	"hash/maphash"
	"sync"
	"unsafe"

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
	return fallbackStepUsageWith(estimateMessageTokens, messages, step)
}

// fallbackStepUsageWith is fallbackStepUsage with the prompt estimate
// taken from estimate, so a turn can hand it its historyTokenEstimator.
func fallbackStepUsageWith(estimate func([]fantasy.Message) int64, messages []fantasy.Message, step fantasy.StepResult) (fantasy.Usage, bool) {
	if !usageIsZero(step.Usage) {
		return step.Usage, false
	}

	inputTokens := estimate(messages)
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
	return estimateMessageTokensWith(approxTokenCount, messages)
}

func estimateMessageTokensWith(count func(string) int64, messages []fantasy.Message) int64 {
	var tokens int64
	for _, msg := range messages {
		tokens += count(string(msg.Role))
		for _, part := range msg.Content {
			tokens += estimateMessagePartTokens(count, part)
		}
	}
	return tokens
}

// stringIdentity names a string by where its bytes live rather than by
// what they say. Strings are immutable, so two strings with the same
// data pointer and length are the same text, and comparing them costs
// nothing however long they are. The pointer also keeps those bytes
// alive, so while an identity is held its address cannot be handed to
// a different string.
type stringIdentity struct {
	data *byte
	n    int
}

// historyTokenEstimator estimates one turn's requests. Each step sends
// the previous step's history plus a little, built from the same
// strings, so it remembers the count for every string of the last
// estimate by identity: an unchanged history costs a map lookup per
// string instead of a pass over every byte of it, and only what is new
// goes to the tokenizer (or the process-wide count cache). A history
// that was rewritten, merged or compacted in between simply holds new
// strings, which are counted afresh; nothing has to detect the rewrite.
// Only the strings of the latest estimate are kept, so memory tracks
// the current request rather than the whole turn.
type historyTokenEstimator struct {
	mu   sync.Mutex
	prev map[stringIdentity]int64
	cur  map[stringIdentity]int64
}

func newHistoryTokenEstimator() *historyTokenEstimator {
	return &historyTokenEstimator{}
}

// Messages estimates messages as estimateMessageTokens does.
func (e *historyTokenEstimator) Messages(messages []fantasy.Message) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cur = make(map[stringIdentity]int64, len(e.prev))
	tokens := estimateMessageTokensWith(e.count, messages)
	e.prev, e.cur = e.cur, nil
	return tokens
}

func (e *historyTokenEstimator) count(s string) int64 {
	if s == "" {
		return 0
	}
	id := stringIdentity{data: unsafe.StringData(s), n: len(s)}
	if n, ok := e.cur[id]; ok {
		return n
	}
	n, ok := e.prev[id]
	if !ok {
		n = approxTokenCount(s)
	}
	e.cur[id] = n
	return n
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
			tokens += estimateGeneratedFileTokens(approxTokenCount, c)
		case *fantasy.FileContent:
			tokens += estimateGeneratedFileTokens(approxTokenCount, *c)
		case fantasy.SourceContent:
			tokens += estimateSourceTokens(approxTokenCount, c)
		case *fantasy.SourceContent:
			tokens += estimateSourceTokens(approxTokenCount, *c)
		case fantasy.ToolCallContent:
			tokens += estimateToolCallTokens(approxTokenCount, c.ToolName, c.Input)
		case *fantasy.ToolCallContent:
			tokens += estimateToolCallTokens(approxTokenCount, c.ToolName, c.Input)
		case fantasy.ToolResultContent:
			if c.ProviderExecuted {
				tokens += estimateToolResultContentTokens(approxTokenCount, c.ToolCallID, c.ToolName, c.ClientMetadata, c.Result)
			}
		case *fantasy.ToolResultContent:
			if c.ProviderExecuted {
				tokens += estimateToolResultContentTokens(approxTokenCount, c.ToolCallID, c.ToolName, c.ClientMetadata, c.Result)
			}
		}
	}
	return tokens
}

func estimateMessagePartTokens(count func(string) int64, part fantasy.MessagePart) int64 {
	switch p := part.(type) {
	case fantasy.TextPart:
		return count(p.Text)
	case *fantasy.TextPart:
		return count(p.Text)
	case fantasy.ReasoningPart:
		return count(p.Text)
	case *fantasy.ReasoningPart:
		return count(p.Text)
	case fantasy.FilePart:
		return estimateFilePartTokens(count, p)
	case *fantasy.FilePart:
		return estimateFilePartTokens(count, *p)
	case fantasy.ToolCallPart:
		return estimateToolCallTokens(count, p.ToolName, p.Input)
	case *fantasy.ToolCallPart:
		return estimateToolCallTokens(count, p.ToolName, p.Input)
	case fantasy.ToolResultPart:
		return estimateToolResultContentTokens(count, p.ToolCallID, "", "", p.Output)
	case *fantasy.ToolResultPart:
		return estimateToolResultContentTokens(count, p.ToolCallID, "", "", p.Output)
	default:
		return 0
	}
}

func estimateToolCallTokens(count func(string) int64, toolName, input string) int64 {
	return count(toolName) + count(input)
}

func estimateToolResultContentTokens(count func(string) int64, toolCallID, toolName, metadata string, output fantasy.ToolResultOutputContent) int64 {
	tokens := count(toolCallID) + count(toolName) + count(metadata)
	switch result := output.(type) {
	case fantasy.ToolResultOutputContentText:
		tokens += count(result.Text)
	case *fantasy.ToolResultOutputContentText:
		tokens += count(result.Text)
	case fantasy.ToolResultOutputContentError:
		if result.Error != nil {
			tokens += count(result.Error.Error())
		}
	case *fantasy.ToolResultOutputContentError:
		if result.Error != nil {
			tokens += count(result.Error.Error())
		}
	case fantasy.ToolResultOutputContentMedia:
		tokens += estimateMediaTokens(count, result.MediaType, result.Text, len(result.Data))
	case *fantasy.ToolResultOutputContentMedia:
		tokens += estimateMediaTokens(count, result.MediaType, result.Text, len(result.Data))
	}
	return tokens
}

func estimateFilePartTokens(count func(string) int64, file fantasy.FilePart) int64 {
	return estimateMediaTokens(count, file.MediaType, file.Filename, len(file.Data))
}

func estimateGeneratedFileTokens(count func(string) int64, file fantasy.FileContent) int64 {
	return estimateMediaTokens(count, file.MediaType, "", len(file.Data))
}

func estimateMediaTokens(count func(string) int64, mediaType, text string, dataBytes int) int64 {
	if dataBytes == 0 {
		return count(mediaType) + count(text)
	}
	return count(fmt.Sprintf("%s %s %d bytes", mediaType, text, dataBytes))
}

func estimateSourceTokens(count func(string) int64, source fantasy.SourceContent) int64 {
	return count(string(source.SourceType)) +
		count(source.ID) +
		count(source.URL) +
		count(source.Title) +
		count(source.MediaType) +
		count(source.Filename)
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
	key := maphash.String(tokenCacheSeed, s)
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

// tokenCacheSeed keys the count cache. maphash reads the string in
// place, many bytes at a time, where FNV went byte by byte over a copy.
// Collisions cost an estimate that is off for one string, never
// anything worse, so a 64-bit hash is plenty, and the cache lives only
// in memory, so a seed per process is fine.
var tokenCacheSeed = maphash.MakeSeed()
