package tools

import (
	"fmt"
	"os"
	"strings"

	"charm.land/fantasy"
)

const MaxDirectoryEntries = 200

func viewDirectory(path string, offset, limit int) (viewFileContent, *fantasy.ToolResponse, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return viewFileContent{}, nil, err
	}
	if limit <= 0 {
		limit = MaxDirectoryEntries
	}
	limit = min(limit, MaxDirectoryEntries)
	start := min(max(0, offset), len(entries))
	end := min(start+limit, len(entries))
	var b strings.Builder
	fmt.Fprintf(&b, "<directory path=%q>\n", path)
	for _, entry := range entries[start:end] {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		fmt.Fprintf(&b, "%q\n", name)
	}
	if end < len(entries) {
		fmt.Fprintf(&b, "\n%d more entries; use offset %d (maximum %d per call).\n", len(entries)-end, end, MaxDirectoryEntries)
	}
	b.WriteString("</directory>\n")
	return viewFileContent{output: b.String(), meta: ViewResponseMetadata{FilePath: path, Content: b.String()}}, nil, nil
}
