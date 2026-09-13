package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	url := modelsDevURL() + "/api.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("could not create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read response from %s: %w", url, err)
	}

	var catalog modelsDev
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, fmt.Errorf("failed to decode models.dev catalog: %w", err)
	}
	if len(catalog) == 0 {
		return nil, fmt.Errorf("models.dev catalog is empty")
	}
	return catalog, nil
}

// translateModelsDev converts the models.dev catalog into harness
// providers. Every models.dev entry that carries enough information to
// be usable becomes a catalog provider: the protocol derives from the
// AI SDK package the entry targets, the endpoint from its
// OpenAI-compatible base URL, and the API key template from its
// declared environment variables. Entries without a mappable protocol
// or base URL are skipped; a user can always add such a provider by
// hand through their config.
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

// sortModels orders models largest-context-first so the default-model
// heuristic (which falls back to the first model) lands on a capable
// model and the model picker surfaces flagship models first.
func sortModels(models []Model) {
	slices.SortStableFunc(models, func(a, b Model) int {
		if a.ContextWindow != b.ContextWindow {
			if a.ContextWindow > b.ContextWindow {
				return -1
			}
			return 1
		}
		return 0
	})
}

// adoptModelsDevProvider derives a harness provider from an uncurated
// models.dev entry. The second return value reports whether the entry
// carries enough information to be usable.
func adoptModelsDevProvider(sourceID string, src modelsDevProvider) (Provider, bool) {
	name := cmpOr(src.Name, sourceID)

	// Map the AI SDK package the entry targets to a harness protocol
	// type where one exists.
	providerType, native := npmToType(src.NPM)
	if !native {
		// Everything else must speak the OpenAI-compatible wire format
		// and publish a base URL to be usable.
		if src.API == "" {
			return Provider{}, false
		}
		providerType = TypeOpenAICompat
	}

	p := Provider{
		ID:          InferenceProvider(sourceID),
		Name:        name,
		APIEndpoint: src.API,
		Type:        providerType,
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

// translateModelsDevModels translates a models.dev model map, skipping
// models the upstream catalog marks deprecated. The second return value
// counts skipped entries.
func translateModelsDevModels(src modelsDevProvider) ([]Model, int) {
	models := make([]Model, 0, len(src.Models))
	skipped := 0
	for _, m := range src.Models {
		if m.Status == "deprecated" {
			skipped++
			continue
		}
		models = append(models, translateModelsDevModel(m))
	}
	return models, skipped
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
