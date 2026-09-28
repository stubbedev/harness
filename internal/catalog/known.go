package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
)

// Harness identifies itself to the gateways that attribute traffic by
// referer and title. The values are cosmetic to the request but show up
// in the provider's dashboards, so they name this fork rather than the
// upstream project it came from.
const (
	harnessReferer = "https://github.com/stubbedev/harness"
	harnessTitle   = "Harness"
)

// gatewayAttribution is the header pair the OpenRouter-style gateways
// read. Never handed out directly; adopters take a clone.
var gatewayAttribution = map[string]string{
	"HTTP-Referer": harnessReferer,
	"X-Title":      harnessTitle,
}

// knownProvider carries the facts models.dev does not publish about a
// provider harness can talk to:
//
//   - Type when the entry names an AI SDK package harness does not map
//     to one of its own protocols (models.dev describes providers for
//     the Vercel AI SDK, which has a package per vendor; harness only
//     speaks a handful of wire formats).
//   - Endpoint when the entry has no OpenAI-compatible base URL of its
//     own. Native-protocol providers get a "$VAR" template so the
//     endpoint stays overridable from the environment the way it was
//     before the catalog moved off catwalk; the rest get the documented
//     base URL, without which the entry would be unusable and skipped.
//   - Headers the service expects on every request.
//
// This is the whole hand-maintained surface of the catalog. Everything
// else -- which providers exist, which models they serve, context
// windows, prices, reasoning levels -- comes from models.dev.
type knownProvider struct {
	Type     Type
	Endpoint string
	Headers  map[string]string
}

var knownProviders = map[InferenceProvider]knownProvider{
	// Native protocols. models.dev publishes no base URL for these
	// because the SDK knows it; the template keeps the escape hatch for
	// proxies and regional endpoints.
	InferenceProviderAnthropic: {Type: TypeAnthropic, Endpoint: "$ANTHROPIC_API_ENDPOINT"},
	InferenceProviderOpenAI:    {Type: TypeOpenAI, Endpoint: "$OPENAI_API_ENDPOINT"},
	// GEMINI_API_ENDPOINT is what this variable was called while the
	// provider id was "gemini"; it still works as a fallback.
	InferenceProviderGemini:   {Type: TypeGoogle, Endpoint: "${GOOGLE_API_ENDPOINT:-$GEMINI_API_ENDPOINT}"},
	InferenceProviderAzure:    {Type: TypeAzure, Endpoint: "$AZURE_OPENAI_API_ENDPOINT"},
	InferenceProviderBedrock:  {Type: TypeBedrock},
	InferenceProviderVertexAI: {Type: TypeVertexAI},

	// Gateways that attribute traffic by referer.
	InferenceProviderOpenRouter:  {Type: TypeOpenRouter, Headers: gatewayAttribution},
	InferenceProviderVercel:      {Type: TypeVercel, Endpoint: "https://ai-gateway.vercel.sh/v1", Headers: gatewayAttribution},
	InferenceProviderHuggingFace: {Headers: gatewayAttribution},

	// OpenAI-compatible services whose models.dev entry names a
	// vendor-specific SDK package and therefore carries no base URL.
	InferenceProviderCerebras: {
		Type:     TypeOpenAICompat,
		Endpoint: "https://api.cerebras.ai/v1",
		Headers:  map[string]string{"X-Cerebras-3rd-Party-Integration": "harness"},
	},
	InferenceProviderGROQ:       {Type: TypeOpenAICompat, Endpoint: "https://api.groq.com/openai/v1"},
	InferenceProviderXAI:        {Type: TypeOpenAICompat, Endpoint: "https://api.x.ai/v1"},
	InferenceProviderMistral:    {Type: TypeOpenAICompat, Endpoint: "https://api.mistral.ai/v1"},
	InferenceProviderCohere:     {Type: TypeOpenAICompat, Endpoint: "https://api.cohere.ai/compatibility/v1"},
	InferenceProviderPerplexity: {Type: TypeOpenAICompat, Endpoint: "https://api.perplexity.ai"},
	InferenceProviderTogetherAI: {Type: TypeOpenAICompat, Endpoint: "https://api.together.xyz/v1"},
	InferenceProviderDeepInfra:  {Type: TypeOpenAICompat, Endpoint: "https://api.deepinfra.com/v1/openai"},
	InferenceProviderVenice:     {Type: TypeOpenAICompat, Endpoint: "https://api.venice.ai/api/v1"},
	InferenceAIHubMix:           {Type: TypeOpenAICompat, Endpoint: "https://aihubmix.com/v1"},
}

// legacyProviderIDs maps the provider ids catwalk used to the models.dev
// ids that replaced them. A user config written against the old catalog
// names providers that no longer exist, and a provider entry that
// matches nothing is dropped with a warning rather than applied, so the
// rename is migrated at load time instead of silently losing the
// provider.
//
// "zai" is deliberately absent: it exists in both catalogs but means
// different things. catwalk's "zai" was the GLM coding subscription;
// models.dev's is the pay-per-token API on the same host, and the
// coding plan is a separate entry. Rewriting it would send a
// pay-per-token key to the wrong endpoint, so the change is reported by
// ProviderMeaningChanged instead.
var legacyProviderIDs = map[InferenceProvider]InferenceProvider{
	"gemini":            InferenceProviderGemini,
	"copilot":           InferenceProviderCopilot,
	"bedrock":           InferenceProviderBedrock,
	"bedrock-europe":    InferenceProviderBedrock,
	"vertexai":          InferenceProviderVertexAI,
	"opencode-zen":      InferenceProviderOpenCodeZen,
	"zhipu":             InferenceProviderZhipu,
	"zhipu-coding":      InferenceProviderZhipuCoding,
	"moonshot":          InferenceProviderMoonshot,
	"fireworks":         InferenceProviderFireworks,
	"minimax-china":     InferenceProviderMiniMaxChina,
	"alibaba-singapore": InferenceProviderAlibabaSingapore,
	"kimi-coding":       InferenceKimiCoding,
	"ionet":             InferenceProviderIoNet,
}

