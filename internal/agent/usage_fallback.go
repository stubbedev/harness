package agent

import (
	"cmp"
	"fmt"
	"hash/maphash"
	"maps"
	"slices"
	"sync"
	"unicode/utf8"
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

// cloneFantasyMessages copies the message slice before a step runs so
// the fallback usage estimator at OnStepFinish sees the request exactly
// as it was sent. Messages and their parts are immutable from here on —
// the estimator only reads — so sharing the part slices is safe and
// avoids a fresh part slice per message per step.
func cloneFantasyMessages(messages []fantasy.Message) []fantasy.Message {
	cloned := make([]fantasy.Message, len(messages))
	copy(cloned, messages)
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
//
// [historyTokenEstimator.Project] goes further for a request that is
// only compared against a limit: a string nothing has counted yet is
// first taken at a rough estimate, bounded above by tokenUpperBound, and
// the tokenizer runs only when the bounds leave the comparison open.
type historyTokenEstimator struct {
	mu   sync.Mutex
	prev map[stringIdentity]tokenCount
	cur  map[stringIdentity]tokenCount

	// pending holds, during one estimate, the strings it has not counted
	// with the tokenizer, and slack how far the true count of those
	// strings can lie above their estimates.
	pending map[stringIdentity]*pendingTokens
	slack   int64
}

// tokenCount is the count remembered for one string: exact when the
// tokenizer produced it, a rough estimate otherwise.
type tokenCount struct {
	n     int64
	exact bool
}

// pendingTokens is a string an estimate took at its rough count, with
// the bound on its true count and how many times the request holds it.
type pendingTokens struct {
	s           string
	estimate    int64
	upper       int64
	occurrences int64
}

func newHistoryTokenEstimator() *historyTokenEstimator {
	return &historyTokenEstimator{}
}

// Messages estimates messages as estimateMessageTokens does.
func (e *historyTokenEstimator) Messages(messages []fantasy.Message) int64 {
	return e.estimate(messages, 0, true)
}

// Project estimates messages for a check against limit, running the
// tokenizer only as far as that check needs it. The result reaches limit
// exactly when the full count would, so a decision taken by comparing it
// with limit is the one the full count gives; below limit it may be a
// rougher figure than Messages returns.
//
// Far from the limit nothing new is tokenized: every uncounted string is
// at most one token per byte, and when even that bound leaves the request
// under limit the rough figure stands. Closer in, the uncounted strings
// are tokenized largest first until the bound clears limit or none is
// left. A string left uncounted is looked at again on the next estimate.
func (e *historyTokenEstimator) Project(messages []fantasy.Message, limit int64) int64 {
	return e.estimate(messages, limit, false)
}

func (e *historyTokenEstimator) estimate(messages []fantasy.Message, limit int64, exact bool) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cur = make(map[stringIdentity]tokenCount, len(e.prev))
	e.pending = make(map[stringIdentity]*pendingTokens)
	e.slack = 0
	tokens := estimateMessageTokensWith(e.count, messages)

	if len(e.pending) > 0 {
		ids := slices.Collect(maps.Keys(e.pending))
		slices.SortFunc(ids, func(a, b stringIdentity) int {
			return cmp.Compare(e.pending[b].upper-e.pending[b].estimate, e.pending[a].upper-e.pending[a].estimate)
		})
		for _, id := range ids {
			if !exact && tokens+e.slack < limit {
				break
			}
			p := e.pending[id]
			n := approxTokenCount(p.s)
			tokens += p.occurrences * (n - p.estimate)
			e.slack -= p.occurrences * (p.upper - p.estimate)
			e.cur[id] = tokenCount{n: n, exact: true}
			delete(e.pending, id)
		}
		for id, p := range e.pending {
			e.cur[id] = tokenCount{n: p.estimate}
		}
	}

	e.prev, e.cur, e.pending = e.cur, nil, nil
	return tokens
}

// count is the per-string count of an estimate in progress: the exact
// count when one is known, the rough estimate otherwise, with the string
// recorded as pending so estimate can settle it if it has to.
func (e *historyTokenEstimator) count(s string) int64 {
	if s == "" {
		return 0
	}
	id := stringIdentity{data: unsafe.StringData(s), n: len(s)}
	if p, ok := e.pending[id]; ok {
		p.occurrences++
		e.slack += p.upper - p.estimate
		return p.estimate
	}
	if c, ok := e.cur[id]; ok {
		return c.n
	}
	if c, ok := e.prev[id]; ok && c.exact {
		e.cur[id] = c
		return c.n
	}
	// Short strings are cheaper to count than to track, and a string
	// counted before - a result warmed when it arrived, or one an
	// earlier turn counted - costs a lookup.
	n, ok := cachedTokenCount(s)
	if !ok && len(s) < tokenCacheMinBytes {
		n, ok = approxTokenCount(s), true
	}
	if ok {
		e.cur[id] = tokenCount{n: n, exact: true}
		return n
	}
	p := &pendingTokens{s: s, estimate: roughTokenCount(s), upper: tokenUpperBound(s), occurrences: 1}
	e.pending[id] = p
	e.slack += p.upper - p.estimate
	return p.estimate
}

// roughTokenCount is the four-bytes-per-token rule, a fair middle for
// prose and code.
func roughTokenCount(s string) int64 {
	return int64((len(s) + 3) / 4)
}

// tokenUpperBound bounds the tokens approxTokenCount can find in s. Every
// BPE token covers at least one byte of the text the tokenizer sees, which
// is s itself when s is valid UTF-8; otherwise each invalid byte reaches
// it as a three-byte replacement character.
func tokenUpperBound(s string) int64 {
	if utf8.ValidString(s) {
		return int64(len(s))
	}
	return 3 * int64(len(s))
}

// warmTokenCount counts s with the tokenizer so a later estimate finds
// the count cached. A tool result is warmed as it arrives, while the
// rest of its batch is still running, so the step that sends it seldom
// has to count it itself.
func warmTokenCount(s string) {
	if len(s) < tokenCacheMinBytes {
		return
	}
	if _, ok := cachedTokenCount(s); ok {
		return
	}
	approxTokenCount(s)
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
	storeTokenCount(key, n)
	return n
}

// cachedTokenCount returns the count approxTokenCount cached for s, if
// it has one, without ever running the tokenizer.
func cachedTokenCount(s string) (int64, bool) {
	if len(s) < tokenCacheMinBytes {
		return 0, false
	}
	key := maphash.String(tokenCacheSeed, s)
	tokenCountCache.Lock()
	defer tokenCountCache.Unlock()
	n, ok := tokenCountCache.counts[key]
	return n, ok
}

func storeTokenCount(key uint64, n int64) {
	tokenCountCache.Lock()
	if len(tokenCountCache.counts) >= tokenCacheMaxEntries {
		tokenCountCache.counts = map[uint64]int64{}
	}
	tokenCountCache.counts[key] = n
	tokenCountCache.Unlock()
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
