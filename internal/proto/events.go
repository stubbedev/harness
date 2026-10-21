package proto

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/update"
)

var (
	ErrUnknownEvent    = errors.New("unknown event")
	ErrNoWireForm      = errors.New("event has no wire form")
	ErrUnknownEventTag = errors.New("unknown event payload type")
)

type eventCodec struct {
	tag        pubsub.PayloadType
	domainType reflect.Type
	wireType   reflect.Type
	wrap       func(any) (any, bool)
	decode     func(json.RawMessage) (any, error)
	toDomain   func(any) any
}

func codec[M, W any](tag pubsub.PayloadType, fromDomain func(M) (W, bool), toDomain func(W) M) eventCodec {
	return eventCodec{
		tag:        tag,
		domainType: reflect.TypeFor[M](),
		wireType:   reflect.TypeFor[W](),
		wrap: func(m any) (any, bool) {
			return fromDomain(m.(M))
		},
		decode: func(raw json.RawMessage) (any, error) {
			var w W
			if err := json.Unmarshal(raw, &w); err != nil {
				return nil, err
			}
			return w, nil
		},
		toDomain: func(w any) any {
			return toDomain(w.(W))
		},
	}
}

func partialEventCodec[D, P any](tag pubsub.PayloadType, fromDomain func(D) (P, bool), toDomain func(P) D) eventCodec {
	return codec(tag,
		func(e pubsub.Event[D]) (pubsub.Event[P], bool) {
			p, ok := fromDomain(e.Payload)
			return pubsub.Event[P]{Type: e.Type, Payload: p}, ok
		},
		func(e pubsub.Event[P]) pubsub.Event[D] {
			return pubsub.Event[D]{Type: e.Type, Payload: toDomain(e.Payload)}
		},
	)
}

func eventCodecOf[D, P any](tag pubsub.PayloadType, fromDomain func(D) P, toDomain func(P) D) eventCodec {
	return partialEventCodec(tag, func(d D) (P, bool) { return fromDomain(d), true }, toDomain)
}

func identity[T any](v T) T { return v }

var eventCodecs = []eventCodec{
	eventCodecOf(pubsub.PayloadTypeLSPEvent, LSPEventFromDomain, LSPEvent.ToDomain),
	partialEventCodec(pubsub.PayloadTypeMCPEvent, MCPEventFromDomain, MCPEvent.ToDomain),
	eventCodecOf(pubsub.PayloadTypeQuestionRequest, QuestionRequestFromDomain, QuestionRequest.ToDomain),
	eventCodecOf(pubsub.PayloadTypeQuestionNotification, QuestionNotificationFromDomain, QuestionNotification.ToDomain),
	eventCodecOf(pubsub.PayloadTypeMessage, MessageFromDomain, Message.ToDomain),
	eventCodecOf(pubsub.PayloadTypeSession, SessionFromDomain, Session.ToDomain),
	eventCodecOf(pubsub.PayloadTypeFile, FileFromDomain, File.ToDomain),
	eventCodecOf(pubsub.PayloadTypeAgentEvent, AgentEventFromDomain, AgentEvent.ToDomain),
	eventCodecOf(pubsub.PayloadTypeRunComplete, RunCompleteFromDomain, RunComplete.ToDomain),
	eventCodecOf(pubsub.PayloadTypeConfigChanged, identity[ConfigChanged], identity[ConfigChanged]),
	eventCodecOf(pubsub.PayloadTypeSkillsEvent, SkillsEventFromDomain, SkillsEvent.ToDomain),
	codec(pubsub.PayloadTypeUpdateAvailable,
		func(m update.AvailableMsg) (pubsub.Event[UpdateAvailable], bool) {
			return pubsub.Event[UpdateAvailable]{Type: pubsub.UpdatedEvent, Payload: UpdateAvailableFromDomain(m)}, true
		},
		func(e pubsub.Event[UpdateAvailable]) update.AvailableMsg { return e.Payload.ToDomain() },
	),
}

var (
	codecsByTag    = indexCodecs(func(c eventCodec) pubsub.PayloadType { return c.tag })
	codecsByDomain = indexCodecs(func(c eventCodec) reflect.Type { return c.domainType })
	codecsByWire   = indexCodecs(func(c eventCodec) reflect.Type { return c.wireType })
)

func indexCodecs[K comparable](key func(eventCodec) K) map[K]eventCodec {
	index := make(map[K]eventCodec, len(eventCodecs))
	for _, c := range eventCodecs {
		k := key(c)
		if _, dup := index[k]; dup {
			panic(fmt.Sprintf("proto: duplicate event codec key %v", k))
		}
		index[k] = c
	}
	return index
}

func WrapEvent(msg any) (*pubsub.Payload, error) {
	c, ok := codecsByDomain[reflect.TypeOf(msg)]
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnknownEvent, msg)
	}
	wire, ok := c.wrap(msg)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrNoWireForm, msg)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", c.tag, err)
	}
	return &pubsub.Payload{Type: c.tag, Payload: raw}, nil
}

func DecodeEvent(p pubsub.Payload) (any, error) {
	c, ok := codecsByTag[p.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEventTag, p.Type)
	}
	return c.decode(p.Payload)
}

func EventToDomain(wire any) (any, error) {
	c, ok := codecsByWire[reflect.TypeOf(wire)]
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnknownEvent, wire)
	}
	return c.toDomain(wire), nil
}
