package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/ui/util"
)

func formCatalog() []catalog.Provider {
	return []catalog.Provider{
		{ID: "anthropic", Name: "Anthropic", Type: catalog.TypeAnthropic, APIKey: "$ANTHROPIC_API_KEY", APIEndpoint: "$ANTHROPIC_API_ENDPOINT"},
		{ID: "azure", Name: "Azure", Type: catalog.TypeAzure, APIEndpoint: "$AZURE_OPENAI_API_ENDPOINT"},
		{ID: "google-vertex", Name: "Google Vertex", Type: catalog.TypeVertexAI},
		{ID: "amazon-bedrock", Name: "Bedrock", Type: catalog.TypeBedrock, APIKey: "$AWS_ACCESS_KEY_ID"},
	}
}

func fieldLabels(fields []providerField) []string {
	labels := make([]string, len(fields))
	for i, f := range fields {
		labels[i] = f.label
	}
	return labels
}

func formInputs(target ProviderTarget, overrides map[string]string) []string {
	fields := target.fields()
	inputs := make([]string, len(fields))
	for i, f := range fields {
		inputs[i] = target.display(f)
		if v, ok := overrides[f.label]; ok {
			inputs[i] = v
		}
	}
	return inputs
}

func TestProviderFieldsPerType(t *testing.T) {
	t.Parallel()

	cases := map[catalog.InferenceProvider][]string{
		"anthropic":      {"API key", "Base URL", "Extra headers"},
		"azure":          {"Endpoint", "API key", "API version", "Extra headers"},
		"google-vertex":  {"Project", "Location", "Extra headers"},
		"amazon-bedrock": {"API key", "Region", "Profile", "Extra headers"},
		"":               {"ID", "Name", "Type", "Base URL", "API key", "Models", "Extra headers"},
	}
	for id, want := range cases {
		target := NewProviderTarget(nil, formCatalog(), id, config.ProviderConfig{})
		require.Equal(t, want, fieldLabels(target.fields()), id)
	}
}

func TestProviderFirstTimeSetupPerType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		id     catalog.InferenceProvider
		inputs map[string]string
		want   map[config.ProviderField]any
	}{
		{"anthropic", map[string]string{"API key": "sk-ant-new"}, map[config.ProviderField]any{config.ProviderFieldAPIKey: "sk-ant-new"}},
		{"azure", map[string]string{"Endpoint": "https://r.openai.azure.com", "API key": "k", "API version": "2024-10-21"}, map[config.ProviderField]any{
			config.ProviderFieldBaseURL: "https://r.openai.azure.com", config.ProviderFieldAPIKey: "k", config.ProviderFieldAPIVersion: "2024-10-21",
		}},
		{"google-vertex", map[string]string{"Project": "proj", "Location": "us-east5"}, map[config.ProviderField]any{
			config.ProviderFieldProject: "proj", config.ProviderFieldLocation: "us-east5",
		}},
		{"amazon-bedrock", map[string]string{"Region": "eu-west-1", "Profile": "work"}, map[config.ProviderField]any{
			config.ProviderFieldRegion: "eu-west-1", config.ProviderFieldProfile: "work",
		}},
	}
	for _, tc := range cases {
		target := NewProviderTarget(nil, formCatalog(), tc.id, config.ProviderConfig{})
		id, update, err := target.buildUpdate(target.fields(), formInputs(target, tc.inputs))
		require.NoError(t, err, tc.id)
		require.Equal(t, tc.id, id)
		require.Equal(t, tc.want, update.Set, tc.id)
		require.Empty(t, update.Unset, tc.id)
	}
}

