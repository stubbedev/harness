package backend

import (
	"context"
	"errors"

	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/crash"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
)

// SendMessage validates and accepts a prompt for the workspace's agent,
// then dispatches the run on a goroutine bound to the workspace context
// and returns immediately. It does not wait for the LLM turn to
// complete: the run's lifetime is owned by the workspace, not by the
// caller. Errors from the dispatched run reach observers through the
// agent event channels (a notify.TypeAgentError notification), not
// through this return value.
//
// SendMessage returns synchronously when the request cannot be accepted:
// ErrWorkspaceNotFound if the workspace is missing, ErrAgentNotInitialized
// if its coordinator is nil, the structural validation errors from
// agent.ValidateCall (ErrEmptyPrompt, ErrSessionMissing) when the prompt
// or session is missing, ErrInvalidSessionID or ErrInvalidRunID when an
// identifier is not the canonical UUID shape harness mints them in, and
// ErrWorkspaceClosing if the workspace is being torn down.
func (b *Backend) SendMessage(workspaceID string, msg proto.AgentMessage) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if ws.AgentCoordinator == nil {
		return ErrAgentNotInitialized
	}

	if err := agent.ValidateCall(agent.SessionAgentCall{
		SessionID:   msg.SessionID,
		Prompt:      msg.Prompt,
		Attachments: proto.AttachmentsToMessage(msg.Attachments),
	}); err != nil {
		return err
	}

	sessionID := msg.SessionID
	// The ID arrives over the wire and reaches scratch directory names,
	// context values, and git command environments downstream, so only
	// the canonical UUID shape every session is minted in is accepted.
	if !agent.UUIDPattern.MatchString(sessionID) {
		return ErrInvalidSessionID
	}

	runID := msg.RunID
	// Same boundary, same rule for the optional run correlator: an empty
	// RunID means the caller supplied none, anything else must be the
	// canonical UUID shape `harness run` mints.
	if !agent.OptionalUUIDPattern.MatchString(runID) {
		return ErrInvalidRunID
	}

	accept := ws.AgentCoordinator.BeginAccepted(sessionID)

	ws.runMu.Lock()
	if ws.closing {
		ws.runMu.Unlock()
		accept.Close()
		return ErrWorkspaceClosing
	}
	ws.runWG.Add(1)
	ws.runMu.Unlock()

	go func() {
		// Mirror runAgent's error fallback so a RunID waiter blocked on
		// the terminal RunComplete observes a deterministic failure
		// instead of hanging when the run panics.
		defer crash.Recover("backend.agentRun", func() {
			ws.AgentNotifications().Publish(pubsub.CreatedEvent, notify.Notification{
				SessionID: sessionID,
				RunID:     runID,
				Type:      notify.TypeAgentError,
				Message:   "agent run panicked; see the crash report",
			})
			if runID == "" {
				return
			}
			if rc := ws.RunCompletions(); rc != nil {
				rc.PublishMustDeliver(ws.ctx, pubsub.UpdatedEvent, notify.RunComplete{
					SessionID: sessionID,
					RunID:     runID,
					Error:     "agent run panicked; see the crash report",
				})
			}
		})
		b.runAgent(ws, msg, sessionID, runID, accept)
	}()
	return nil
}

// runAgent executes an accepted agent run for the workspace. It owns the
// accept reservation (releasing it on return) and the runWG ticket added
// by SendMessage. The run is bound to the workspace context so its
// lifetime is independent of any client's HTTP request.
//
// On a non-cancel error it surfaces the failure to observers via a
// notify.TypeAgentError notification (lossy, best-effort). That alone is
// not a reliable terminal signal: the agent-event fan-in uses lossy
// subscribers, so a `harness run` caller blocking on its RunID could hang
// if the event is dropped. To guarantee termination, when msg.RunID is
// non-empty and the coordinator did not already publish the run's
// authoritative terminal RunComplete (e.g. the error was returned before
// sessionAgent.Run executed, such as a readyWg or UpdateModels failure),
// runAgent emits an errored RunComplete on the must-deliver
// runCompletions broker so the waiter observes a deterministic terminal
// event. context.Canceled is expected (sessionAgent.Run already
// publishes the cancelled terminal marker) and produces no error
// terminal event.
//
// When msg.RunID is non-empty it is attached to the context via
// agent.WithRunID so the coordinator can stamp the terminal
// notify.RunComplete event with that correlator. A run-complete marker
// is also attached so the coordinator can report whether it published
// the terminal event, letting runAgent avoid a duplicate fallback.
func (b *Backend) runAgent(ws *Workspace, msg proto.AgentMessage, sessionID, runID string, accept *agent.AcceptedRun) {
	defer ws.runWG.Done()
	defer accept.Close()

	ctx := ws.ctx
	if runID != "" {
		ctx = agent.WithRunID(ctx, runID)
	}
	ctx = agent.WithRunCompleteMarker(ctx)

	_, err := ws.AgentCoordinator.RunAccepted(ctx, accept, sessionID, msg.Prompt, proto.AttachmentsToMessage(msg.Attachments)...)
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}

	ws.AgentNotifications().Publish(pubsub.CreatedEvent, notify.Notification{
		SessionID: sessionID,
		RunID:     runID,
		Type:      notify.TypeAgentError,
		Message:   err.Error(),
	})

	// Reliable terminal fallback. Only needed when a RunID waiter
	// exists and the coordinator has not already emitted the run's
	// terminal RunComplete; otherwise this would be a duplicate.
	if runID == "" || agent.RunCompletePublished(ctx) {
		return
	}
	if rc := ws.RunCompletions(); rc != nil {
		rc.PublishMustDeliver(ctx, pubsub.UpdatedEvent, notify.RunComplete{
			SessionID: sessionID,
			RunID:     runID,
			Error:     err.Error(),
		})
	}
}
