package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestFileMutationMetadataPreservesRendererMetadata(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "main.go")
	content := []byte("package main\n")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	response := withFileMutations(fantasy.ToolResponse{Content: "written", Metadata: `{"additions":1}`}, path)
	var metadata struct {
		Additions int            `json:"additions"`
		Mutations []fileMutation `json:"file_mutations"`
	}
	require.NoError(t, json.Unmarshal([]byte(response.Metadata), &metadata))
	require.Equal(t, 1, metadata.Additions)
	require.Len(t, metadata.Mutations, 1)
	digest := sha256.Sum256(content)
	require.Equal(t, fileMutation{Path: path, Version: hex.EncodeToString(digest[:])[:12]}, metadata.Mutations[0], "the version is the first twelve hex digits of the content digest")
}
