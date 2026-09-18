package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

const defaultModelsDevURL = "https://models.dev"

// modelsDevURL returns the models.dev base URL, overridable for tests
// and self-hosted mirrors.
func modelsDevURL() string {
	if u := envLookup("MODELS_DEV_URL"); u != "" {
		return u
	}
	return defaultModelsDevURL
}

// modelsDev is the shape of https://models.dev/api.json, keyed by
// provider id. Only the fields the translation consumes are declared.
type modelsDev map[string]modelsDevProvider

type modelsDevProvider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// API is the OpenAI-compatible base URL. Present on openai-compat
	// style providers; native providers leave it empty.
	API    string                    `json:"api"`
	Models map[string]modelsDevModel `json:"models"`
	NPM    string                    `json:"npm"`
	Env    []string                  `json:"env"`
	Doc    string                    `json:"doc"`
}

type modelsDevModel struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Family           string              `json:"family"`
	Description      string              `json:"description"`
	Attachment       bool                `json:"attachment"`
	Reasoning        bool                `json:"reasoning"`
	ReasoningOptions []modelsDevEffort   `json:"reasoning_options"`
	ToolCall         bool                `json:"tool_call"`
	Status           string              `json:"status"`
	Knowledge        string              `json:"knowledge"`
	ReleaseDate      string              `json:"release_date"`
	LastUpdated      string              `json:"last_updated"`
	OpenWeights      bool                `json:"open_weights"`
	Cost             modelsDevCost       `json:"cost"`
	Limit            modelsDevLimit      `json:"limit"`
	Modalities       modelsDevModalities `json:"modalities"`
}

type modelsDevEffort struct {
	Type   string   `json:"type"`
	Min    int64    `json:"min"`
	Values []string `json:"values"`
}

type modelsDevCost struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CacheRead   float64 `json:"cache_read"`
	CacheWrite  float64 `json:"cache_write"`
	Reasoning   float64 `json:"reasoning"`
	InputAudio  float64 `json:"input_audio"`
	OutputAudio float64 `json:"output_audio"`
}

type modelsDevLimit struct {
	Context int64 `json:"context"`
	Input   int64 `json:"input"`
	Output  int64 `json:"output"`
}

type modelsDevModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// fetchModelsDev downloads and decodes the models.dev catalog.
func fetchModelsDev(ctx context.Context, client *http.Client) (modelsDev, error) {
	var catalog modelsDev
	if err := FetchJSON(ctx, client, modelsDevURL()+"/api.json", &catalog); err != nil {
		return nil, err
	}
	if len(catalog) == 0 {
		return nil, fmt.Errorf("models.dev catalog is empty")
	}
	return catalog, nil
}

// translateModelsDev converts the models.dev catalog into harness
// providers. Every models.dev entry that carries enough information to
// be usable becomes a catalog provider: the protocol derives from the
// AI SDK package the entry targets or from the known-provider table,
// the endpoint from its OpenAI-compatible base URL (or, again, the
// table), and the API key template from its declared environment
// variables. Entries without a mappable protocol or base URL are
// skipped; a user can always add such a provider by hand through their
// config.
func translateModelsDev(md modelsDev) []Provider {
	providers := make([]Provider, 0, len(md))
	for sourceID, src := range md {
		if len(src.Models) == 0 {
			continue
		}
		adopted, ok := adoptModelsDevProvider(sourceID, src)
		if !ok {
			continue
		}
		models, _ := translateModelsDevModels(src)
		if len(models) == 0 {
			continue
		}
		sortModels(models)
		adopted.Models = models
		setDefaultModels(&adopted)
		providers = append(providers, adopted)
	}

	// models.dev is a JSON object, so iteration order is random; sort
	// the catalog by ID for deterministic output.
	slices.SortStableFunc(providers, func(a, b Provider) int {
		return strings.Compare(string(a.ID), string(b.ID))
	})
	return providers
}

