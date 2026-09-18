package app

import (
	"fmt"
	"strings"

	xstrings "github.com/charmbracelet/x/exp/strings"
	"github.com/stubbedev/harness/internal/config"
)

// parseModelStr parses a model string into provider filter and model ID.
// Format: "model-name" or "provider/model-name" or "synthetic/moonshot/kimi-k2".
// This function only checks if the first component is a valid provider name; if not,
// it treats the entire string as a model ID (which may contain slashes).
func parseModelStr(providers map[string]config.ProviderConfig, modelStr string) (providerFilter, modelID string) {
	parts := strings.Split(modelStr, "/")
	if len(parts) == 1 {
		return "", parts[0]
	}
	// Check if the first part is a valid provider name
	if _, ok := providers[parts[0]]; ok {
		return parts[0], strings.Join(parts[1:], "/")
	}

	// First part is not a valid provider, treat entire string as model ID
	return "", modelStr
}

// ModelMatch is a resolved provider/model pair found in the provider
// catalog.
type ModelMatch struct {
	Provider string
	ModelID  string
}

// FindModels resolves large and small model strings against the
// configured providers. Format: "model-name", "provider/model-name" or
// "synthetic/moonshot/kimi-k2". The first component of a slashed string
// is only treated as a provider filter when it names a configured
// provider; otherwise the whole string is a model ID.
func FindModels(providers map[string]config.ProviderConfig, largeModel, smallModel string) ([]ModelMatch, []ModelMatch, error) {
	largeProviderFilter, largeModelID := parseModelStr(providers, largeModel)
	smallProviderFilter, smallModelID := parseModelStr(providers, smallModel)

	// Validate provider filters exist.
	for _, pf := range []struct {
		filter, label string
	}{
		{largeProviderFilter, "large"},
		{smallProviderFilter, "small"},
	} {
		if pf.filter != "" {
			if _, ok := providers[pf.filter]; !ok {
				return nil, nil, fmt.Errorf("%s model: provider %q not found in configuration. Use 'harness models' to list available models", pf.label, pf.filter)
			}
		}
	}

	// Find matching models in a single pass.
	var largeMatches, smallMatches []ModelMatch
	for name, provider := range providers {
		if provider.Disable {
			continue
		}
		for _, m := range provider.Models {
			if filter(largeModelID, largeProviderFilter, m.ID, name) {
				largeMatches = append(largeMatches, ModelMatch{Provider: name, ModelID: m.ID})
			}
			if filter(smallModelID, smallProviderFilter, m.ID, name) {
				smallMatches = append(smallMatches, ModelMatch{Provider: name, ModelID: m.ID})
			}
		}
	}

	return largeMatches, smallMatches, nil
}

func filter(modelFilter, providerFilter, model, provider string) bool {
	return modelFilter != "" && strings.EqualFold(model, modelFilter) &&
		(providerFilter == "" || strings.EqualFold(provider, providerFilter))
}

// ValidateModels ensures exactly one match exists and returns it.
func ValidateModels(matches []ModelMatch, modelID, label string) (ModelMatch, error) {
	switch {
	case len(matches) == 0:
		return ModelMatch{}, fmt.Errorf("%s model %q not found", label, modelID)
	case len(matches) > 1:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Provider
		}
		return ModelMatch{}, fmt.Errorf(
			"%s model: model %q found in multiple providers: %s. Please specify provider using 'provider/model' format",
			label,
			modelID,
			xstrings.EnglishJoin(names, true),
		)
	}
	return matches[0], nil
}
