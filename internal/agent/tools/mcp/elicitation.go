package mcp

import (
	"context"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ElicitationHandler answers a server-initiated elicitation request
// (elicitation/create): the server asks the human a question while a tool
// call is in flight. server is the config name of the asking MCP server.
// Return a non-nil result to answer; an error declines.
type ElicitationHandler = func(ctx context.Context, server string, params *mcp.ElicitParams) (*mcp.ElicitResult, error)

var (
	elicitationMu       sync.RWMutex
	elicitationHandler  ElicitationHandler
)

// SetElicitationHandler installs the process-wide elicitation handler.
// Passing nil removes it; servers then see no elicitation capability and
// requests are declined by the protocol layer. Must be called before
// Initialize; connections created earlier keep whatever handler was
// installed at their construction time.
func SetElicitationHandler(h ElicitationHandler) {
	elicitationMu.Lock()
	defer elicitationMu.Unlock()
	elicitationHandler = h
}

// currentElicitationHandler returns the installed handler and the client
// option wiring it into a new connection for the named server.
func currentElicitationHandler(name string) func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	elicitationMu.RLock()
	h := elicitationHandler
	elicitationMu.RUnlock()
	if h == nil {
		return nil
	}
	return func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		res, err := h(ctx, name, req.Params)
		if err != nil {
			slog.Warn("MCP elicitation declined",
				"name", name,
				"error", err,
			)
			return &mcp.ElicitResult{Action: "decline"}, nil
		}
		return res, nil
	}
}
