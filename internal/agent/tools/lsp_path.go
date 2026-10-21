package tools

import (
	"fmt"
	"os"
	"strings"

	"github.com/stubbedev/harness/internal/filepathext"
)

type absPath string

var lspFileActions = []LSPAction{LSPActionSymbols, LSPActionReplaceSymbol}

type lspPaths struct {
	workingDir string
}

func (r lspPaths) root(path string) absPath {
	return absPath(filepathext.SmartJoin(r.workingDir, path))
}

func (r lspPaths) optionalFile(path string) absPath {
	if path == "" {
		return ""
	}
	return r.root(path)
}

func (r lspPaths) file(action LSPAction, path string) (absPath, error) {
	if path == "" {
		return "", fmt.Errorf("%s needs path to name a file: %s act on one file", action, lspFileActionList())
	}
	resolved := r.root(path)
	if info, err := os.Stat(string(resolved)); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s needs path to name a file, but %s is a directory: %s act on one file", action, resolved, lspFileActionList())
	}
	return resolved, nil
}

func lspFileActionList() string {
	names := make([]string, len(lspFileActions))
	for i, action := range lspFileActions {
		names[i] = string(action)
	}
	return strings.Join(names, " and ")
}
