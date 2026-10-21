package dialog

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
)

const providerFieldID config.ProviderField = "id"

const secretMask = "••••"

var customProviderTypes = []catalog.Type{
	catalog.TypeOpenAICompat,
	catalog.TypeOpenAI,
	catalog.TypeAnthropic,
	catalog.TypeGoogle,
}

type ProviderTarget struct {
	Provider   catalog.Provider
	Settings   config.ProviderConfig
	Custom     bool
	Configured bool
	reserved   map[catalog.InferenceProvider]bool
}

func NewProviderTarget(cfg *config.Config, catalogProviders []catalog.Provider, id catalog.InferenceProvider, settings config.ProviderConfig) ProviderTarget {
	target := ProviderTarget{Settings: settings}
	if cfg != nil && id != "" {
		_, target.Configured = cfg.Providers.Get(id)
	}
	if id == "" {
		target.Custom = true
		target.reserved = make(map[catalog.InferenceProvider]bool)
		for _, p := range catalogProviders {
			target.reserved[p.ID] = true
		}
		if cfg != nil {
			for pid := range cfg.Providers.Seq2() {
				target.reserved[pid] = true
			}
		}
		return target
	}
	target.Settings.ID = id
	if idx := slices.IndexFunc(catalogProviders, func(p catalog.Provider) bool { return p.ID == id }); idx >= 0 {
		target.Provider = catalogProviders[idx]
		return target
	}
	target.Custom = true
	target.Provider = catalog.Provider{ID: id, Name: cmp.Or(settings.Name, string(id)), Type: settings.Type}
	return target
}

func (t ProviderTarget) title() string {
	if t.Provider.ID == "" {
		return "Add Custom Provider"
	}
	return "Configure " + cmp.Or(t.Provider.Name, string(t.Provider.ID))
}

type providerField struct {
	key         config.ProviderField
	label       string
	placeholder string
	secret      bool
	required    bool
}

func (t ProviderTarget) fields() []providerField {
	headers := providerField{key: config.ProviderFieldExtraHeaders, label: "Extra headers", placeholder: "Name: value; Other: $VAR"}
	apiKey := providerField{key: config.ProviderFieldAPIKey, label: "API key", placeholder: cmp.Or(t.Provider.APIKey, "sk-... or $VAR or $(command)"), secret: true}
	baseURL := providerField{key: config.ProviderFieldBaseURL, label: "Base URL", placeholder: cmp.Or(t.Provider.APIEndpoint, "provider default")}
	if t.Custom {
		var fields []providerField
		if t.Provider.ID == "" {
			fields = append(fields, providerField{key: providerFieldID, label: "ID", placeholder: "my-provider", required: true})
		}
		types := make([]string, len(customProviderTypes))
		for i, pt := range customProviderTypes {
			types[i] = string(pt)
		}
		baseURL.placeholder = "https://api.example.com/v1"
		baseURL.required = true
		apiKey.placeholder = "$VAR, $(command) or the key; empty for none"
		return append(fields,
			providerField{key: config.ProviderFieldName, label: "Name", placeholder: "display name"},
			providerField{key: config.ProviderFieldType, label: "Type", placeholder: strings.Join(types, " | "), required: true},
			baseURL,
			apiKey,
			providerField{key: config.ProviderFieldModels, label: "Models", placeholder: "model-a, model-b; empty to discover"},
			headers,
		)
	}
	switch t.Provider.Type {
	case catalog.TypeAzure:
		baseURL.label = "Endpoint"
		baseURL.placeholder = cmp.Or(t.Provider.APIEndpoint, "https://<resource>.openai.azure.com")
		return []providerField{
			baseURL,
			apiKey,
			{key: config.ProviderFieldAPIVersion, label: "API version", placeholder: "$AZURE_OPENAI_API_VERSION"},
			headers,
		}
	case catalog.TypeVertexAI:
		return []providerField{
			{key: config.ProviderFieldProject, label: "Project", placeholder: "$VERTEXAI_PROJECT"},
			{key: config.ProviderFieldLocation, label: "Location", placeholder: "$VERTEXAI_LOCATION"},
			headers,
		}
	case catalog.TypeBedrock:
		apiKey.label = "API key"
		apiKey.placeholder = "Bedrock API key; empty for AWS credentials"
		return []providerField{
			apiKey,
			{key: config.ProviderFieldRegion, label: "Region", placeholder: "AWS SDK default"},
			{key: config.ProviderFieldProfile, label: "Profile", placeholder: "$AWS_PROFILE"},
			headers,
		}
	default:
		return []providerField{apiKey, baseURL, headers}
	}
}

func (t ProviderTarget) rawValue(key config.ProviderField) string {
	s := t.Settings
	switch key {
	case providerFieldID:
		return string(s.ID)
	case config.ProviderFieldName:
		return s.Name
	case config.ProviderFieldType:
		return string(s.Type)
	case config.ProviderFieldBaseURL:
		return s.BaseURL
	case config.ProviderFieldAPIKey:
		return s.APIKey
	case config.ProviderFieldAPIVersion:
		return s.APIVersion
	case config.ProviderFieldProject:
		return s.Project
	case config.ProviderFieldLocation:
		return s.Location
	case config.ProviderFieldRegion:
		return s.Region
	case config.ProviderFieldProfile:
		return s.Profile
	case config.ProviderFieldExtraHeaders:
		return formatHeaders(s.ExtraHeaders, false)
	case config.ProviderFieldModels:
		return strings.Join(modelIDs(s.Models), ", ")
	default:
		return ""
	}
}

