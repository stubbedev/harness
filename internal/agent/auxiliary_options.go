package agent

import (
	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

// auxiliaryProviderOptions is getProviderOptions for the calls that
// write titles and compaction summaries. They run at the model's weakest
// reasoning level whatever the user chose for coding: the output is
// short and mechanical, and the call sits on the critical path - a
// summary before the next turn can start, a title alongside the first
// answer - where a model that thinks for a minute is felt as latency.
func auxiliaryProviderOptions(model Model, providerCfg config.ProviderConfig) fantasy.ProviderOptions {
	if lowest := catalog.LowestReasoningLevel(model.CatalogCfg.ReasoningLevels); lowest != "" {
		model.ModelCfg.ReasoningEffort = lowest
	}
	return getProviderOptions(model, providerCfg)
}
