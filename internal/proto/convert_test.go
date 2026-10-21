package proto

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/skills"
	"github.com/stubbedev/harness/internal/update"
)

// filler sets every exported field it can reach to a distinct non-zero
// value, so a round trip that drops a field shows up as a mismatch.
type filler struct{ n int }

func filled[T any](f *filler) T {
	var v T
	f.fill(reflect.ValueOf(&v).Elem())
	return v
}

func (f *filler) fill(v reflect.Value) {
	f.n++
	switch v.Kind() {
	case reflect.String:
		v.SetString("v" + string(rune('a'+f.n%26)) + time.Duration(f.n).String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(f.n))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(f.n))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(f.n) + 0.5)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		f.fill(p.Elem())
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		f.fill(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		e := reflect.New(v.Type().Elem()).Elem()
		f.fill(k)
		f.fill(e)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Unix(int64(f.n), 0).UTC()))
			return
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				f.fill(v.Field(i))
			}
		}
	}
	// Interfaces, channels and funcs are left zero; tests set the
	// interface-typed fields they care about by hand.
}

func TestSessionRoundTrip(t *testing.T) {
	t.Parallel()
	d := filled[session.Session](&filler{})
	// Server-only state.
	d.EstimatedUsage = false
	d.CompactionSummary, d.CompactionBoundaryID, d.CompactionAgedID = "", "", ""
	require.Equal(t, d, SessionFromDomain(d).ToDomain())
}

func TestFileRoundTrip(t *testing.T) {
	t.Parallel()
	d := filled[history.File](&filler{})
	require.Equal(t, d, FileFromDomain(d).ToDomain())
}

func TestCheckpointRoundTrip(t *testing.T) {
	t.Parallel()
	d := filled[checkpoints.Checkpoint](&filler{})
	require.Equal(t, d, CheckpointFromDomain(d).ToDomain())
}

func TestMessageRoundTrip(t *testing.T) {
	t.Parallel()
	f := &filler{}
	d := filled[message.Message](f)
	d.Parts = nil
	for _, zero := range message.PartTypes() {
		d.Parts = append(d.Parts, filledPart(f, zero))
	}
	require.Equal(t, d, MessageFromDomain(d).ToDomain())
}

// TestEveryDomainPartCrossesTheWire walks every part type the message
// store knows through the wire conversion and the wire JSON codec, so a
// new part cannot be silently dropped between server and client.
func TestEveryDomainPartCrossesTheWire(t *testing.T) {
	t.Parallel()
	for _, zero := range message.PartTypes() {
		part := filledPart(&filler{}, zero)
		t.Run(fmt.Sprintf("%T", part), func(t *testing.T) {
			t.Parallel()
			wire := partFromDomain(part)
			require.NotNil(t, wire, "no wire form for %T", part)

			encoded, err := json.Marshal(Message{Parts: []ContentPart{wire}})
			require.NoError(t, err)
			var decoded Message
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			require.Len(t, decoded.Parts, 1, "wire codec dropped %T", wire)
			require.Equal(t, part, partToDomain(decoded.Parts[0]))
		})
	}
}

// filledPart returns a part of zero's type with every wire-visible field
// set. Provider-private replay metadata on reasoning stays on the server.
func filledPart(f *filler, zero message.ContentPart) message.ContentPart {
	v := reflect.New(reflect.TypeOf(zero)).Elem()
	f.fill(v)
	part := v.Interface().(message.ContentPart)
	if reasoning, ok := part.(message.ReasoningContent); ok {
		reasoning.ThoughtSignature, reasoning.ToolID, reasoning.ResponsesData = "", "", nil
		return reasoning
	}
	return part
}

func TestQuestionRoundTrip(t *testing.T) {
	t.Parallel()
	f := &filler{}
	req := filled[question.Request](f)
	require.Equal(t, req, QuestionRequestFromDomain(req).ToDomain())

	answers := filled[[]question.Answer](f)
	require.Equal(t, answers, QuestionResponsesToDomain(QuestionResponsesFromDomain(answers)))
}

func TestSkillStatesRoundTrip(t *testing.T) {
	t.Parallel()
	d := filled[[]*skills.SkillState](&filler{})
	d[0].Err = errors.New("broken")
	got := SkillStatesToDomain(SkillStatesFromDomain(d))
	require.Len(t, got, 1)
	require.EqualError(t, got[0].Err, "broken")
	got[0].Err = d[0].Err
	require.Equal(t, d, got)
}