// ParseProviders decodes a provider catalog from raw JSON. It accepts
// both shapes harness understands: the models.dev api.json document
// (an object keyed by provider id) and a plain array of providers, as
// written by hand-maintained files. The models.dev shape is translated
// through the same adoption rules as the live fetch; the array shape
// is used verbatim.
func ParseProviders(data []byte) ([]Provider, error) {
	trimmed := strings.TrimLeft(string(data), " \t\r\n")
	if strings.HasPrefix(trimmed, "[") {
		var providers []Provider
		if err := json.Unmarshal(data, &providers); err != nil {
			return nil, fmt.Errorf("failed to decode provider list: %w", err)
		}
		if len(providers) == 0 {
			return nil, fmt.Errorf("no providers found in the provided source")
		}
		return providers, nil
	}

	var md modelsDev
	if err := json.Unmarshal(data, &md); err != nil {
		return nil, fmt.Errorf("failed to decode provider data: %w", err)
	}
	if len(md) == 0 {
		return nil, fmt.Errorf("no providers found in the provided source")
	}
	return translateModelsDev(md), nil
}

// sortModels orders a provider's models most-capable-first, which is
// both what the model picker should show first and where the default
// large model is taken from. Capability is not published, so it is
// approximated: the widest context window, then the highest price
// (vendors charge for their flagship), then the most recent release.
// models.dev is a JSON object, so the id breaks the last tie to keep
// the order stable across fetches.
func sortModels(models []Model) {
	slices.SortStableFunc(models, func(a, b Model) int {
		if a.ContextWindow != b.ContextWindow {
			return descend(a.ContextWindow, b.ContextWindow)
		}
		if a.CostPer1MOut != b.CostPer1MOut {
			return descend(a.CostPer1MOut, b.CostPer1MOut)
		}
		if a.CostPer1MIn != b.CostPer1MIn {
			return descend(a.CostPer1MIn, b.CostPer1MIn)
		}
		if a.ReleaseDate != b.ReleaseDate {
			return strings.Compare(b.ReleaseDate, a.ReleaseDate)
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// descend orders two values largest-first.
func descend[T int64 | float64](a, b T) int {
	if a > b {
		return -1
	}
	return 1
}

// setDefaultModels picks the pair a provider opens with when the user
// has not chosen one. models.dev publishes no such recommendation, so
// they are derived: the large model is the most capable one on offer
// (the head of the list sortModels produced), and the small model --
// which runs titles, summaries and other cheap internal calls -- is the
// cheapest model that is still a general-purpose chat model. Cost is
// compared output-first because output tokens dominate those calls.
//
// A provider whose models are all free (a local runtime, a flat-rate
// plan) has no cost signal to rank by, so the small model falls back to
// the narrowest-context model -- the tail of the sorted list, and the
// cheapest to run.
func setDefaultModels(p *Provider) {
	if len(p.Models) == 0 {
		return
	}
	// Models are sorted most-capable-first, so the large model is the
	// head of the list.
	p.DefaultLargeModelID = p.Models[0].ID

	smallest := len(p.Models) - 1
	small := -1
	for i, m := range p.Models {
		if m.CostPer1MIn == 0 && m.CostPer1MOut == 0 {
			continue
		}
		if small < 0 || cheaper(m, p.Models[small]) {
			small = i
		}
	}
	if small < 0 {
		small = smallest
	}
	p.DefaultSmallModelID = p.Models[small].ID
}

// cheaper reports whether a costs less to run than b, comparing output
// price first and falling back to the id so the choice is stable.
func cheaper(a, b Model) bool {
	if a.CostPer1MOut != b.CostPer1MOut {
		return a.CostPer1MOut < b.CostPer1MOut
	}
	if a.CostPer1MIn != b.CostPer1MIn {
		return a.CostPer1MIn < b.CostPer1MIn
	}
	return a.ID < b.ID
}

// adoptModelsDevProvider derives a harness provider from an uncurated
// models.dev entry, filled in from the known-provider table where
// models.dev is silent. The second return value reports whether the
// entry carries enough information to be usable.
func adoptModelsDevProvider(sourceID string, src modelsDevProvider) (Provider, bool) {
	id := InferenceProvider(sourceID)
	known := knownProviders[id]

	// The protocol comes from the known-provider table first, then from
	// the AI SDK package the entry targets.
	providerType := known.Type
	if providerType == "" {
		var native bool
		if providerType, native = npmToType(src.NPM); !native {
			providerType = TypeOpenAICompat
		}
	}

	// An OpenAI-compatible provider is only usable with a base URL.
	// models.dev publishes one for most of them; the table covers the
	// entries that name a vendor SDK instead, and native protocols
	// carry the endpoint in their own client.
	endpoint := cmpOr(src.API, known.Endpoint)
	if endpoint == "" && providerType == TypeOpenAICompat {
		return Provider{}, false
	}

	p := Provider{
		ID:             id,
		Name:           cmpOr(src.Name, sourceID),
		APIEndpoint:    endpoint,
		Type:           providerType,
		DefaultHeaders: knownHeaders(id),
	}
	if len(src.Env) > 0 && src.Env[0] != "" {
		p.APIKey = "$" + src.Env[0]
	}
	return p, true
}

// npmToType maps the models.dev npm field to a harness provider type
// for the protocols harness speaks natively.
func npmToType(npm string) (Type, bool) {
	switch npm {
	case "@ai-sdk/anthropic":
		return TypeAnthropic, true
	case "@ai-sdk/openai":
		return TypeOpenAI, true
	case "@ai-sdk/google":
		return TypeGoogle, true
	case "@ai-sdk/google-vertex", "@ai-sdk/google-vertex/anthropic":
		return TypeVertexAI, true
	case "@ai-sdk/azure":
		return TypeAzure, true
	case "@ai-sdk/amazon-bedrock":
		return TypeBedrock, true
	case "@ai-sdk/vercel":
		return TypeVercel, true
	case "@openrouter/ai-sdk-provider":
		return TypeOpenRouter, true
	default:
		return "", false
	}
}

// translateModelsDevModels translates a models.dev model map, keeping
// only the models harness can actually hold a conversation with. The
// second return value counts skipped entries.
func translateModelsDevModels(src modelsDevProvider) ([]Model, int) {
	models := make([]Model, 0, len(src.Models))
	skipped := 0
	for _, m := range src.Models {
		if !usableModel(m) {
			skipped++
			continue
		}
		models = append(models, translateModelsDevModel(m))
	}
	return models, skipped
}

// usableModel reports whether a models.dev entry is a model the agent
// loop can drive. models.dev catalogues everything a provider serves,
// embeddings, image generators, speech and moderation endpoints
// included; harness drives a chat model that can call tools, so
// anything that cannot take text in, cannot write text back, or cannot
// call a tool would only ever fail at request time if offered.
func usableModel(m modelsDevModel) bool {
	if m.Status == "deprecated" || !m.ToolCall {
		return false
	}
	return acceptsModality(m.Modalities.Input, "text") &&
		acceptsModality(m.Modalities.Output, "text")
}

// acceptsModality reports whether a declared modality list contains
// want. An empty list is treated as text-only: a handful of entries
// omit the field, and every one of them is a chat model.
func acceptsModality(modalities []string, want string) bool {
	if len(modalities) == 0 {
		return want == "text"
	}
	return slices.Contains(modalities, want)
}

func translateModelsDevModel(m modelsDevModel) Model {
	model := Model{
		ID:                 m.ID,
		Name:               cmpOr(m.Name, m.ID),
		CostPer1MIn:        m.Cost.Input,
		CostPer1MOut:       m.Cost.Output,
		CostPer1MInCached:  m.Cost.CacheWrite,
		CostPer1MOutCached: m.Cost.CacheRead,
		ContextWindow:      m.Limit.Context,
		ReleaseDate:        m.ReleaseDate,
		DefaultMaxTokens:   cmpI64(m.Limit.Output, 4096),
		CanReason:          m.Reasoning,
	}

	for _, opt := range m.ReasoningOptions {
		if opt.Type == "effort" && len(opt.Values) > 0 {
			model.ReasoningLevels = opt.Values
			model.DefaultReasoningEffort = defaultEffort(opt.Values)
			break
		}
	}

	for _, in := range m.Modalities.Input {
		if in == "image" || in == "pdf" {
			model.SupportsImages = true
			break
		}
	}
	if m.Attachment {
		model.SupportsImages = true
	}
	return model
}

// defaultEffort picks the default reasoning effort from a level list,
// preferring "medium" when present.
func defaultEffort(levels []string) string {
	for _, l := range levels {
		if l == "medium" {
			return l
		}
	}
	if len(levels) > 0 {
		return levels[0]
	}
	return ""
}

// httpClient returns the shared HTTP client for catalog fetches.
func httpClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func cmpI64(a, fallback int64) int64 {
	if a != 0 {
		return a
	}
	return fallback
}
