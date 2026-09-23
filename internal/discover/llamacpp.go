package discover

import (
	"context"
	"net/http"

	"github.com/stubbedev/harness/internal/catalog"
)

func init() {
	RegisterEnricher("llamacpp", &llamacppEnricher{})
}

// llamacppModelsResponse mirrors the response from llama-server's
// GET /v1/models endpoint. Unlike the standard OpenAI listing,
// llama-server embeds a meta block with model architecture details.
type llamacppModelsResponse struct {
	Data []llamacppModelEntry `json:"data"`
}

// llamacppModelEntry is a single entry from /v1/models.
type llamacppModelEntry struct {
	ID   string       `json:"id"`
	Meta llamacppMeta `json:"meta"`
}

// llamacppMeta holds per-model architecture metadata exposed by
// llama-server in the /v1/models response.
type llamacppMeta struct {
	NCtx      int64 `json:"n_ctx"`
	NCtxTrain int64 `json:"n_ctx_train"`
	NParams   int64 `json:"n_params"`
	Size      int64 `json:"size"`
}

// llamacppEnricher fetches model metadata from llama-server's
// /v1/models endpoint and populates context window on discovered
// models. It prefers n_ctx (the configured runtime context) and
// falls back to n_ctx_train (the model's trained maximum).
type llamacppEnricher struct{}

func (e *llamacppEnricher) EnrichModels(ctx context.Context, cfg Config, resolver Resolver, models []catalog.Model) []catalog.Model {
	modelsResp, ok := fetchJSON[llamacppModelsResponse](ctx, http.MethodGet, cfg.BaseURL, "/v1/models", cfg, resolver, nil)
	if !ok {
		return models
	}
	byID := indexBy(modelsResp.Data, func(m llamacppModelEntry) string { return m.ID })

	for i := range models {
		entry, ok := byID[models[i].ID]
		if !ok {
			continue
		}
		meta := entry.Meta

		// Context window: prefer configured n_ctx, fall back to
		// the model's trained maximum.
		if models[i].ContextWindow == 0 {
			if meta.NCtx > 0 {
				models[i].ContextWindow = meta.NCtx
			} else if meta.NCtxTrain > 0 {
				models[i].ContextWindow = meta.NCtxTrain
			}
		}
	}

	return models
}
