package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/qjebbs/go-jsons"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/stubbedev/harness/internal/catalog"
)

type ProviderField string

const (
	ProviderFieldName         ProviderField = "name"
	ProviderFieldType         ProviderField = "type"
	ProviderFieldBaseURL      ProviderField = "base_url"
	ProviderFieldAPIKey       ProviderField = "api_key"
	ProviderFieldExtraHeaders ProviderField = "extra_headers"
	ProviderFieldAPIVersion   ProviderField = "api_version"
	ProviderFieldProject      ProviderField = "project"
	ProviderFieldLocation     ProviderField = "location"
	ProviderFieldRegion       ProviderField = "region"
	ProviderFieldProfile      ProviderField = "profile"
	ProviderFieldModels       ProviderField = "models"
	ProviderFieldDiscover     ProviderField = "discover_models"
)

var providerFields = []ProviderField{
	ProviderFieldName,
	ProviderFieldType,
	ProviderFieldBaseURL,
	ProviderFieldAPIKey,
	ProviderFieldExtraHeaders,
	ProviderFieldAPIVersion,
	ProviderFieldProject,
	ProviderFieldLocation,
	ProviderFieldRegion,
	ProviderFieldProfile,
	ProviderFieldModels,
	ProviderFieldDiscover,
}

type ProviderUpdate struct {
	Set   map[ProviderField]any `json:"set,omitempty"`
	Unset []ProviderField       `json:"unset,omitempty"`
}

func (u ProviderUpdate) Empty() bool {
	return len(u.Set) == 0 && len(u.Unset) == 0
}

func (u ProviderUpdate) Validate() error {
	for field := range u.Set {
		if !slices.Contains(providerFields, field) {
			return fmt.Errorf("provider field %q cannot be written", field)
		}
	}
	for _, field := range u.Unset {
		if !slices.Contains(providerFields, field) {
			return fmt.Errorf("provider field %q cannot be written", field)
		}
		if _, ok := u.Set[field]; ok {
			return fmt.Errorf("provider field %q is both set and unset", field)
		}
	}
	return nil
}

func (u ProviderUpdate) apply(data string, providerID catalog.InferenceProvider) (string, error) {
	set := make([]ProviderField, 0, len(u.Set))
	for field := range u.Set {
		set = append(set, field)
	}
	slices.Sort(set)
	var err error
	for _, field := range set {
		if data, err = sjson.Set(data, ProviderFieldPath(providerID, string(field)), u.Set[field]); err != nil {
			return "", fmt.Errorf("failed to set provider field %s: %w", field, err)
		}
	}
	for _, field := range u.Unset {
		if data, err = sjson.Delete(data, ProviderFieldPath(providerID, string(field))); err != nil {
			return "", fmt.Errorf("failed to delete provider field %s: %w", field, err)
		}
	}
	return data, nil
}

func (u ProviderUpdate) ApplyTo(settings ProviderConfig) (ProviderConfig, error) {
	if err := u.Validate(); err != nil {
		return ProviderConfig{}, err
	}
	if settings.ID == "" {
		return ProviderConfig{}, errors.New("provider settings carry no provider ID")
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return ProviderConfig{}, err
	}
	doc, err := sjson.SetRaw("{}", ProviderPath(settings.ID), string(raw))
	if err != nil {
		return ProviderConfig{}, err
	}
	if doc, err = u.apply(doc, settings.ID); err != nil {
		return ProviderConfig{}, err
	}
	var out ProviderConfig
	if err := json.Unmarshal([]byte(gjson.Get(doc, ProviderPath(settings.ID)).Raw), &out); err != nil {
		return ProviderConfig{}, err
	}
	out.ID = settings.ID
	return out, nil
}

func (s *ConfigStore) UpdateProvider(ctx context.Context, scope Scope, providerID catalog.InferenceProvider, update ProviderUpdate) error {
	if err := update.Validate(); err != nil {
		return err
	}
	if update.Empty() {
		return nil
	}
	err := s.atomicWrite(scope, func(data []byte) ([]byte, error) {
		out, err := update.apply(string(data), providerID)
		return []byte(out), err
	})
	if err != nil {
		return err
	}
	return s.reloadAfterWrite(ctx)
}

func (s *ConfigStore) RemoveProvider(ctx context.Context, scope Scope, providerID catalog.InferenceProvider) error {
	err := s.atomicWrite(scope, func(data []byte) ([]byte, error) {
		out, err := sjson.Delete(string(data), ProviderPath(providerID))
		return []byte(out), err
	})
	if err != nil {
		return fmt.Errorf("failed to remove provider %s: %w", providerID, err)
	}
	return s.reloadAfterWrite(ctx)
}

func (s *ConfigStore) reloadAfterWrite(ctx context.Context) error {
	if s.workingDir == "" {
		return nil
	}
	return s.ReloadFromDisk(ctx)
}

func (s *ConfigStore) ProviderSettings(providerID catalog.InferenceProvider) (ProviderConfig, bool, error) {
	key := ProviderPath(providerID)
	var entries [][]byte
	for _, path := range s.LoadedPaths() {
		data, err := readConfigJSON(path)
		if err != nil {
			return ProviderConfig{}, false, err
		}
		if entry := gjson.GetBytes(data, key); entry.IsObject() {
			entries = append(entries, []byte(entry.Raw))
		}
	}
	if len(entries) == 0 {
		return ProviderConfig{}, false, nil
	}
	merged, err := jsons.Merge(entries)
	if err != nil {
		return ProviderConfig{}, false, fmt.Errorf("merging settings of provider %s: %w", providerID, err)
	}
	var settings ProviderConfig
	if err := json.Unmarshal(merged, &settings); err != nil {
		return ProviderConfig{}, false, fmt.Errorf("decoding settings of provider %s: %w", providerID, err)
	}
	settings.ID = providerID
	return settings, true, nil
}
