package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

func TestRefuseUnresolvedLarge_FailsWhenConfiguredMissesCatalog(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {Provider: "openai", Model: "first-can-reason"},
		},
		LargeFallback: true,
		LargeConfigured: config.SelectedModel{
			Provider: "ghost",
			Model:    "missing",
		},
	}

	err := refuseUnresolvedLarge("", cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ghost/missing")
	require.Contains(t, strings.ToLower(err.Error()), "refusing")
}

func TestRefuseUnresolvedLarge_CLIOverrideWins(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		LargeFallback: true,
		LargeConfigured: config.SelectedModel{
			Provider: "ghost",
			Model:    "missing",
		},
	}

	require.NoError(t, refuseUnresolvedLarge("openai/gpt-4o", cfg))
}

func TestRefuseUnresolvedLarge_NoFallbackContinues(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {Provider: "openai", Model: "gpt-4o"},
		},
	}

	require.NoError(t, refuseUnresolvedLarge("", cfg))
}

func TestResolvedLargeLine_DefaultVerbosity(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {Provider: "openai", Model: "gpt-4o"},
		},
	}

	require.Equal(t, "harness run: openai/gpt-4o", resolvedLargeLine(cfg))
}

func TestResolvedLargeLine_ZeroValue(t *testing.T) {
	t.Parallel()

	require.Equal(t, "harness run: model unresolved", resolvedLargeLine(nil))
	require.Equal(t, "harness run: model unresolved", resolvedLargeLine(&config.Config{}))
	require.Equal(t, "harness run: model unresolved", resolvedLargeLine(&config.Config{
		Models: map[config.SelectedModelType]config.SelectedModel{},
	}))
	require.Equal(t, "harness run: model unresolved", resolvedLargeLine(&config.Config{
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {},
		},
	}))
}

func TestRunCmd_NoNewModelFlags(t *testing.T) {
	t.Parallel()

	require.NotNil(t, runCmd.Flags().Lookup("model"))
	require.NotNil(t, runCmd.Flags().Lookup("small-model"))
	require.Nil(t, runCmd.Flags().Lookup("strict-models"))
	require.Nil(t, runCmd.Flags().Lookup("fail-loud"))
	require.Nil(t, runCmd.Flags().Lookup("require-large"))
}