// meaningChanged documents the provider ids that survived the move off
// catwalk with a different meaning, keyed by id.
var meaningChanged = map[InferenceProvider]string{
	InferenceProviderZAI: `"zai" is now Z.AI's pay-per-token API (api.z.ai/api/paas/v4); ` +
		`the GLM coding subscription catwalk called "zai" is the separate provider "zai-coding-plan"`,
}

// LegacyProviderID returns the current id for a provider id that was
// renamed when the catalog moved off catwalk.
func LegacyProviderID(id string) (string, bool) {
	current, ok := legacyProviderIDs[InferenceProvider(id)]
	return string(current), ok
}

// ProviderMeaningChanged returns an explanation when a provider id
// still exists but no longer refers to what it used to.
func ProviderMeaningChanged(id string) (string, bool) {
	note, ok := meaningChanged[InferenceProvider(id)]
	return note, ok
}

// IsZAI reports whether id is one of Z.AI's GLM endpoints. The
// pay-per-token API and the coding subscription are the same service
// behind two base URLs and share its request quirks, so the
// GLM-specific request fields apply to both.
func IsZAI(id string) bool {
	return id == string(InferenceProviderZAI) || id == string(InferenceProviderZAICoding)
}

// IsAlibabaDashScope reports whether id is one of Alibaba's DashScope
// gateways. They share a set of request quirks and models.dev lists one
// entry per region and plan ("alibaba", "alibaba-cn",
// "alibaba-coding-plan", ...), so the family is matched by prefix
// rather than enumerated.
func IsAlibabaDashScope(id string) bool {
	return id == string(InferenceProviderAlibabaSingapore) ||
		strings.HasPrefix(id, string(InferenceProviderAlibabaSingapore)+"-")
}

// knownHeaders returns a copy of the headers a known provider expects,
// or nil when it expects none.
func knownHeaders(id InferenceProvider) map[string]string {
	known, ok := knownProviders[id]
	if !ok || len(known.Headers) == 0 {
		return nil
	}
	return maps.Clone(known.Headers)
}

// Some gateways serve one OpenAI-compatible endpoint but route certain
// models to a different wire protocol. Which models is a fact about the
// gateway, not about the model, and models.dev does not publish it.

// copilotResponsesModels are the Copilot models served by the OpenAI
// Responses API instead of Chat Completions.
var copilotResponsesModels = map[string]bool{
	"gpt-5.2":       true,
	"gpt-5.2-codex": true,
	"gpt-5.3-codex": true,
	"gpt-5.4":       true,
	"gpt-5.4-mini":  true,
	"gpt-5.5":       true,
	"gpt-5-mini":    true,
	"gpt-5.6-luna":  true,
	"gpt-5.6-terra": true,
	"gpt-5.6-sol":   true,
	"gpt-6-astra":   true,
	"grok-4.5":      true,
	"grok-4.6":      true,
}

// ResponsesAPIRouter returns the predicate naming the models provider
// serves over the OpenAI Responses API instead of Chat Completions, or nil
// when it routes no model there. See https://opencode.ai/docs/zen and
// https://opencode.ai/docs/go for OpenCode's split.
func ResponsesAPIRouter(provider InferenceProvider) func(modelID string) bool {
	switch provider {
	case InferenceProviderCopilot:
		return func(modelID string) bool { return copilotResponsesModels[modelID] }
	case InferenceProviderOpenCodeGo, InferenceProviderOpenCodeZen:
		return func(modelID string) bool {
			return strings.HasPrefix(modelID, "gpt-") ||
				strings.HasPrefix(modelID, "grok-") ||
				strings.HasPrefix(modelID, "muse-spark-")
		}
	default:
		return nil
	}
}

// UsesMessagesAPI reports whether provider serves modelID over the
// Anthropic Messages API instead of Chat Completions.
func UsesMessagesAPI(provider InferenceProvider, modelID string) bool {
	switch provider {
	case InferenceProviderOpenCodeGo:
		return strings.HasPrefix(modelID, "minimax-") ||
			strings.HasPrefix(modelID, "qwen3.6-") ||
			strings.HasPrefix(modelID, "qwen3.7-") ||
			strings.HasPrefix(modelID, "qwen3.8-")
	case InferenceProviderOpenCodeZen:
		return strings.HasPrefix(modelID, "claude-") ||
			strings.HasPrefix(modelID, "qwen3.5-") ||
			strings.HasPrefix(modelID, "qwen3.6-") ||
			strings.HasPrefix(modelID, "qwen3.7-") ||
			strings.HasPrefix(modelID, "qwen3.8-")
	default:
		return false
	}
}

// translationRevision is bumped whenever the models.dev translation itself
// changes in a way the known-provider table does not capture.
const translationRevision = 1

// TranslationVersion identifies the translation this build applies to
// models.dev: the translator's revision and the known-provider table. A
// catalog cached by a build with another version was translated by other
// rules (another endpoint, header or protocol), so it is only a stale
// answer until this build has fetched and translated its own.
var TranslationVersion = sync.OnceValue(func() string {
	table, err := json.Marshal(knownProviders)
	if err != nil {
		panic(fmt.Sprintf("catalog: known-provider table does not encode: %v", err))
	}
	sum := sha256.Sum256(append([]byte(strconv.Itoa(translationRevision)+"\n"), table...))
	return hex.EncodeToString(sum[:8])
})
