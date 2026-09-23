package discover

import (
	"context"
	"net/http"

	"github.com/stubbedev/harness/internal/catalog"
)

func init() {
	RegisterEnricher("lmstudio", &lmstudioEnricher{})
}

// lmstudioModelsResponse mirrors the response from LM Studio's native
// GET /api/v1/models endpoint. The model array is returned under the
// "models" key (not "data" like the OpenAI-compatible endpoint). Only
// the fields we care about are decoded.
type lmstudioModelsResponse struct {
	Models []lmstudioModelEntry `json:"models"`
}

// lmstudioModelEntry is a single entry from /api/v1/models.
type lmstudioModelEntry struct {
	Key              string               `json:"key"`
	DisplayName      string               `json:"display_name"`
	MaxContextLength int64                `json:"max_context_length"`
	LoadedInstances  []lmstudioInstance   `json:"loaded_instances"`
	Capabilities     lmstudioCapabilities `json:"capabilities"`
}

// lmstudioCapabilities holds optional model capability flags from
// LM Studio's /api/v1/models endpoint.
type lmstudioCapabilities struct {
	Vision bool `json:"vision"`
}

// lmstudioInstance is a currently loaded model instance with its
// runtime config.
type lmstudioInstance struct {
	Config lmstudioInstanceConfig `json:"config"`
}

// lmstudioInstanceConfig holds per-instance runtime settings.
type lmstudioInstanceConfig struct {
	ContextLength int64 `json:"context_length"`
}

// lmstudioEnricher fetches model metadata from LM Studio's native
// /api/v1/models endpoint and populates context window, display name,
// and vision support on discovered models.
type lmstudioEnricher struct{}

func (e *lmstudioEnricher) EnrichModels(ctx context.Context, cfg Config, resolver Resolver, models []catalog.Model) []catalog.Model {
	modelsResp, ok := fetchJSON[lmstudioModelsResponse](ctx, http.MethodGet, stripV1Suffix(cfg.BaseURL), "/api/v1/models", cfg, resolver, nil)
	if !ok {
		return models
	}
	metaByKey := indexBy(modelsResp.Models, func(m lmstudioModelEntry) string { return m.Key })

	for i := range models {
		meta, ok := metaByKey[models[i].ID]
		if !ok {
			continue
		}

		// Context window: prefer loaded instance config, fall back
		// to the model-level max.
		if models[i].ContextWindow == 0 {
			if len(meta.LoadedInstances) > 0 && meta.LoadedInstances[0].Config.ContextLength > 0 {
				models[i].ContextWindow = meta.LoadedInstances[0].Config.ContextLength
			} else if meta.MaxContextLength > 0 {
				models[i].ContextWindow = meta.MaxContextLength
			}
		}

		// Display name if not already set by user.
		if models[i].Name == models[i].ID && meta.DisplayName != "" {
			models[i].Name = meta.DisplayName
		}

		// Vision support from capabilities, if not already set by user.
		if !models[i].SupportsImages {
			models[i].SupportsImages = meta.Capabilities.Vision
		}
	}

	return models
}
