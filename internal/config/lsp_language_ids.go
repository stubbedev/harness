package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

type LanguageIDs map[string]protocol.LanguageKind

func NormalizeFileType(fileType string) string {
	return strings.TrimPrefix(strings.ToLower(fileType), ".")
}

func (ids *LanguageIDs) UnmarshalJSON(data []byte) error {
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("language_ids must map file patterns to language ID strings: %w", err)
	}
	parsed := make(LanguageIDs, len(raw))
	for _, pattern := range slices.Sorted(maps.Keys(raw)) {
		key := NormalizeFileType(pattern)
		if key == "" {
			return fmt.Errorf("language_ids: file pattern %q is empty", pattern)
		}
		id := raw[pattern]
		if id == "" {
			return fmt.Errorf("language_ids: language ID for %q is empty", pattern)
		}
		if _, dup := parsed[key]; dup {
			return fmt.Errorf("language_ids: file pattern %q duplicates another pattern that also normalizes to %q", pattern, key)
		}
		parsed[key] = protocol.LanguageKind(id)
	}
	*ids = parsed
	return nil
}
