package proto

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/session"
)

func declaredPayloadTypes(t *testing.T) []pubsub.PayloadType {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../pubsub/events.go", nil, 0)
	require.NoError(t, err)
	var tags []pubsub.PayloadType
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "PayloadType" {
				continue
			}
			for _, v := range vs.Values {
				tag, err := strconv.Unquote(v.(*ast.BasicLit).Value)
				require.NoError(t, err)
				tags = append(tags, tag)
			}
		}
	}
	require.NotEmpty(t, tags)
	return tags
}

func TestEveryPayloadTypeHasExactlyOneCodec(t *testing.T) {
	t.Parallel()
	declared := declaredPayloadTypes(t)
	counts := make(map[pubsub.PayloadType]int)
	for _, c := range eventCodecs {
		counts[c.tag]++
	}
	for _, tag := range declared {
		require.Equal(t, 1, counts[tag], "payload type %q must have exactly one codec", tag)
	}
	require.ElementsMatch(t, declared, func() []pubsub.PayloadType {
		tags := make([]pubsub.PayloadType, len(eventCodecs))
		for i, c := range eventCodecs {
			tags[i] = c.tag
		}
		return tags
	}(), "every codec tag must be a declared pubsub.PayloadType")
	require.Len(t, codecsByDomain, len(eventCodecs))
	require.Len(t, codecsByWire, len(eventCodecs))
}

var wireOnlyFixups = map[reflect.Type]func(reflect.Value){
	reflect.TypeFor[pubsub.Event[session.Session]](): func(v reflect.Value) {
		s := v.Addr().Interface().(*pubsub.Event[session.Session])
		s.Payload.EstimatedUsage = false
		s.Payload.CompactionSummary, s.Payload.CompactionBoundaryID, s.Payload.CompactionAgedID = "", "", ""
	},
	reflect.TypeFor[pubsub.Event[message.Message]](): func(v reflect.Value) {
		v.Addr().Interface().(*pubsub.Event[message.Message]).Payload.Parts = []message.ContentPart{message.TextContent{Text: "hi"}}
	},
	reflect.TypeFor[pubsub.Event[notify.Notification]](): func(v reflect.Value) {
		v.Addr().Interface().(*pubsub.Event[notify.Notification]).Payload.ProviderID = ""
	},
	reflect.TypeFor[pubsub.Event[mcp.Event]](): func(v reflect.Value) {
		e := v.Addr().Interface().(*pubsub.Event[mcp.Event])
		e.Payload.Type = mcp.EventToolsListChanged
		e.Payload.ChannelMessage = ""
		e.Payload.State = mcp.StateConnected
	},
	reflect.TypeFor[pubsub.Event[lsp.Event]](): func(v reflect.Value) {
		v.Addr().Interface().(*pubsub.Event[lsp.Event]).Payload.Error = errors.New("crashed")
	},
}

func TestEveryCodecRoundTripsOverTheWire(t *testing.T) {
	t.Parallel()
	for _, c := range eventCodecs {
		t.Run(c.tag, func(t *testing.T) {
			t.Parallel()
			v := reflect.New(c.domainType).Elem()
			(&filler{}).fill(v)
			if f := v.FieldByName("MustDeliver"); f.IsValid() {
				f.SetBool(false)
			}
			if fix, ok := wireOnlyFixups[c.domainType]; ok {
				fix(v)
			}
			msg := v.Interface()

			frame, err := EncodeEvent(msg)
			require.NoError(t, err)

			wire, err := DecodeEvent(frame)
			require.NoError(t, err)
			require.Equal(t, c.wireType, reflect.TypeOf(wire))

			got, err := EventToDomain(wire)
			require.NoError(t, err)
			require.Equal(t, msg, got)
		})
	}
}

func TestEncodeEventRejectsUnknownAndWirelessEvents(t *testing.T) {
	t.Parallel()
	_, err := EncodeEvent(struct{}{})
	require.ErrorIs(t, err, ErrUnknownEvent)

	_, err = EncodeEvent(pubsub.Event[mcp.Event]{Payload: mcp.Event{Type: mcp.EventChannelMessage}})
	require.ErrorIs(t, err, ErrNoWireForm)

	_, err = DecodeEvent([]byte(`{"type":"nope","payload":{}}`))
	require.ErrorIs(t, err, ErrUnknownEventTag)

	_, err = EventToDomain(struct{}{})
	require.ErrorIs(t, err, ErrUnknownEvent)
}

func TestDecodeEventReportsMalformedFrames(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		frame string
		want  error
	}{
		"truncated payload":     {frame: `{"type":"lsp_event","payload":`},
		"payload before type":   {frame: `{"payload":{},"type":"lsp_event"}`, want: ErrPayloadBeforeType},
		"no payload":            {frame: `{"type":"lsp_event"}`, want: ErrNoPayload},
		"non-string type":       {frame: `{"type":1,"payload":{}}`},
		"not an object":         {frame: `[]`},
		"trailing garbage":      {frame: `{"type":"lsp_event","payload":{}} x`},
		"part data before type": {frame: `{"type":"message","payload":{"type":"updated","payload":{"parts":[{"data":{},"type":"text"}]}}}`, want: ErrPartDataBeforeType},
		"part without data":     {frame: `{"type":"message","payload":{"type":"updated","payload":{"parts":[{"type":"text"}]}}}`, want: ErrPartWithoutData},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeEvent([]byte(tc.frame))
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrUnknownEventTag)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
		})
	}
}

func TestDecodeEventSkipsUnknownFramesKeysAndParts(t *testing.T) {
	t.Parallel()
	wire, err := DecodeEvent([]byte(`{"id":7,"type":"message","payload":{"type":"updated","payload":{"id":"m1","parts":[{"type":"hologram","data":{"x":1}},{"type":"text","extra":[1],"data":{"text":"hi"}}]}}}`))
	require.NoError(t, err)
	require.Equal(t, pubsub.Event[Message]{
		Type:    pubsub.UpdatedEvent,
		Payload: Message{ID: "m1", Parts: []ContentPart{TextContent{Text: "hi"}}},
	}, wire)
}

func TestLSPEventErrorCrossesTheWire(t *testing.T) {
	t.Parallel()
	frame, err := EncodeEvent(pubsub.Event[lsp.Event]{
		Type:    pubsub.UpdatedEvent,
		Payload: lsp.Event{Type: lsp.EventStateChanged, Name: "gopls", State: lsp.StateError, Error: errors.New("boom")},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"lsp_event","payload":{"type":"updated","payload":{"type":"state_changed","name":"gopls","state":3,"error":"boom"}}}`, string(frame))
}
