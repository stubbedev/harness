package lsp

import (
	"context"
	"encoding/json"
	"log/slog"

	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stubbedev/harness/internal/lsp/util"
)

// HandleWorkspaceConfiguration handles workspace configuration requests
func HandleWorkspaceConfiguration(_ context.Context, _ string, params json.RawMessage) (any, error) {
	return []map[string]any{{}}, nil
}

// HandleWorkDoneProgressCreate handles server-initiated window/workDoneProgress/create
// requests. The client advertises window.workDoneProgress: true in its capabilities
// (see makeClientCapabilities in powernap), which per the LSP spec grants servers
// permission to send this request — so it must be answered, even as a no-op, or the
// server (e.g. typescript-language-server) treats the unhandled response as fatal and
// crashes. See github.com/charmbracelet/x issue tracking powernap capability gaps.
func HandleWorkDoneProgressCreate(_ context.Context, _ string, _ json.RawMessage) (any, error) {
	return nil, nil
}

// HandleRegisterCapability acknowledges capability registration requests.
// Nothing consumes dynamic registrations (file watching is done by Harness
// itself), so they are validated and otherwise ignored.
func HandleRegisterCapability(_ context.Context, _ string, params json.RawMessage) (any, error) {
	var registerParams protocol.RegistrationParams
	if err := json.Unmarshal(params, &registerParams); err != nil {
		slog.Error("Error unmarshaling registration params", "error", err)
		return nil, err
	}

	return nil, nil
}

// HandleApplyEdit handles workspace edit requests
func HandleApplyEdit(encoding powernap.OffsetEncoding) func(_ context.Context, _ string, params json.RawMessage) (any, error) {
	return func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var edit protocol.ApplyWorkspaceEditParams
		if err := json.Unmarshal(params, &edit); err != nil {
			return nil, err
		}

		err := util.ApplyWorkspaceEdit(edit.Edit, encoding)
		if err != nil {
			slog.Error("Error applying workspace edit", "error", err)
			return protocol.ApplyWorkspaceEditResult{Applied: false, FailureReason: err.Error()}, nil
		}

		return protocol.ApplyWorkspaceEditResult{Applied: true}, nil
	}
}

// HandleServerMessage handles server messages
func HandleServerMessage(_ context.Context, method string, params json.RawMessage) {
	var msg protocol.ShowMessageParams
	if err := json.Unmarshal(params, &msg); err != nil {
		slog.Debug("Error unmarshal server message", "error", err)
		return
	}

	switch msg.Type {
	case protocol.Error:
		slog.Error("LSP Server", "message", msg.Message)
	case protocol.Warning:
		slog.Warn("LSP Server", "message", msg.Message)
	case protocol.Info:
		slog.Info("LSP Server", "message", msg.Message)
	case protocol.Log:
		slog.Debug("LSP Server", "message", msg.Message)
	}
}

// HandleDiagnostics handles diagnostic notifications from the LSP server
func HandleDiagnostics(client *Client, params json.RawMessage) {
	var diagParams protocol.PublishDiagnosticsParams
	if err := json.Unmarshal(params, &diagParams); err != nil {
		slog.Error("Error unmarshaling diagnostics params", "error", err)
		return
	}

	client.diagnostics.Set(diagParams.URI, diagParams.Diagnostics)

	// Calculate total diagnostic count
	totalCount := 0
	for _, diagnostics := range client.diagnostics.Seq2() {
		totalCount += len(diagnostics)
	}

	// Trigger callback if set
	if client.onDiagnosticsChanged != nil {
		client.onDiagnosticsChanged(client.name, totalCount)
	}
}
