package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestPathBuildersEscapeUserKeys(t *testing.T) {
	t.Parallel()

	doc, err := sjson.Set("{}", ProviderFieldPath("my.llm", "api_key"), "k")
	require.NoError(t, err)
	require.Equal(t, "k", gjson.Get(doc, "providers").Get(`my\.llm`).Get("api_key").String())
	require.Equal(t, "k", gjson.Get(doc, ProviderFieldPath("my.llm", "api_key")).String())
}

// TestLiteralConfigPathsNameRealFields finds every string literal passed
// as a path to the config setters and requires it to resolve to a field of
// Config through the JSON tags. A renamed tag otherwise leaves the write
// landing on a key nothing reads.
func TestLiteralConfigPathsNameRealFields(t *testing.T) {
	t.Parallel()

	setters := map[string]bool{"SetConfigField": true, "SetConfigFields": true, "RemoveConfigField": true}
	var paths []string
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !setters[sel.Sel.Name] {
				return true
			}
			for _, arg := range call.Args {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if value, err := strconv.Unquote(lit.Value); err == nil && strings.Contains(value, ".") {
						paths = append(paths, value)
					}
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		require.True(t, configPathResolves(reflect.TypeFor[Config](), strings.Split(path, ".")), "config path %q names no field", path)
	}
}

// csyncMapValue returns the value type of a csync.Map, which config uses
// for maps it shares across goroutines and JSON reads as a map.
func csyncMapValue(t reflect.Type) (reflect.Type, bool) {
	if !strings.HasSuffix(t.PkgPath(), "internal/csync") || !strings.HasPrefix(t.Name(), "Map[") {
		return nil, false
	}
	for field := range t.Fields() {
		if field.Type.Kind() == reflect.Map {
			return field.Type.Elem(), true
		}
	}
	return nil, false
}

// configPathResolves walks segments through t by JSON tag; a map takes any
// key.
func configPathResolves(t reflect.Type, segments []string) bool {
	for len(segments) > 0 {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Map:
			t, segments = t.Elem(), segments[1:]
		case reflect.Struct:
			if inner, ok := csyncMapValue(t); ok {
				t, segments = inner, segments[1:]
				continue
			}
			found := false
			for field := range t.Fields() {
				name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
				if name == segments[0] {
					t, segments, found = field.Type, segments[1:], true
					break
				}
			}
			if !found {
				return false
			}
		default:
			return false
		}
	}
	return true
}
