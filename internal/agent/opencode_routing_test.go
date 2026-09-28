package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

func TestBuildProviderOpenCodeRouting(t *testing.T) {
	t.Parallel()

	for _, providerID := range []string{
		string(catalog.InferenceProviderOpenCodeZen),
		string(catalog.InferenceProviderOpenCodeGo),
	} {
		t.Run(providerID, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			providerCfg := config.ProviderConfig{
				ID:      providerID,
				BaseURL: "https://opencode.ai/zen/v1",
				Type:    catalog.TypeOpenAICompat,
				APIKey:  "$OPENCODE_API_KEY",
			}
			coord := newTestCoordinator(t, env, providerID, providerCfg)

			for _, modelID := range []string{"kimi-k3", "muse-spark-1.3-contributor-free", "grok-4.6", "gpt-5.6-luna"} {
				provider, err := coord.buildProvider(providerCfg, config.SelectedModel{
					Model:    modelID,
					Provider: providerID,
				}, false)
				require.NoError(t, err, modelID)
				require.NotNil(t, provider, modelID)
			}
		})
	}
}
