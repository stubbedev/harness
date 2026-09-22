// Package agentstate translates Harness pub/sub events into a small
// neutral agent-lifecycle vocabulary (assistant output, run completion,
// summarization) that terminal-multiplexer integrations consume. It is
// the single translation point for every integration mode: herdr, tmux,
// and any future reporter all share Translate and BridgeLocal instead
// of each re-deriving state from raw domain and proto events.
package agentstate

import (
	"context"
	"time"

	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/crash"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
)

// Event is the integration-facing event vocabulary. Each type maps to
// a distinct transition in the agent lifecycle.
type Event interface {
	agentStateEvent()
}

// AssistantMessage indicates the agent produced output. Transitions
// to working if not already active.
type AssistantMessage struct {
	SessionID string
}

func (AssistantMessage) agentStateEvent() {}

// RunComplete indicates the agent finished a turn. Transitions to
// idle, or to an error state when the run failed.
type RunComplete struct {
	SessionID string
	Error     string
}

func (RunComplete) agentStateEvent() {}

// Summarizing indicates the agent is compacting context. Transitions
// to working if not already active.
type Summarizing struct{}

func (Summarizing) agentStateEvent() {}

// Translate converts a pub/sub event (domain or proto) into an Event.
// Returns nil for event types integrations don't care about.
func Translate(ev any) Event {
	switch e := ev.(type) {
	// Domain types (TUI / local headless).
	case pubsub.Event[message.Message]:
		return translateMessage(
			e.Payload.Role == message.Assistant,
			e.Payload.SessionID,
			e.Payload.IsSummaryMessage,
		)
	case pubsub.Event[notify.RunComplete]:
		return RunComplete{SessionID: e.Payload.SessionID, Error: e.Payload.Error}

	// Proto types (client/server mode).
	case pubsub.Event[proto.Message]:
		return translateMessage(
			e.Payload.Role == proto.Assistant,
			e.Payload.SessionID,
			false,
		)
	case pubsub.Event[proto.RunComplete]:
		return RunComplete{SessionID: e.Payload.SessionID, Error: e.Payload.Error}
	case pubsub.Event[proto.AgentEvent]:
		if e.Payload.Type == proto.AgentEventTypeSummarize && !e.Payload.Done {
			return Summarizing{}
		}
		return nil

	default:
		return nil
	}
}

// translateMessage is the shared message-mapping logic for both domain
// and proto message types.
func translateMessage(isAssistant bool, sessionID string, isSummary bool) Event {
	if !isAssistant {
		return nil
	}
	if isSummary {
		return Summarizing{}
	}
	return AssistantMessage{SessionID: sessionID}
}

// BridgeSources groups the pub/sub sources that BridgeLocal subscribes
// to. Adding a new event type means adding a field here rather than
// growing the function signature.
type BridgeSources struct {
	RunCompletions pubsub.Subscriber[notify.RunComplete]
	Messages       pubsub.Subscriber[message.Message]
}

// BridgeLocal subscribes to local pub/sub brokers and forwards
// translated events to handle. Used in TUI and local headless modes
// where the agent runs in-process. Cancelling ctx stops the bridge
// goroutines. One bridge can feed any number of integrations.
//
// Each goroutine uses a resilient subscription loop that re-subscribes
// if the channel closes unexpectedly, ensuring the bridge survives
// transient pub/sub broker resets.
func BridgeLocal(ctx context.Context, handle func(Event), src BridgeSources) {
	if handle == nil {
		return
	}
	crash.Go("agentstate.runCompletions", func() {
		forward(ctx, handle, func(subCtx context.Context) <-chan pubsub.Event[notify.RunComplete] {
			return src.RunCompletions.Subscribe(subCtx)
		})
	})
	crash.Go("agentstate.messages", func() {
		forward(ctx, handle, func(subCtx context.Context) <-chan pubsub.Event[message.Message] {
			return src.Messages.Subscribe(subCtx)
		})
	})
}

// forward reads from a pub/sub channel and forwards translated events
// to handle. If the channel closes (e.g., due to broker reset), it
// re-subscribes after a brief delay. Runs until ctx is cancelled.
func forward[T any](ctx context.Context, handle func(Event), subscribe func(context.Context) <-chan pubsub.Event[T]) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		subCtx, cancel := context.WithCancel(ctx)
		ch := subscribe(subCtx)

	inner:
		for {
			select {
			case <-ctx.Done():
				cancel()
				return
			case ev, ok := <-ch:
				if !ok {
					// Channel closed — broker may have reset.
					// Cancel the sub-context and re-subscribe.
					cancel()
					time.Sleep(100 * time.Millisecond)
					break inner
				}
				if sev := Translate(ev); sev != nil {
					handle(sev)
				}
			}
		}
	}
}