func TestSkillCatalogRoundTrip(t *testing.T) {
	t.Parallel()
	f := &filler{}
	entry := filled[skills.CatalogEntry](f)
	require.Equal(t, entry, SkillInfoFromDomain(entry).ToDomain())

	result := filled[skills.SkillReadResult](f)
	require.Equal(t, result, SkillReadResultFromDomain(result).ToDomain())
}

func TestMCPRoundTrip(t *testing.T) {
	t.Parallel()
	f := &filler{}

	info := filled[mcp.ClientInfo](f)
	// The live session and connect-time configs stay on the server.
	info.Client, info.Config, info.PendingConfig = nil, config.MCPConfig{}, nil
	require.Equal(t, info, MCPClientInfoFromDomain(info).ToDomain())

	ev := filled[mcp.Event](f)
	ev.Type = mcp.EventResourcesListChanged
	ev.ChannelMessage = ""
	wire, ok := MCPEventFromDomain(ev)
	require.True(t, ok)
	require.Equal(t, ev, wire.ToDomain())

	_, ok = MCPEventFromDomain(mcp.Event{Type: mcp.EventChannelMessage})
	require.False(t, ok, "channel messages have no wire form")
}

func TestNotifyRoundTrip(t *testing.T) {
	t.Parallel()
	f := &filler{}
	n := filled[notify.Notification](f)
	n.ProviderID = "" // Re-authentication runs on the server.
	require.Equal(t, n, AgentEventFromDomain(n).ToDomain())

	rc := filled[notify.RunComplete](f)
	require.Equal(t, rc, RunCompleteFromDomain(rc).ToDomain())
}

func TestLSPRoundTrip(t *testing.T) {
	t.Parallel()
	f := &filler{}

	ev := filled[lsp.Event](f)
	ev.Error = errors.New("crashed")
	gotEv := LSPEventFromDomain(ev).ToDomain()
	require.EqualError(t, gotEv.Error, "crashed")
	gotEv.Error = ev.Error
	require.Equal(t, ev, gotEv)

	info := filled[lsp.ClientInfo](f)
	info.Error = errors.New("stopped")
	gotInfo := LSPClientInfoFromDomain(info).ToDomain()
	require.EqualError(t, gotInfo.Error, "stopped")
	gotInfo.Error = info.Error
	require.Equal(t, info, gotInfo)

	clean := filled[lsp.ClientInfo](f)
	require.Equal(t, map[string]lsp.ClientInfo{"x": clean},
		LSPClientInfosToDomain(map[string]LSPClientInfo{"x": LSPClientInfoFromDomain(clean)}))
}

func TestCommandListingsRoundTrip(t *testing.T) {
	t.Parallel()
	f := &filler{}

	prompts := filled[[]commands.MCPPrompt](f)
	require.Equal(t, prompts, MCPPromptsToDomain(mapSlice(prompts, MCPPromptFromDomain)))

	cmds := filled[[]extensions.Command](f)
	require.Equal(t, cmds, ExtensionCommandsToDomain(mapSlice(cmds, ExtensionCommandFromDomain)))

	pending := filled[[]mcp.PendingAuthServer](f)
	require.Equal(t, pending, MCPPendingAuthServersToDomain(mapSlice(pending, MCPPendingAuthServerFromDomain)))
}

func TestEventPayloadsRoundTrip(t *testing.T) {
	t.Parallel()
	f := &filler{}

	n := filled[question.Notification](f)
	require.Equal(t, n, QuestionNotificationFromDomain(n).ToDomain())

	u := filled[update.AvailableMsg](f)
	require.Equal(t, u, UpdateAvailableFromDomain(u).ToDomain())

	e := filled[skills.Event](f)
	require.Equal(t, e, SkillsEventFromDomain(e).ToDomain())
}

func TestSkillStatesFromDomainKeepsNilAndEmptyDistinct(t *testing.T) {
	t.Parallel()
	require.Nil(t, SkillStatesFromDomain(nil))
	require.Equal(t, []SkillState{}, SkillStatesFromDomain([]*skills.SkillState{}))
}
