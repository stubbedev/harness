package client

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// clientRoute is one request the client builds: its method when the
// builder names it, and its path with every non-literal segment as {}.
type clientRoute struct {
	method string
	path   string
	at     string
}

var verbOf = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "del": "DELETE",
	"MethodGet": "GET", "MethodPost": "POST", "MethodPut": "PUT", "MethodPatch": "PATCH", "MethodDelete": "DELETE",
}

// TestEveryClientRouteIsServed reads every path the client builds with
// apiPath or wsPath and requires the server to register a pattern for it
// (with the same method, where the client names one), so a route renamed
// on one side cannot silently 404 on the other.
func TestEveryClientRouteIsServed(t *testing.T) {
	t.Parallel()

	served := serverPatterns(t)
	routes := clientRoutes(t)
	require.NotEmpty(t, routes)
	for _, r := range routes {
		ok := false
		for _, p := range served {
			if p.path == r.path && (r.method == "" || p.method == r.method) {
				ok = true
				break
			}
		}
		require.True(t, ok, "%s: the client requests %s %s, which the server does not register", r.at, r.method, r.path)
	}
}

func serverPatterns(t *testing.T) []clientRoute {
	t.Helper()
	src, err := os.ReadFile("../server/server.go")
	require.NoError(t, err)
	param := regexp.MustCompile(`\{[^}]*\}`)
	var out []clientRoute
	for _, m := range regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) (/v1[^"]*)"`).FindAllStringSubmatch(string(src), -1) {
		out = append(out, clientRoute{method: m[1], path: param.ReplaceAllString(m[2], "{}")})
	}
	require.NotEmpty(t, out)
	return out
}

func clientRoutes(t *testing.T) []clientRoute {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var out []clientRoute
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		var stack []ast.Node
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || (fn.Name != "apiPath" && fn.Name != "wsPath") {
				return true
			}
			if isHelperBody(stack) {
				return true
			}
			segments := []string{"/v1"}
			args := call.Args
			if fn.Name == "wsPath" {
				segments = append(segments, "workspaces", "{}")
				args = args[1:]
			}
			for _, arg := range args {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					value, _ := strconv.Unquote(lit.Value)
					segments = append(segments, value)
					continue
				}
				segments = append(segments, "{}")
			}
			out = append(out, clientRoute{
				method: enclosingMethod(stack),
				path:   strings.Join(segments, "/"),
				at:     fset.Position(call.Pos()).String(),
			})
			return true
		})
	}
	return out
}

// isHelperBody reports whether the call sits in apiPath or wsPath itself.
func isHelperBody(stack []ast.Node) bool {
	for _, n := range stack {
		if fd, ok := n.(*ast.FuncDecl); ok && (fd.Name.Name == "apiPath" || fd.Name.Name == "wsPath") {
			return true
		}
	}
	return false
}

// enclosingMethod finds the verb of the request builder the path is passed
// to: get(...)/post(...) or a request literal's method field.
func enclosingMethod(stack []ast.Node) string {
	for i := len(stack) - 2; i >= 0; i-- {
		switch n := stack[i].(type) {
		case *ast.CallExpr:
			if fn, ok := n.Fun.(*ast.Ident); ok {
				if verb, ok := verbOf[fn.Name]; ok {
					return verb
				}
			}
		case *ast.CompositeLit:
			for _, elt := range n.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "method" {
					if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
						return verbOf[sel.Sel.Name]
					}
				}
			}
		case *ast.FuncDecl:
			return ""
		}
	}
	return ""
}
