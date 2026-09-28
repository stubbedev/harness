package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// TestEveryOpenableDialogHasAnOpener reads every ActionOpenDialog literal
// in the UI and requires the dialog it names to have an opener, so a
// palette entry cannot open nothing.
func TestEveryOpenableDialogHasAnOpener(t *testing.T) {
	t.Parallel()

	named := map[string]bool{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isOpenDialogType(lit.Type) || len(lit.Elts) == 0 {
				return true
			}
			value := lit.Elts[0]
			if kv, ok := value.(*ast.KeyValueExpr); ok {
				value = kv.Value
			}
			switch v := value.(type) {
			case *ast.Ident:
				named[v.Name] = true
			case *ast.SelectorExpr:
				named[v.Sel.Name] = true
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, named)

	known := map[string]bool{}
	for id := range dialogOpeners {
		for _, name := range dialogIDNames {
			if name.id == id {
				known[name.name] = true
			}
		}
	}
	for name := range named {
		require.True(t, known[name], "ActionOpenDialog names %s, which has no opener in dialogOpeners", name)
	}
}

func isOpenDialogType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "ActionOpenDialog"
	case *ast.SelectorExpr:
		return t.Sel.Name == "ActionOpenDialog"
	}
	return false
}

// dialogIDNames spells each opener's ID constant, which the literals name.
var dialogIDNames = []struct {
	name string
	id   dialog.ID
}{
	{"SessionsID", dialog.SessionsID},
	{"ModelsID", dialog.ModelsID},
	{"CommandsID", dialog.CommandsID},
	{"ReasoningID", dialog.ReasoningID},
	{"ConnectID", dialog.ConnectID},
	{"ThemesID", dialog.ThemesID},
	{"RewindID", dialog.RewindID},
	{"NotificationsID", dialog.NotificationsID},
	{"MentionPickerID", dialog.MentionPickerID},
	{"MCPServersID", dialog.MCPServersID},
	{"LSPServersID", dialog.LSPServersID},
}
