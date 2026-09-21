package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"

	"charm.land/fantasy"
	"github.com/tidwall/sjson"
)

type fileMutation struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

func withFileMutations(response fantasy.ToolResponse, paths ...string) fantasy.ToolResponse {
	mutations := make([]fileMutation, 0, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		digest := sha256.Sum256(content)
		// The version tells one edit of a file from the next within a
		// session; twelve hex digits do that, and the full digest per
		// file was a third of every execution-state snapshot.
		mutations = append(mutations, fileMutation{Path: path, Version: hex.EncodeToString(digest[:])[:12]})
	}
	if len(mutations) == 0 {
		return response
	}
	metadata := response.Metadata
	if metadata == "" {
		metadata = "{}"
	}
	data, err := json.Marshal(mutations)
	if err != nil {
		return response
	}
	if updated, err := sjson.SetRaw(metadata, "file_mutations", string(data)); err == nil {
		response.Metadata = updated
	}
	return response
}
