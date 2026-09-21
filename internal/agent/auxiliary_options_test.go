package agent

import (
	"testing"

	"charm.land/fantasy/providers/openaicompat"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

// Titles and summaries run at the weakest reasoning level the model has,
// whatever effort the user configured for coding.
func TestAuxiliaryProviderOptionsUseTheLowestEffort(t *testing.T) {
	t.Parallel()
	model := Model{
		CatalogCfg: catalog.Model{ID: "glm-5.3", CanReason: true, ReasoningLevels: []string{"low", "high", "max"}},
		ModelCfg:   config.SelectedModel{Provider: "zai-coding-plan", ReasoningEffort: "max"},
	}
	providerCfg := config.ProviderConfig{ID: string(catalog.InferenceProviderZAICoding), Type: openaicompat.Name}

	parsed, ok := auxiliaryProviderOptions(model, providerCfg)[openaicompat.Name].(*openaicompat.ProviderOptions)
	require.True(t, ok)
	require.NotNil(t, parsed.ReasoningEffort)
	require.Equal(t, "low", string(*parsed.ReasoningEffort))

	coding, ok := getProviderOptions(model, providerCfg)[openaicompat.Name].(*openaicompat.ProviderOptions)
	require.True(t, ok)
	require.Equal(t, "max", string(*coding.ReasoningEffort), "the coding call keeps the configured effort")
}