func TestProviderCustomSetup(t *testing.T) {
	t.Parallel()

	cfg := configuredTestConfig(t, "taken")
	target := NewProviderTarget(cfg, formCatalog(), "", config.ProviderConfig{})
	inputs := formInputs(target, map[string]string{
		"ID": "work", "Name": "Work", "Type": "anthropic", "Base URL": "https://llm.example.com/v1",
		"API key": "$(op read op://work/key)", "Models": "big, small",
	})
	id, update, err := target.buildUpdate(target.fields(), inputs)
	require.NoError(t, err)
	require.Equal(t, catalog.InferenceProvider("work"), id)
	require.Equal(t, map[config.ProviderField]any{
		config.ProviderFieldName:    "Work",
		config.ProviderFieldType:    "anthropic",
		config.ProviderFieldBaseURL: "https://llm.example.com/v1",
		config.ProviderFieldAPIKey:  "$(op read op://work/key)",
		config.ProviderFieldModels:  []catalog.Model{{ID: "big", Name: "big"}, {ID: "small", Name: "small"}},
	}, update.Set)

	_, _, err = target.buildUpdate(target.fields(), formInputs(target, map[string]string{"ID": "taken", "Type": "openai-compat", "Base URL": "https://x"}))
	require.ErrorContains(t, err, "already exists")
	_, _, err = target.buildUpdate(target.fields(), formInputs(target, map[string]string{"ID": "anthropic", "Type": "openai-compat", "Base URL": "https://x"}))
	require.ErrorContains(t, err, "already exists")
	_, _, err = target.buildUpdate(target.fields(), formInputs(target, map[string]string{"ID": "x", "Type": "azure", "Base URL": "https://x"}))
	require.ErrorContains(t, err, "is not one of")
	_, _, err = target.buildUpdate(target.fields(), formInputs(target, map[string]string{"ID": "x", "Type": "openai-compat"}))
	require.ErrorContains(t, err, "Base URL is required")
}

func TestProviderCustomDiscoveryLeavesModelsUnset(t *testing.T) {
	t.Parallel()

	target := NewProviderTarget(nil, formCatalog(), "", config.ProviderConfig{})
	_, update, err := target.buildUpdate(target.fields(), formInputs(target, map[string]string{"ID": "local", "Type": "openai-compat", "Base URL": "http://localhost:11434/v1"}))
	require.NoError(t, err)
	require.NotContains(t, update.Set, config.ProviderFieldModels)
}

func editedAnthropic() ProviderTarget {
	return NewProviderTarget(nil, formCatalog(), "anthropic", config.ProviderConfig{
		APIKey:       "sk-secret-1234",
		BaseURL:      "https://proxy.example.com/v1",
		ExtraHeaders: map[string]string{"X-Token": "literal-secret", "X-Org": "$ORG"},
	})
}

func TestProviderEditMasksSecretsAndKeepsReferences(t *testing.T) {
	t.Parallel()

	target := editedAnthropic()
	fields := target.fields()
	require.Empty(t, target.display(fields[0]), "a literal key never reaches the input")
	require.Equal(t, "••••1234 (unchanged)", target.placeholder(fields[0]))
	require.Equal(t, "https://proxy.example.com/v1", target.display(fields[1]))
	require.Equal(t, "X-Org: $ORG; X-Token: ••••cret", target.display(fields[2]))
}

func TestProviderEditUntouchedWritesNothing(t *testing.T) {
	t.Parallel()

	target := editedAnthropic()
	_, update, err := target.buildUpdate(target.fields(), formInputs(target, nil))
	require.NoError(t, err)
	require.True(t, update.Empty(), "%+v", update)
}

func TestProviderEditWritesOnlyChangedFields(t *testing.T) {
	t.Parallel()

	target := editedAnthropic()
	_, update, err := target.buildUpdate(target.fields(), formInputs(target, map[string]string{"Base URL": "https://other.example.com/v1"}))
	require.NoError(t, err)
	require.Equal(t, map[config.ProviderField]any{config.ProviderFieldBaseURL: "https://other.example.com/v1"}, update.Set)
	require.Empty(t, update.Unset)

	_, update, err = target.buildUpdate(target.fields(), formInputs(target, map[string]string{"Base URL": ""}))
	require.NoError(t, err)
	require.Empty(t, update.Set)
	require.Equal(t, []config.ProviderField{config.ProviderFieldBaseURL}, update.Unset)
}

func TestProviderEditHeadersRestoresMaskedLiterals(t *testing.T) {
	t.Parallel()

	target := editedAnthropic()
	_, update, err := target.buildUpdate(target.fields(), formInputs(target, map[string]string{"Extra headers": "X-Org: $ORG2; X-Token: ••••cret"}))
	require.NoError(t, err)
	require.Equal(t, map[config.ProviderField]any{
		config.ProviderFieldExtraHeaders: map[string]string{"X-Org": "$ORG2", "X-Token": "literal-secret"},
	}, update.Set)

	_, _, err = target.buildUpdate(target.fields(), formInputs(target, map[string]string{"Extra headers": "broken"}))
	require.ErrorContains(t, err, "Name: value")
}

