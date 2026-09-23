package proto

import (
	"errors"

	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/skills"
)

// This file holds both directions of every domain <-> wire conversion
// the server and the client workspace share. Keeping each pair side by
// side, with a round-trip test over every field, is what stops the two
// ends from drifting: a field added to a domain type fails the test
// until it is either carried or explicitly listed as server-only.

// mapSlice converts every element of in with f, keeping nil as nil.
func mapSlice[T, U any](in []T, f func(T) U) []U {
	if in == nil {
		return nil
	}
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func stringError(s string) error {
	if s == "" {
		return nil
	}
	return errors.New(s)
}

// SessionFromDomain converts a session for the wire. Compaction state
// and the estimated-usage flag stay on the server: clients never send
// them to the model.
func SessionFromDomain(s session.Session) Session {
	return Session{
		ID:               s.ID,
		ParentSessionID:  s.ParentSessionID,
		Title:            s.Title,
		SummaryMessageID: s.SummaryMessageID,
		MessageCount:     s.MessageCount,
		PromptTokens:     s.PromptTokens,
		CompletionTokens: s.CompletionTokens,
		Cost:             s.Cost,
		Todos:            mapSlice(s.Todos, todoFromDomain),
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        s.UpdatedAt,
	}
}

// ToDomain converts a wire session back. The read-time signals IsBusy
// and AttachedClients are dropped: session.Session models stored state.
func (s Session) ToDomain() session.Session {
	return session.Session{
		ID:               s.ID,
		ParentSessionID:  s.ParentSessionID,
		Title:            s.Title,
		SummaryMessageID: s.SummaryMessageID,
		MessageCount:     s.MessageCount,
		PromptTokens:     s.PromptTokens,
		CompletionTokens: s.CompletionTokens,
		Cost:             s.Cost,
		Todos:            mapSlice(s.Todos, Todo.toDomain),
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        s.UpdatedAt,
	}
}

func todoFromDomain(t session.Todo) Todo {
	return Todo{Content: t.Content, Status: string(t.Status), ActiveForm: t.ActiveForm}
}

func (t Todo) toDomain() session.Todo {
	return session.Todo{Content: t.Content, Status: session.TodoStatus(t.Status), ActiveForm: t.ActiveForm}
}

// FileFromDomain converts a history file for the wire.
func FileFromDomain(f history.File) File {
	return File{
		ID:        f.ID,
		SessionID: f.SessionID,
		Path:      f.Path,
		Content:   f.Content,
		Version:   f.Version,
		CreatedAt: f.CreatedAt,
		UpdatedAt: f.UpdatedAt,
	}
}

// ToDomain converts a wire history file back.
func (f File) ToDomain() history.File {
	return history.File{
		ID:        f.ID,
		SessionID: f.SessionID,
		Path:      f.Path,
		Content:   f.Content,
		Version:   f.Version,
		CreatedAt: f.CreatedAt,
		UpdatedAt: f.UpdatedAt,
	}
}

// FilesFromDomain converts history files for the wire.
func FilesFromDomain(fs []history.File) []File { return mapSlice(fs, FileFromDomain) }

// FilesToDomain converts wire history files back.
func FilesToDomain(fs []File) []history.File { return mapSlice(fs, File.ToDomain) }

// CheckpointFromDomain converts a rewind checkpoint for the wire.
func CheckpointFromDomain(c checkpoints.Checkpoint) Checkpoint {
	return Checkpoint(c)
}

// ToDomain converts a wire checkpoint back.
func (c Checkpoint) ToDomain() checkpoints.Checkpoint {
	return checkpoints.Checkpoint(c)
}

// CheckpointsFromDomain converts rewind checkpoints for the wire.
func CheckpointsFromDomain(cs []checkpoints.Checkpoint) []Checkpoint {
	return mapSlice(cs, CheckpointFromDomain)
}

// CheckpointsToDomain converts wire checkpoints back.
func CheckpointsToDomain(cs []Checkpoint) []checkpoints.Checkpoint {
	return mapSlice(cs, Checkpoint.ToDomain)
}

// SessionsToDomain converts wire sessions back.
func SessionsToDomain(ss []Session) []session.Session { return mapSlice(ss, Session.ToDomain) }

// MessageFromDomain converts a message for the wire. Provider-private
// reasoning metadata (thought signatures, Responses API data) and the
// provider-executed flag stay on the server, which is the only side
// that replays history to a model.
func MessageFromDomain(m message.Message) Message {
	out := Message{
		ID:                      m.ID,
		SessionID:               m.SessionID,
		Role:                    MessageRole(m.Role),
		Model:                   m.Model,
		Provider:                m.Provider,
		PrismModelID:            m.PrismModelID,
		PrismModelName:          m.PrismModelName,
		PrismHypercreditSavings: m.PrismHypercreditSavings,
		PrismDollarSavings:      m.PrismDollarSavings,
		CreatedAt:               m.CreatedAt,
		UpdatedAt:               m.UpdatedAt,
		IsSummaryMessage:        m.IsSummaryMessage,
	}
	for _, p := range m.Parts {
		if part := partFromDomain(p); part != nil {
			out.Parts = append(out.Parts, part)
		}
	}
	return out
}

// ToDomain converts a wire message back.
func (m Message) ToDomain() message.Message {
	out := message.Message{
		ID:                      m.ID,
		SessionID:               m.SessionID,
		Role:                    message.MessageRole(m.Role),
		Model:                   m.Model,
		Provider:                m.Provider,
		PrismModelID:            m.PrismModelID,
		PrismModelName:          m.PrismModelName,
		PrismHypercreditSavings: m.PrismHypercreditSavings,
		PrismDollarSavings:      m.PrismDollarSavings,
		CreatedAt:               m.CreatedAt,
		UpdatedAt:               m.UpdatedAt,
		IsSummaryMessage:        m.IsSummaryMessage,
	}
	for _, p := range m.Parts {
		if part := partToDomain(p); part != nil {
			out.Parts = append(out.Parts, part)
		}
	}
	return out
}

// MessagesFromDomain converts messages for the wire.
func MessagesFromDomain(ms []message.Message) []Message {
	return mapSlice(ms, MessageFromDomain)
}

// MessagesToDomain converts wire messages back.
func MessagesToDomain(ms []Message) []message.Message {
	return mapSlice(ms, Message.ToDomain)
}

func partFromDomain(p message.ContentPart) ContentPart {
	switch v := p.(type) {
	case message.TextContent:
		return TextContent{Text: v.Text}
	case message.ReasoningContent:
		return ReasoningContent{
			Thinking:   v.Thinking,
			Signature:  v.Signature,
			StartedAt:  v.StartedAt,
			FinishedAt: v.FinishedAt,
		}
	case message.ToolCall:
		return ToolCall{ID: v.ID, Name: v.Name, Input: v.Input, Finished: v.Finished}
	case message.ToolResult:
		return ToolResult(v)
	case message.Finish:
		return Finish{Reason: FinishReason(v.Reason), Time: v.Time, Message: v.Message, Details: v.Details}
	case message.ImageURLContent:
		return ImageURLContent(v)
	case message.BinaryContent:
		return BinaryContent(v)
	case message.ShellCommand:
		return ShellCommand(v)
	case message.SubagentNote:
		return SubagentNote(v)
	}
	return nil
}

func partToDomain(p ContentPart) message.ContentPart {
	switch v := p.(type) {
	case TextContent:
		return message.TextContent{Text: v.Text}
	case ReasoningContent:
		return message.ReasoningContent{
			Thinking:   v.Thinking,
			Signature:  v.Signature,
			StartedAt:  v.StartedAt,
			FinishedAt: v.FinishedAt,
		}
	case ToolCall:
		return message.ToolCall{ID: v.ID, Name: v.Name, Input: v.Input, Finished: v.Finished}
	case ToolResult:
		return message.ToolResult(v)
	case Finish:
		return message.Finish{Reason: message.FinishReason(v.Reason), Time: v.Time, Message: v.Message, Details: v.Details}
	case ImageURLContent:
		return message.ImageURLContent(v)
	case BinaryContent:
		return message.BinaryContent(v)
	case ShellCommand:
		return message.ShellCommand(v)
	case SubagentNote:
		return message.SubagentNote(v)
	}
	return nil
}

// QuestionRequestFromDomain converts a question batch for the wire.
func QuestionRequestFromDomain(r question.Request) QuestionRequest {
	return QuestionRequest{
		ID:                 r.ID,
		SessionID:          r.SessionID,
		ToolCallID:         r.ToolCallID,
		Questions:          mapSlice(r.Questions, questionFromDomain),
		ConfirmTitle:       r.ConfirmTitle,
		ConfirmDescription: r.ConfirmDescription,
	}
}

// ToDomain converts a wire question batch back.
func (r QuestionRequest) ToDomain() question.Request {
	return question.Request{
		ID:                 r.ID,
		SessionID:          r.SessionID,
		ToolCallID:         r.ToolCallID,
		Questions:          mapSlice(r.Questions, QuestionItem.toDomain),
		ConfirmTitle:       r.ConfirmTitle,
		ConfirmDescription: r.ConfirmDescription,
	}
}

func questionFromDomain(q question.Question) QuestionItem {
	return QuestionItem{
		ID:          q.ID,
		Type:        string(q.Type),
		Label:       q.Label,
		Question:    q.Text,
		Description: q.Description,
		Choices:     mapSlice(q.Choices, func(c question.Choice) QuestionChoice { return QuestionChoice(c) }),
		Secret:      q.Secret,
	}
}

func (q QuestionItem) toDomain() question.Question {
	return question.Question{
		ID:          q.ID,
		Type:        question.Type(q.Type),
		Label:       q.Label,
		Text:        q.Question,
		Description: q.Description,
		Choices:     mapSlice(q.Choices, func(c QuestionChoice) question.Choice { return question.Choice(c) }),
		Secret:      q.Secret,
	}
}

// QuestionResponsesFromDomain converts answers for the wire.
func QuestionResponsesFromDomain(as []question.Answer) []QuestionResponse {
	return mapSlice(as, func(a question.Answer) QuestionResponse { return QuestionResponse(a) })
}

// QuestionResponsesToDomain converts wire answers back.
func QuestionResponsesToDomain(rs []QuestionResponse) []question.Answer {
	return mapSlice(rs, func(r QuestionResponse) question.Answer { return question.Answer(r) })
}

// SkillStatesFromDomain converts skill discovery states for the wire.
// Errors are flattened to strings because error does not round-trip
// over JSON.
func SkillStatesFromDomain(states []*skills.SkillState) []SkillState {
	return mapSlice(states, func(s *skills.SkillState) SkillState {
		return SkillState{
			Name:  s.Name,
			Path:  s.Path,
			State: SkillDiscoveryState(s.State),
			Error: errorString(s.Err),
		}
	})
}

// SkillStatesToDomain converts wire skill states back. A non-empty
// Error becomes a synthetic error value; nothing type-asserts on it.
func SkillStatesToDomain(states []SkillState) []*skills.SkillState {
	return mapSlice(states, func(s SkillState) *skills.SkillState {
		return &skills.SkillState{
			Name:  s.Name,
			Path:  s.Path,
			State: skills.DiscoveryState(s.State),
			Err:   stringError(s.Error),
		}
	})
}

// SkillInfoFromDomain converts a skill catalog entry for the wire.
func SkillInfoFromDomain(e skills.CatalogEntry) SkillInfo {
	return SkillInfo{
		ID:            e.ID,
		Name:          e.Name,
		Description:   e.Description,
		Label:         e.Label,
		Source:        string(e.Source),
		UserInvocable: e.UserInvocable,
	}
}

// ToDomain converts a wire skill catalog entry back.
func (i SkillInfo) ToDomain() skills.CatalogEntry {
	return skills.CatalogEntry{
		ID:            i.ID,
		Name:          i.Name,
		Description:   i.Description,
		Label:         i.Label,
		Source:        skills.SourceType(i.Source),
		UserInvocable: i.UserInvocable,
	}
}

// SkillReadResultFromDomain converts skill read metadata for the wire.
func SkillReadResultFromDomain(r skills.SkillReadResult) SkillReadResult {
	return SkillReadResult{Name: r.Name, Description: r.Description, Source: string(r.Source), Builtin: r.Builtin}
}

// ToDomain converts wire skill read metadata back.
func (r SkillReadResult) ToDomain() skills.SkillReadResult {
	return skills.SkillReadResult{Name: r.Name, Description: r.Description, Source: skills.SourceType(r.Source), Builtin: r.Builtin}
}

// MCPClientInfoFromDomain converts an MCP server's state for the wire.
// The live session and the connect-time configs stay on the server.
func MCPClientInfoFromDomain(i mcp.ClientInfo) MCPClientInfo {
	return MCPClientInfo{
		Name:          i.Name,
		State:         MCPState(i.State),
		Error:         i.Error,
		ToolCount:     i.Counts.Tools,
		PromptCount:   i.Counts.Prompts,
		ResourceCount: i.Counts.Resources,
		ConnectedAt:   i.ConnectedAt,
	}
}

// ToDomain converts a wire MCP server state back.
func (i MCPClientInfo) ToDomain() mcp.ClientInfo {
	return mcp.ClientInfo{
		Name:        i.Name,
		State:       mcp.State(i.State),
		Error:       i.Error,
		Counts:      mcp.Counts{Tools: i.ToolCount, Prompts: i.PromptCount, Resources: i.ResourceCount},
		ConnectedAt: i.ConnectedAt,
	}
}

// MCPEventFromDomain converts an MCP event for the wire. It reports
// false for event types with no wire form (channel messages), which
// callers drop rather than coerce into a state change.
func MCPEventFromDomain(e mcp.Event) (MCPEvent, bool) {
	var t MCPEventType
	switch e.Type {
	case mcp.EventStateChanged:
		t = MCPEventStateChanged
	case mcp.EventToolsListChanged:
		t = MCPEventToolsListChanged
	case mcp.EventPromptsListChanged:
		t = MCPEventPromptsListChanged
	case mcp.EventResourcesListChanged:
		t = MCPEventResourcesListChanged
	default:
		return MCPEvent{}, false
	}
	return MCPEvent{
		Type:          t,
		Name:          e.Name,
		State:         MCPState(e.State),
		Error:         e.Error,
		ToolCount:     e.Counts.Tools,
		PromptCount:   e.Counts.Prompts,
		ResourceCount: e.Counts.Resources,
	}, true
}

// ToDomain converts a wire MCP event back.
func (e MCPEvent) ToDomain() mcp.Event {
	t := mcp.EventStateChanged
	switch e.Type {
	case MCPEventToolsListChanged:
		t = mcp.EventToolsListChanged
	case MCPEventPromptsListChanged:
		t = mcp.EventPromptsListChanged
	case MCPEventResourcesListChanged:
		t = mcp.EventResourcesListChanged
	}
	return mcp.Event{
		Type:   t,
		Name:   e.Name,
		State:  mcp.State(e.State),
		Error:  e.Error,
		Counts: mcp.Counts{Tools: e.ToolCount, Prompts: e.PromptCount, Resources: e.ResourceCount},
	}
}

// AgentEventFromDomain converts an agent notification for the wire.
// The human-readable message travels in Error. ProviderID stays on the
// server, which is where provider re-authentication runs.
func AgentEventFromDomain(n notify.Notification) AgentEvent {
	return AgentEvent{
		SessionID:    n.SessionID,
		SessionTitle: n.SessionTitle,
		RunID:        n.RunID,
		Type:         AgentEventType(n.Type),
		Error:        stringError(n.Message),
		AWSSOCommand: n.AWSSOCommand,
		AWSSOURL:     n.AWSSOURL,
	}
}

// ToDomain converts a wire agent event back into a notification.
func (e AgentEvent) ToDomain() notify.Notification {
	return notify.Notification{
		SessionID:    e.SessionID,
		SessionTitle: e.SessionTitle,
		RunID:        e.RunID,
		Type:         notify.Type(e.Type),
		Message:      errorString(e.Error),
		AWSSOCommand: e.AWSSOCommand,
		AWSSOURL:     e.AWSSOURL,
	}
}

// RunCompleteFromDomain converts a run's terminal event for the wire.
func RunCompleteFromDomain(r notify.RunComplete) RunComplete {
	return RunComplete(r)
}

// ToDomain converts a wire run completion back.
func (r RunComplete) ToDomain() notify.RunComplete {
	return notify.RunComplete(r)
}
