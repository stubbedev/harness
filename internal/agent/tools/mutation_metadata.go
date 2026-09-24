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

// newFileMutation returns the file_mutations entry for path holding
// content.
func newFileMutation(path string, content []byte) fileMutation {
	digest := sha256.Sum256(content)
	// The version tells one edit of a file from the next within a
	// session; twelve hex digits do that, and the full digest per
	// file was a third of every execution-state snapshot.
	return fileMutation{Path: path, Version: hex.EncodeToString(digest[:])[:12]}
}

// withFileMutations adds a file_mutations entry for each path, read back
// from disk, to the response metadata. Tools that know the content they
// wrote put the entries in their metadata struct instead, which spares
// reading the file back and re-parsing metadata that may hold whole files.
func withFileMutations(response fantasy.ToolResponse, paths ...string) fantasy.ToolResponse {
	mutations := make([]fileMutation, 0, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		mutations = append(mutations, newFileMutation(path, content))
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
