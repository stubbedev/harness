package envvars

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var harnessVar = regexp.MustCompile(`^HARNESS_[A-Z0-9_]+$`)

// TestEveryVariableIsDocumented holds each registered variable to a row
// in the config docs' environment table.
func TestEveryVariableIsDocumented(t *testing.T) {
	t.Parallel()

	docs, err := os.ReadFile("../../docs/config/README.md")
	require.NoError(t, err)
	for _, name := range All() {
		require.Contains(t, string(docs), "`"+name+"`", "%s is not documented in docs/config/README.md", name)
	}
}

// TestNoVariableIsReadByLiteral finds every HARNESS_* string literal in
// the non-test sources outside this package. Each must be one of these
// constants' names spelled out, which means it bypassed the registry.
func TestNoVariableIsReadByLiteral(t *testing.T) {
	t.Parallel()

	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if filepath.Base(filepath.Dir(path)) == "envvars" {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if value, err := strconv.Unquote(lit.Value); err == nil && harnessVar.MatchString(value) {
				t.Errorf("%s: %q read by literal; add it to internal/envvars", fset.Position(lit.Pos()), value)
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
}