func (t ProviderTarget) display(f providerField) string {
	if f.key == config.ProviderFieldExtraHeaders {
		return formatHeaders(t.Settings.ExtraHeaders, true)
	}
	raw := t.rawValue(f.key)
	if f.secret && !isReference(raw) {
		return ""
	}
	return raw
}

func (t ProviderTarget) placeholder(f providerField) string {
	raw := t.rawValue(f.key)
	if f.secret && raw != "" && !isReference(raw) {
		return maskSecret(raw) + " (unchanged)"
	}
	return f.placeholder
}

func (t ProviderTarget) buildUpdate(fields []providerField, inputs []string) (catalog.InferenceProvider, config.ProviderUpdate, error) {
	id := t.Provider.ID
	update := config.ProviderUpdate{Set: make(map[config.ProviderField]any)}
	var errs []error
	for i, f := range fields {
		in := strings.TrimSpace(inputs[i])
		shown := t.display(f)
		raw := t.rawValue(f.key)
		if f.required && in == "" && (in != shown || raw == "") {
			errs = append(errs, fmt.Errorf("%s is required", f.label))
			continue
		}
		if f.key == providerFieldID {
			id = catalog.InferenceProvider(in)
			if strings.ContainsFunc(in, isSpace) {
				errs = append(errs, fmt.Errorf("ID %q cannot contain spaces", in))
			} else if t.reserved[id] {
				errs = append(errs, fmt.Errorf("a provider with ID %q already exists", in))
			}
			continue
		}
		if in == shown {
			continue
		}
		switch f.key {
		case config.ProviderFieldExtraHeaders:
			headers, err := parseHeaders(in, t.Settings.ExtraHeaders)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if maps.Equal(headers, t.Settings.ExtraHeaders) {
				continue
			}
			setOrUnset(&update, f.key, len(headers) == 0, len(t.Settings.ExtraHeaders) > 0, headers)
		case config.ProviderFieldModels:
			ids := splitList(in)
			if slices.Equal(ids, modelIDs(t.Settings.Models)) {
				continue
			}
			setOrUnset(&update, f.key, len(ids) == 0, len(t.Settings.Models) > 0, modelsFor(ids, t.Settings.Models))
		case config.ProviderFieldType:
			if !slices.Contains(customProviderTypes, catalog.Type(in)) {
				errs = append(errs, fmt.Errorf("type %q is not one of %s", in, f.placeholder))
				continue
			}
			setOrUnset(&update, f.key, false, true, in)
		default:
			setOrUnset(&update, f.key, in == "", raw != "", in)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return "", config.ProviderUpdate{}, err
	}
	return id, update, nil
}

func setOrUnset(update *config.ProviderUpdate, key config.ProviderField, empty, existed bool, value any) {
	switch {
	case !empty:
		update.Set[key] = value
	case existed:
		update.Unset = append(update.Unset, key)
	}
}

func (t ProviderTarget) candidate(id catalog.InferenceProvider, update config.ProviderUpdate) (config.ProviderConfig, error) {
	base := t.Settings
	base.ID = id
	cand, err := update.ApplyTo(base)
	if err != nil {
		return config.ProviderConfig{}, err
	}
	if t.Custom {
		cand.Type = cmp.Or(cand.Type, catalog.TypeOpenAICompat)
		cand.Name = cmp.Or(cand.Name, string(id))
		return cand, nil
	}
	cand.Type = t.Provider.Type
	cand.Name = cmp.Or(cand.Name, t.Provider.Name)
	cand.BaseURL = cmp.Or(cand.BaseURL, t.Provider.APIEndpoint)
	if t.Provider.Type != catalog.TypeBedrock {
		cand.APIKey = cmp.Or(cand.APIKey, t.Provider.APIKey)
	}
	headers := maps.Clone(t.Provider.DefaultHeaders)
	if headers == nil {
		headers = make(map[string]string)
	}
	maps.Copy(headers, cand.ExtraHeaders)
	cand.ExtraHeaders = headers
	return cand, nil
}

func isReference(v string) bool {
	return strings.Contains(v, "$")
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n'
}

func maskSecret(v string) string {
	runes := []rune(v)
	if len(runes) <= 4 {
		return secretMask
	}
	return secretMask + string(runes[len(runes)-4:])
}

func formatHeaders(headers map[string]string, masked bool) string {
	parts := make([]string, 0, len(headers))
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		value := headers[name]
		if masked && !isReference(value) {
			value = maskSecret(value)
		}
		parts = append(parts, name+": "+value)
	}
	return strings.Join(parts, "; ")
}

func parseHeaders(in string, original map[string]string) (map[string]string, error) {
	headers := make(map[string]string)
	for part := range strings.SplitSeq(in, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("header %q must be written as Name: value", part)
		}
		value = strings.TrimSpace(value)
		if prev, ok := original[name]; ok && !isReference(prev) && value == maskSecret(prev) {
			value = prev
		}
		headers[name] = value
	}
	return headers, nil
}

func splitList(in string) []string {
	var out []string
	for item := range strings.SplitSeq(in, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func modelIDs(models []catalog.Model) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}

func modelsFor(ids []string, existing []catalog.Model) []catalog.Model {
	models := make([]catalog.Model, 0, len(ids))
	for _, id := range ids {
		if idx := slices.IndexFunc(existing, func(m catalog.Model) bool { return m.ID == id }); idx >= 0 {
			models = append(models, existing[idx])
			continue
		}
		models = append(models, catalog.Model{ID: id, Name: id})
	}
	return models
}
