package agentstate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
)

// Domain type translation.

func TestTranslateDomainAssistantMessage(t *testing.T) {
	t.Parallel()
	ev := pubsub.Event[message.Message]{
		Payload: message.Message{Role: message.Assistant, SessionID: "s1"},
	}
	assert.Equal(t, AssistantMessage{SessionID: "s1"}, Translate(ev))
}

func TestTranslateDomainSummaryMessage(t *testing.T) {
	t.Parallel()
	ev := pubsub.Event[message.Message]{
		Payload: message.Message{
			Role:             message.Assistant,
			SessionID:        "s1",
			IsSummaryMessage: true,
		},
	}
	assert.Equal(t, Summarizing{}, Translate(ev))
}

func TestTranslateDomainNonAssistantIgnored(t *testing.T) {
	t.Parallel()
	ev := pubsub.Event[message.Message]{
		Payload: message.Message{Role: message.System},
	}
	assert.Nil(t, Translate(ev))
}

func TestTranslateDomainRunComplete(t *testing.T) {
	t.Parallel()
	ev := pubsub.Event[notify.RunComplete]{
		Payload: notify.RunComplete{SessionID: "s1"},
	}
	assert.Equal(t, RunComplete{SessionID: "s1"}, Translate(ev))
}

// Proto type translation.

func TestTranslateProtoAssistantMessage(t *testing.T) {
	t.Parallel()
	ev := pubsub.Event[proto.Message]{
		Payload: proto.Message{Role: proto.Assistant, SessionID: "s1"},
	}
	assert.Equal(t, AssistantMessage{SessionID: "s1"}, Translate(ev))
}

func TestTranslateProtoNonAssistantIgnored(t *testing.T) {
	t.Parallel()
	ev := pubsub.Event[proto.Message]{
		Payload: proto.Message{Role: proto.User, SessionID: "s1"},
	}
	assert.Nil(t, Translate(ev))
}

func TestTranslateProtoRunComplete(t *testing.T) {
	t.Parallel()
	ev := pubsub.Event[proto.RunComplete]{
		Payload: proto.RunComplete{SessionID: "s1"},
	}
	assert.Equal(t, RunComplete{SessionID: "s1"}, Translate(ev))
}

func TestTranslateProtoSummarizing(t *testing.T) {
	t.Parallel()
	ev := pubsub.Event[proto.AgentEvent]{
		Payload: proto.AgentEvent{
			Type: proto.AgentEventTypeSummarize,
			Done: false,
		},
	}
	assert.Equal(t, Summarizing{}, Translate(ev))
}

func TestTranslateProtoSummarizeDoneIgnored(t *testing.T) {
	t.Parallel()
	ev := pubsub.Event[proto.AgentEvent]{
		Payload: proto.AgentEvent{
			Type: proto.AgentEventTypeSummarize,
			Done: true,
		},
	}
	assert.Nil(t, Translate(ev))
}

func TestTranslateProtoSessionIgnored(t *testing.T) {
	t.Parallel()
	ev := pubsub.Event[proto.Session]{
		Payload: proto.Session{ID: "s1"},
	}
	assert.Nil(t, Translate(ev))
}

// Unknown types.

func TestTranslateUnknownReturnsNil(t *testing.T) {
	t.Parallel()
	assert.Nil(t, Translate("not an event"))
}
