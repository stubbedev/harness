package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/fantasy"
)

const MaxToolResultBytes = 100_000

// MaxToolPreviewBytes bounds what a spilled result still shows inline. The
// full result rides the file path, so a large preview only re-spends the
// context the spill exists to save.
const MaxToolPreviewBytes = 4_000

func CapToolResponse(ctx context.Context, response fantasy.ToolResponse) fantasy.ToolResponse {
	if len(response.Data) != 0 || (response.Type != "" && response.Type != "text") || len(response.Content) <= MaxToolResultBytes {
		return response
	}

	path, err := spillToolResult(GetSessionFromContext(ctx), response.Content)
	var notice string
	if err != nil {
		notice = fmt.Sprintf("Tool result exceeds %d bytes. WARNING: full result could not be saved: %s. Omitted output is lost; retry with a narrower request.\n\nPreview:\n", MaxToolResultBytes, toolResultPrefix(err.Error(), 512))
	} else {
		notice = fmt.Sprintf("Tool result (%d bytes) saved in full to: %s\nUse view with offset/limit or shell to selectively search/read this file; only the first %d bytes follow.\n\nPreview:\n", len(response.Content), path, MaxToolPreviewBytes)
	}
	response.Content = notice + toolResultPrefix(response.Content, min(MaxToolPreviewBytes, MaxToolResultBytes-len(notice)))
	return response
}

func toolResultPrefix(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	return strings.ToValidUTF8(content[:min(len(content), limit)], "")
}

func spillToolResult(sessionID, content string) (string, error) {
	dir, err := ScratchDir(sessionID, "tool-results")
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "result-*.txt")
	if err != nil {
		return "", err
	}
	n, writeErr := file.WriteString(content)
	closeErr := file.Close()
	if writeErr == nil && n != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(file.Name())
		if writeErr != nil {
			return "", writeErr
		}
		return "", closeErr
	}
	return file.Name(), nil
}