func TestProviderReferenceRoundTrips(t *testing.T) {
	t.Parallel()

	target := NewProviderTarget(nil, formCatalog(), "anthropic", config.ProviderConfig{APIKey: "$ANTHROPIC_API_KEY"})
	key := target.fields()[0]
	require.Equal(t, "$ANTHROPIC_API_KEY", target.display(key))

	_, update, err := target.buildUpdate(target.fields(), formInputs(target, nil))
	require.NoError(t, err)
	require.True(t, update.Empty())

	_, update, err = target.buildUpdate(target.fields(), formInputs(target, map[string]string{"API key": "$(op read op://p/k)"}))
	require.NoError(t, err)
	require.Equal(t, map[config.ProviderField]any{config.ProviderFieldAPIKey: "$(op read op://p/k)"}, update.Set)
}

func TestProviderCandidateFillsCatalogDefaults(t *testing.T) {
	t.Parallel()

	target := NewProviderTarget(nil, formCatalog(), "anthropic", config.ProviderConfig{})
	cand, err := target.candidate("anthropic", config.ProviderUpdate{})
	require.NoError(t, err)
	require.Equal(t, catalog.TypeAnthropic, cand.Type)
	require.Equal(t, "$ANTHROPIC_API_KEY", cand.APIKey)
	require.Equal(t, "$ANTHROPIC_API_ENDPOINT", cand.BaseURL)

	bedrock := NewProviderTarget(nil, formCatalog(), "amazon-bedrock", config.ProviderConfig{})
	cand, err = bedrock.candidate("amazon-bedrock", config.ProviderUpdate{})
	require.NoError(t, err)
	require.Empty(t, cand.APIKey, "an empty Bedrock key means the AWS credential chain, not the access key id")
}

func newFormForTest(t *testing.T, target ProviderTarget) *ProviderForm {
	t.Helper()
	s := styles.CharmtonePantera()
	return NewProviderForm(&common.Common{Workspace: &stubWorkspace{cfg: &config.Config{}}, Styles: &s}, target)
}

func TestProviderFormSaveWithoutChangesWritesNothing(t *testing.T) {
	t.Parallel()

	f := newFormForTest(t, editedAnthropic())
	action := f.HandleMsg(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	cmd, ok := action.(ActionCmd)
	require.True(t, ok, "%T", action)
	require.Equal(t, "Nothing changed", cmd.Cmd().(util.InfoMsg).Msg)
}

func TestProviderFormSavesOnlyAfterVerifiedTest(t *testing.T) {
	t.Parallel()

	f := newFormForTest(t, editedAnthropic())
	for _, r := range "sk-new" {
		f.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, ok := f.HandleMsg(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}).(ActionCmd)
	require.True(t, ok)
	require.Equal(t, providerFormTesting, f.status)
	require.Nil(t, f.HandleMsg(tea.KeyPressMsg{Code: 'x', Text: "x"}), "keys are held while the test runs")

	update := config.ProviderUpdate{Set: map[config.ProviderField]any{config.ProviderFieldAPIKey: "sk-new"}}
	require.Nil(t, f.HandleMsg(providerTestedMsg{id: "anthropic", update: update, save: true, err: &config.ConnectionError{Provider: "anthropic", Status: 401, Message: "invalid x-api-key"}}))
	require.Equal(t, providerFormFailed, f.status)
	require.Contains(t, f.err.Error(), "invalid x-api-key")

	require.Equal(t, ActionSaveProvider{ProviderID: "anthropic", Update: update},
		f.HandleMsg(providerTestedMsg{id: "anthropic", update: update, save: true}))
}

func TestProviderFormMovesBetweenFields(t *testing.T) {
	t.Parallel()

	f := newFormForTest(t, editedAnthropic())
	require.Equal(t, "API key ❯ ", f.input.Prompt)
	f.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 1, f.focused)
	require.Equal(t, "https://proxy.example.com/v1", f.input.Value())
	f.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	f.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Equal(t, 2, f.focused, "previous wraps around")
}
