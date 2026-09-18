package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// envLookup reads an environment variable. It is a tiny indirection so
// tests can override lookups without mutating process state.
var envLookup = os.Getenv

// FetchCatalog fetches a fresh catalog from the live sources:
// models.dev for the general catalog plus OpenRouter's first-party
// model API for the openrouter provider entry. A partial failure
// degrades gracefully: when the OpenRouter API cannot be reached, its
// entry keeps the model list models.dev published for it.
func FetchCatalog(ctx context.Context, client *http.Client) ([]Provider, error) {
	if client == nil {
		client = httpClient()
	}

	md, err := fetchModelsDev(ctx, client)
	if err != nil {
		return nil, err
	}

	providers := translateModelsDev(md)

	// Refresh the openrouter entry from OpenRouter's own API, which
	// carries reasoning effort metadata models.dev lacks. A failure
	// keeps the models.dev-derived list.
	for i, p := range providers {
		if p.ID != InferenceProviderOpenRouter {
			continue
		}
		models, oerr := fetchOpenRouterModels(ctx, client)
		if oerr != nil {
			slog.Warn("Could not fetch OpenRouter model list", "error", oerr)
			continue
		}
		providers[i].Models = models
		setDefaultModels(&providers[i])
	}

	return providers, nil
}

// openRouterModels is the shape of
// https://openrouter.ai/api/v1/models.
type openRouterModels struct {
	Data []openRouterModel `json:"data"`
}

type openRouterModel struct {
	ID                  string               `json:"id"`
	Name                string               `json:"name"`
	Created             int64                `json:"created"`
	ContextLength       int64                `json:"context_length"`
	Description         string               `json:"description"`
	Pricing             openRouterPricing    `json:"pricing"`
	Reasoning           *openRouterReasoning `json:"reasoning"`
	Architecture        openRouterArch       `json:"architecture"`
	SupportedParameters []string             `json:"supported_parameters"`
	TopProvider         openRouterTop        `json:"top_provider"`
}

type openRouterPricing struct {
	Prompt          string `json:"prompt"`
	Completion      string `json:"completion"`
	InputCacheRead  string `json:"input_cache_read"`
	InputCacheWrite string `json:"input_cache_write"`
}

type openRouterReasoning struct {
	DefaultEffort    string   `json:"default_effort"`
	SupportedEfforts []string `json:"supported_efforts"`
}

type openRouterArch struct {
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

type openRouterTop struct {
	MaxCompletionTokens int64 `json:"max_completion_tokens"`
}

const openRouterModelsURL = "https://openrouter.ai/api/v1/models"

// fetchOpenRouterModels downloads and translates OpenRouter's model
// list into catalog models.
func fetchOpenRouterModels(ctx context.Context, client *http.Client) ([]Model, error) {
	url := openRouterModelsURL
	if base := envLookup("OPENROUTER_MODELS_URL"); base != "" {
		url = base
	}
	var parsed openRouterModels
	if err := FetchJSON(ctx, client, url, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("OpenRouter model list is empty")
	}

	models := make([]Model, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		// OpenRouter lists everything it routes, including models that
		// cannot call tools. Harness drives tool calls on every turn,
		// so those entries would only fail at request time.
		if !slices.Contains(m.SupportedParameters, "tools") {
			continue
		}
		model := Model{
			ID:                 m.ID,
			Name:               strings.TrimPrefix(cmpOr(m.Name, m.ID), "OpenRouter: "),
			CostPer1MIn:        openRouterPrice(m.Pricing.Prompt),
			CostPer1MOut:       openRouterPrice(m.Pricing.Completion),
			CostPer1MInCached:  openRouterPrice(m.Pricing.InputCacheWrite),
			CostPer1MOutCached: openRouterPrice(m.Pricing.InputCacheRead),
			ContextWindow:      m.ContextLength,
			ReleaseDate:        openRouterReleaseDate(m.Created),
			DefaultMaxTokens:   cmpI64(m.TopProvider.MaxCompletionTokens, 4096),
		}
		if m.Reasoning != nil && len(m.Reasoning.SupportedEfforts) > 0 {
			model.CanReason = true
			model.ReasoningLevels = m.Reasoning.SupportedEfforts
			model.DefaultReasoningEffort = cmpOr(m.Reasoning.DefaultEffort, defaultEffort(m.Reasoning.SupportedEfforts))
		}
		for _, in := range m.Architecture.InputModalities {
			if in == "image" || in == "file" {
				model.SupportsImages = true
				break
			}
		}
		models = append(models, model)
	}

	if len(models) == 0 {
		return nil, fmt.Errorf("OpenRouter listed no tool-capable models")
	}
	sortModels(models)
	return models, nil
}

// openRouterReleaseDate renders OpenRouter's creation timestamp in the
// same YYYY-MM-DD shape models.dev publishes, so both sources sort the
// same way.
func openRouterReleaseDate(created int64) string {
	if created <= 0 {
		return ""
	}
	return time.Unix(created, 0).UTC().Format(time.DateOnly)
}

// openRouterPrice converts OpenRouter's per-token price string into a
// per-1M-token float. Unparsable or negative prices become 0.
func openRouterPrice(perToken string) float64 {
	if perToken == "" {
		return 0
	}
	v, err := strconv.ParseFloat(perToken, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v * 1_000_000
}
