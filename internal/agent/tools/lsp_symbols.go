package tools

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/lsp"
)

type symbolsParams struct {
	File absPath
}

func symbolsAction(lspManager *lsp.Manager) func(context.Context, symbolsParams) (fantasy.ToolResponse, error) {
	return func(ctx context.Context, params symbolsParams) (fantasy.ToolResponse, error) {
		file := string(params.File)
		lspManager.Start(ctx, file)

		client := findLSPClient(lspManager, params.File)
		if client == nil {
			return fantasy.NewTextErrorResponse(noLSPClientMessage(lspManager, file)), nil
		}

		symbols, err := client.DocumentSymbols(ctx, file)
		if err != nil {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to get document symbols: %s", err)), nil
		}
		if len(symbols) == 0 {
			return fantasy.NewTextResponse(fmt.Sprintf("No symbols found in %s", file)), nil
		}

		return fantasy.NewTextResponse(formatSymbols(symbols, 0)), nil
	}
}

func formatSymbols(symbols []protocol.DocumentSymbolResult, indent int) string {
	var b strings.Builder
	prefix := strings.Repeat("  ", indent)
	for _, sym := range symbols {
		rng := sym.GetRange()
		line := rng.Start.Line + 1
		kind := symbolKindString(sym)
		fmt.Fprintf(&b, "%s%s %s (line %d)\n", prefix, kind, sym.GetName(), line)
		if ds, ok := sym.(*protocol.DocumentSymbol); ok && len(ds.Children) > 0 {
			b.WriteString(formatDocumentSymbolChildren(ds.Children, indent+1))
		}
	}
	return b.String()
}

func formatDocumentSymbolChildren(children []protocol.DocumentSymbol, indent int) string {
	var b strings.Builder
	prefix := strings.Repeat("  ", indent)
	for i := range children {
		c := &children[i]
		kind := symbolKindString(c)
		fmt.Fprintf(&b, "%s%s %s (line %d)\n", prefix, kind, c.Name, c.Range.Start.Line+1)
		if len(c.Children) > 0 {
			b.WriteString(formatDocumentSymbolChildren(c.Children, indent+1))
		}
	}
	return b.String()
}

func symbolKindString(sym protocol.DocumentSymbolResult) string {
	var kind protocol.SymbolKind
	switch s := sym.(type) {
	case *protocol.DocumentSymbol:
		kind = s.Kind
	case *protocol.SymbolInformation:
		kind = s.Kind
	default:
		return "Symbol"
	}
	if name, ok := symbolKindNames[kind]; ok {
		return name
	}
	return "Symbol"
}

var symbolKindNames = map[protocol.SymbolKind]string{
	protocol.File:          "File",
	protocol.Module:        "Module",
	protocol.Namespace:     "Namespace",
	protocol.Package:       "Package",
	protocol.Class:         "Class",
	protocol.Method:        "Method",
	protocol.Property:      "Property",
	protocol.Field:         "Field",
	protocol.Constructor:   "Constructor",
	protocol.Enum:          "Enum",
	protocol.Interface:     "Interface",
	protocol.Function:      "Function",
	protocol.Variable:      "Variable",
	protocol.Constant:      "Constant",
	protocol.String:        "String",
	protocol.Number:        "Number",
	protocol.Boolean:       "Boolean",
	protocol.Array:         "Array",
	protocol.Object:        "Object",
	protocol.Key:           "Key",
	protocol.Null:          "Null",
	protocol.EnumMember:    "EnumMember",
	protocol.Struct:        "Struct",
	protocol.Event:         "Event",
	protocol.Operator:      "Operator",
	protocol.TypeParameter: "TypeParameter",
}
