package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/goccy/go-yaml"
)

// Config files are YAML on disk and JSON in memory. Every struct in this
// package is tagged for encoding/json, and the merge, read, and write
// paths are all built on JSON pointers (gjson/sjson), so YAML is handled
// by converting at the edges rather than by teaching those paths a second
// syntax. The conversion is lossless for the data model — YAML's scalars,
// sequences, and mappings map onto JSON's — but it does not preserve
// comments or key order, which is why the app only ever writes back to
// the machine-owned state file and project/user configs stay hand-owned.

// yamlExt and ymlExt are the recognized config file extensions.
const (
	yamlExt = ".yaml"
	ymlExt  = ".yml"

	// userConfigFile is the hand-written configuration, read but never
	// written by Harness: $XDG_CONFIG_HOME/harness/config.yaml and
	// /etc/harness/config.yaml.
	userConfigFile = "config" + yamlExt

	// stateConfigFile is the machine-owned configuration Harness writes
	// back to — API keys, OAuth tokens, model pins, recent models, UI
	// preferences. It lives in the data directory, globally at
	// $XDG_DATA_HOME/harness/state.yaml and per workspace at
	// .harness/state.yaml. Writes round-trip through JSON, so comments
	// placed here do not survive.
	stateConfigFile = "state" + yamlExt
)

// decodeConfig converts on-disk config bytes to the JSON representation
// used throughout this package. An empty (or whitespace/comment-only)
// document yields nil, which callers treat as "nothing to merge" rather
// than as an error: a config file holding only comments is legitimate.
func decodeConfig(data []byte) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	jsonBytes, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, err
	}
	// A document that parses to the null literal carries no settings.
	if bytes.Equal(bytes.TrimSpace(jsonBytes), []byte("null")) {
		return nil, nil
	}
	if !json.Valid(jsonBytes) {
		return nil, fmt.Errorf("config did not convert to valid JSON")
	}
	return jsonBytes, nil
}

// encodeConfig converts the JSON representation back to the YAML written
// to disk. Empty input produces an empty document rather than the "null"
// literal, so truncating a config leaves a readable file.
func encodeConfig(jsonBytes []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(jsonBytes)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	out, err := yaml.JSONToYAML(jsonBytes)
	if err != nil {
		return nil, err
	}
	return out, nil
}
