package server

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/app"
	"github.com/stubbedev/harness/internal/backend"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/skills"
)

// wrapEvent converts a raw tea.Msg (a pubsub.Event[T] from the app
// event fan-in) into a pubsub.Payload envelope with the correct
// PayloadType discriminator and a proto-typed inner payload that has
// proper JSON tags. Returns nil if the event type is unrecognized.
func wrapEvent(ev any) *pubsub.Payload {
	switch e := ev.(type) {
	case pubsub.Event[app.LSPEvent]:
		return envelope(pubsub.PayloadTypeLSPEvent, pubsub.Event[proto.LSPEvent]{
			Type: e.Type,
			Payload: proto.LSPEvent{
				Type:            proto.LSPEventType(e.Payload.Type),
				Name:            e.Payload.Name,
				State:           e.Payload.State,
				Error:           e.Payload.Error,
				DiagnosticCount: e.Payload.DiagnosticCount,
			},
		})
	case pubsub.Event[mcp.Event]:
		payload, ok := proto.MCPEventFromDomain(e.Payload)
		if !ok {
			// Unsupported MCP event type (e.g. EventChannelMessage, which
			// has no proto representation until session delivery is wired
			// up). Drop it instead of fabricating a state_changed event.
			slog.Debug("Dropping unsupported MCP event type for SSE", "type", e.Payload.Type)
			return nil
		}
		return envelope(pubsub.PayloadTypeMCPEvent, pubsub.Event[proto.MCPEvent]{Type: e.Type, Payload: payload})
	case pubsub.Event[question.Request]:
		slog.Info("Wrapping question batch event for SSE", "id", e.Payload.ID, "questions", len(e.Payload.Questions))
		return envelope(pubsub.PayloadTypeQuestionRequest, pubsub.Event[proto.QuestionRequest]{
			Type:    e.Type,
			Payload: proto.QuestionRequestFromDomain(e.Payload),
		})
	case pubsub.Event[question.Notification]:
		return envelope(pubsub.PayloadTypeQuestionNotification, pubsub.Event[proto.QuestionNotification]{
			Type:    e.Type,
			Payload: proto.QuestionNotification(e.Payload),
		})
	case pubsub.Event[message.Message]:
		return envelope(pubsub.PayloadTypeMessage, pubsub.Event[proto.Message]{
			Type:    e.Type,
			Payload: proto.MessageFromDomain(e.Payload),
		})
	case pubsub.Event[session.Session]:
		return envelope(pubsub.PayloadTypeSession, pubsub.Event[proto.Session]{
			Type:    e.Type,
			Payload: proto.SessionFromDomain(e.Payload),
		})
	case pubsub.Event[history.File]:
		return envelope(pubsub.PayloadTypeFile, pubsub.Event[proto.File]{
			Type:    e.Type,
			Payload: proto.FileFromDomain(e.Payload),
		})
	case pubsub.Event[notify.Notification]:
		return envelope(pubsub.PayloadTypeAgentEvent, pubsub.Event[proto.AgentEvent]{
			Type:    e.Type,
			Payload: proto.AgentEventFromDomain(e.Payload),
		})
	case pubsub.Event[notify.RunComplete]:
		return envelope(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{
			Type:    e.Type,
			Payload: proto.RunCompleteFromDomain(e.Payload),
		})
	case pubsub.Event[proto.ConfigChanged]:
		return envelope(pubsub.PayloadTypeConfigChanged, e)
	case app.UpdateAvailableMsg:
		return envelope(pubsub.PayloadTypeUpdateAvailable, pubsub.Event[proto.UpdateAvailable]{
			Type: pubsub.UpdatedEvent,
			Payload: proto.UpdateAvailable{
				CurrentVersion: e.CurrentVersion,
				LatestVersion:  e.LatestVersion,
				IsDevelopment:  e.IsDevelopment,
			},
		})
	case pubsub.Event[skills.Event]:
		return envelope(pubsub.PayloadTypeSkillsEvent, pubsub.Event[proto.SkillsEvent]{
			Type:    e.Type,
			Payload: proto.SkillsEvent{States: proto.SkillStatesFromDomain(e.Payload.States)},
		})
	default:
		slog.Warn("Unrecognized event type for SSE wrapping", "type", fmt.Sprintf("%T", ev))
		return nil
	}
}

// envelope marshals the inner event and wraps it in a pubsub.Payload.
func envelope(payloadType pubsub.PayloadType, inner any) *pubsub.Payload {
	raw, err := json.Marshal(inner)
	if err != nil {
		slog.Error("Failed to marshal event payload", "error", err)
		return nil
	}
	return &pubsub.Payload{
		Type:    payloadType,
		Payload: raw,
	}
}

// isSessionBusy reports whether the given workspace has an in-flight
// agent run for sessionID. It tolerates a nil workspace (treating it as
// "not busy") so REST handlers can pass GetWorkspace's result through
// unconditionally — the workspace lookup error is already surfaced by
// the prior ListSessions/GetSession call when relevant.
func isSessionBusy(ws *backend.Workspace, sessionID string) bool {
	if ws == nil || ws.App == nil || ws.AgentCoordinator == nil {
		return false
	}
	return ws.AgentCoordinator.IsSessionBusy(sessionID)
}

// attachedClients returns the number of clients currently viewing
// sessionID in ws. Hold-only clients (streams == 0) do not contribute.
// A nil workspace is treated as zero so handlers can pass GetWorkspace's
// result through without an extra guard.
func attachedClients(ws *backend.Workspace, sessionID string) int {
	if ws == nil {
		return 0
	}
	return ws.AttachedClientsForSession(sessionID)
}
