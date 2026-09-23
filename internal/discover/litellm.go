package discover

import (
	"context"
	"net/http"

	"github.com/stubbedev/harness/internal/catalog"
)

// litellmModelInfoResponse mirrors the response from LiteLLM's
// /model/info endpoint, which returns rich metadata including context
// windows, max tokens, and pricing.
type litellmModelInfoResponse struct {
	Data []litellmModelInfo `json:"data"`
}

// litellmModelInfo is a single entry from /model/info.
type litellmModelInfo struct {
	ModelName string           `json:"model_name"`
	ModelInfo litellmModelMeta `json:"model_info"`
}

// litellmModelMeta holds the metadata fields we care about from
// LiteLLM's model_info block.
type litellmModelMeta struct {
	MaxInputTokens     *int64   `json:"max_input_tokens"`
	MaxOutputTokens    *int64   `json:"max_output_tokens"`
	InputCostPerToken  *float64 `json:"input_cost_per_token"`
	OutputCostPerToken *float64 `json:"output_cost_per_token"`
	Mode               string   `json:"mode"`
}

func init() {
	RegisterEnricher("litellm", &litellmEnricher{})
}

// litellmEnricher fetches model metadata from LiteLLM's /model/info
// endpoint and populates context window, max tokens, and pricing on
// discovered models.
type litellmEnricher struct{}

func (e *litellmEnricher) EnrichModels(ctx context.Context, cfg Config, resolver Resolver, models []catalog.Model) []catalog.Model {
	infoResp, ok := fetchJSON[litellmModelInfoResponse](ctx, http.MethodGet, stripV1Suffix(cfg.BaseURL), "/model/info", cfg, resolver, nil)
	if !ok {
		return models
	}
	byName := indexBy(infoResp.Data, func(m litellmModelInfo) string { return m.ModelName })

	// Apply metadata to discovered models, preserving existing
	// non-zero values (user overrides win).
	for i := range models {
		entry, ok := byName[models[i].ID]
		if !ok {
			continue
		}
		meta := entry.ModelInfo
		if models[i].ContextWindow == 0 && meta.MaxInputTokens != nil {
			models[i].ContextWindow = *meta.MaxInputTokens
		}
		if models[i].DefaultMaxTokens == 0 && meta.MaxOutputTokens != nil {
			models[i].DefaultMaxTokens = *meta.MaxOutputTokens
		}
		if models[i].CostPer1MIn == 0 && meta.InputCostPerToken != nil {
			models[i].CostPer1MIn = *meta.InputCostPerToken * 1_000_000
		}
		if models[i].CostPer1MOut == 0 && meta.OutputCostPerToken != nil {
			models[i].CostPer1MOut = *meta.OutputCostPerToken * 1_000_000
		}
	}

	return models
}
