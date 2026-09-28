package proto

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/message"
)

// CreateMessageParams represents parameters for creating a message.
type CreateMessageParams struct {
	Role     MessageRole   `json:"role"`
	Parts    []ContentPart `json:"parts"`
	Model    string        `json:"model"`
	Provider string        `json:"provider,omitempty"`
}

// Message represents a message in the proto layer.
type Message struct {
	ID                      string        `json:"id"`
	Role                    MessageRole   `json:"role"`
	SessionID               string        `json:"session_id"`
	Parts                   []ContentPart `json:"parts"`
	Model                   string        `json:"model"`
	Provider                string        `json:"provider"`
	PrismModelID            string        `json:"prism_model_id,omitempty"`
	PrismModelName          string        `json:"prism_model_name,omitempty"`
	PrismHypercreditSavings *float64      `json:"prism_hypercredit_savings,omitempty"`
	PrismDollarSavings      *float64      `json:"prism_dollar_savings,omitempty"`
	CreatedAt               int64         `json:"created_at"`
	UpdatedAt               int64         `json:"updated_at"`
	IsSummaryMessage        bool          `json:"is_summary_message,omitempty"`
}

// MessageRole represents the role of a message sender.
type MessageRole string

const (
	Assistant MessageRole = "assistant"
	User      MessageRole = "user"
	System    MessageRole = "system"
	Tool      MessageRole = "tool"
)

// MarshalText implements the [encoding.TextMarshaler] interface.
func (r MessageRole) MarshalText() ([]byte, error) {
	return []byte(r), nil
}

// UnmarshalText implements the [encoding.TextUnmarshaler] interface.
func (r *MessageRole) UnmarshalText(data []byte) error {
	*r = MessageRole(data)
	return nil
}

// FinishReason represents why a message generation finished.
type FinishReason string

const (
	FinishReasonEndTurn       FinishReason = "end_turn"
	FinishReasonMaxTokens     FinishReason = "max_tokens"
	FinishReasonToolUse       FinishReason = "tool_use"
	FinishReasonCanceled      FinishReason = "canceled"
	FinishReasonError         FinishReason = "error"
	FinishReasonContentFilter FinishReason = "content_filter"
	FinishReasonUnknown       FinishReason = "unknown"
)

// MarshalText implements the [encoding.TextMarshaler] interface.
func (fr FinishReason) MarshalText() ([]byte, error) {
	return []byte(fr), nil
}

// UnmarshalText implements the [encoding.TextUnmarshaler] interface.
func (fr *FinishReason) UnmarshalText(data []byte) error {
	*fr = FinishReason(data)
	return nil
}

// ContentPart is a part of a message's content.
//
//sumtype:decl
type ContentPart interface {
	partType() partType
}

// ReasoningContent represents the reasoning/thinking part of a message.
type ReasoningContent struct {
	Thinking   string `json:"thinking"`
	Signature  string `json:"signature"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

// String returns the thinking content as a string.
func (tc ReasoningContent) String() string {
	return tc.Thinking
}

func (ReasoningContent) partType() partType { return reasoningType }

// TextContent represents a text part of a message.
type TextContent struct {
	Text string `json:"text"`
}

// String returns the text content as a string.
func (tc TextContent) String() string {
	return tc.Text
}

func (TextContent) partType() partType { return textType }

// ImageURLContent represents an image URL part of a message.
type ImageURLContent struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// String returns the image URL as a string.
func (iuc ImageURLContent) String() string {
	return iuc.URL
}

func (ImageURLContent) partType() partType { return imageURLType }

// BinaryContent represents binary data in a message.
type BinaryContent struct {
	Path     string
	MIMEType string
	Data     []byte
}

// String returns a base64-encoded string of the binary data.
func (bc BinaryContent) String(p catalog.InferenceProvider) string {
	base64Encoded := base64.StdEncoding.EncodeToString(bc.Data)
	if p == catalog.InferenceProviderOpenAI {
		return "data:" + bc.MIMEType + ";base64," + base64Encoded
	}
	return base64Encoded
}

func (BinaryContent) partType() partType { return binaryType }

// ToolCall represents a tool call in a message.
type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
	Type  string `json:"type,omitempty"`
	// ProviderExecuted marks a call the provider ran itself (a hosted
	// web search, say), which the agent must not execute.
	ProviderExecuted bool `json:"provider_executed,omitempty"`
	Finished         bool `json:"finished,omitempty"`
	// MCPServer names the MCP server the called tool belongs to.
	MCPServer string `json:"mcp_server,omitempty"`
}

func (ToolCall) partType() partType { return toolCallType }

// ToolResult represents the result of a tool call.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	Data       string `json:"data,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	Metadata   string `json:"metadata"`
	IsError    bool   `json:"is_error"`
	Canceled   bool   `json:"canceled,omitempty"`
}

func (ToolResult) partType() partType { return toolResultType }

// Finish represents the end of a message generation.
type Finish struct {
	Reason  FinishReason `json:"reason"`
	Time    int64        `json:"time"`
	Message string       `json:"message,omitempty"`
	Details string       `json:"details,omitempty"`
}

func (Finish) partType() partType { return finishType }

// ShellCommand stores a bang-mode shell command and its output.
type ShellCommand struct {
	Command  string `json:"command"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

func (ShellCommand) partType() partType { return shellCommandType }

// SubagentNote is a message a running background sub-agent pushed to its
// orchestrator. It renders as user text for the model but never in the
// frontend transcript.
type SubagentNote struct {
	AgentName      string `json:"agent_name"`
	Handle         string `json:"handle,omitempty"`
	ChildSessionID string `json:"child_session_id,omitempty"`
	Text           string `json:"text"`
}

func (SubagentNote) partType() partType { return subagentNoteType }

// ContextNote is text the harness adds to a message for the model alone
// (runtime facts, directory instructions, diagnostics). It never renders
// in the transcript.
type ContextNote struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func (ContextNote) partType() partType { return contextNoteType }

// MarshalJSON implements the [json.Marshaler] interface.
func (m Message) MarshalJSON() ([]byte, error) {
	parts, err := MarshalParts(m.Parts)
	if err != nil {
		return nil, err
	}

	type Alias Message
	return json.Marshal(&struct {
		Parts json.RawMessage `json:"parts"`
		*Alias
	}{
		Parts: json.RawMessage(parts),
		Alias: (*Alias)(&m),
	})
}

// UnmarshalJSON implements the [json.Unmarshaler] interface.
func (m *Message) UnmarshalJSON(data []byte) error {
	type Alias Message
	aux := &struct {
		Parts json.RawMessage `json:"parts"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	parts, err := UnmarshalParts([]byte(aux.Parts))
	if err != nil {
		return err
	}

	m.Parts = parts
	return nil
}

// Content returns the first text content part.
func (m *Message) Content() TextContent {
	for _, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			return c
		}
	}
	return TextContent{}
}

// ReasoningContent returns the first reasoning content part.
func (m *Message) ReasoningContent() ReasoningContent {
	for _, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			return c
		}
	}
	return ReasoningContent{}
}

// ImageURLContent returns all image URL content parts.
func (m *Message) ImageURLContent() []ImageURLContent {
	imageURLContents := make([]ImageURLContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ImageURLContent); ok {
			imageURLContents = append(imageURLContents, c)
		}
	}
	return imageURLContents
}

// BinaryContent returns all binary content parts.
func (m *Message) BinaryContent() []BinaryContent {
	binaryContents := make([]BinaryContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(BinaryContent); ok {
			binaryContents = append(binaryContents, c)
		}
	}
	return binaryContents
}

// ToolCalls returns all tool call parts.
func (m *Message) ToolCalls() []ToolCall {
	toolCalls := make([]ToolCall, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			toolCalls = append(toolCalls, c)
		}
	}
	return toolCalls
}

// ToolResults returns all tool result parts.
func (m *Message) ToolResults() []ToolResult {
	toolResults := make([]ToolResult, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolResult); ok {
			toolResults = append(toolResults, c)
		}
	}
	return toolResults
}

// IsFinished returns true if the message has a finish part.
func (m *Message) IsFinished() bool {
	for _, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			return true
		}
	}
	return false
}

// FinishPart returns the finish part if present.
func (m *Message) FinishPart() *Finish {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return &c
		}
	}
	return nil
}

// FinishReason returns the finish reason if present.
func (m *Message) FinishReason() FinishReason {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return c.Reason
		}
	}
	return ""
}

// IsThinking returns true if the message is currently in a thinking state.
func (m *Message) IsThinking() bool {
	return m.ReasoningContent().Thinking != "" && m.Content().Text == "" && !m.IsFinished()
}

// ThinkingDuration returns the duration of the thinking phase.
func (m *Message) ThinkingDuration() time.Duration {
	reasoning := m.ReasoningContent()
	if reasoning.StartedAt == 0 {
		return 0
	}

	endTime := reasoning.FinishedAt
	if endTime == 0 {
		endTime = time.Now().Unix()
	}

	return time.Duration(endTime-reasoning.StartedAt) * time.Second
}

type partType string

const (
	reasoningType    partType = "reasoning"
	textType         partType = "text"
	imageURLType     partType = "image_url"
	binaryType       partType = "binary"
	toolCallType     partType = "tool_call"
	toolResultType   partType = "tool_result"
	finishType       partType = "finish"
	shellCommandType partType = "shell_command"
	subagentNoteType partType = "subagent_note"
	contextNoteType  partType = "context_note"
)

type partWrapper struct {
	Type partType    `json:"type"`
	Data ContentPart `json:"data"`
}

// MarshalParts marshals content parts to JSON.
func MarshalParts(parts []ContentPart) ([]byte, error) {
	wrappedParts := make([]partWrapper, len(parts))
	for i, part := range parts {
		wrappedParts[i] = partWrapper{Type: part.partType(), Data: part}
	}
	return json.Marshal(wrappedParts)
}

// partDecoders maps each wire part type to its decoder. A type missing
// here cannot be sent: TestEveryDomainPartCrossesTheWire walks every
// domain part through the conversion and back.
var partDecoders = map[partType]func(json.RawMessage) (ContentPart, error){
	reasoningType:    decodePart[ReasoningContent],
	textType:         decodePart[TextContent],
	imageURLType:     decodePart[ImageURLContent],
	binaryType:       decodePart[BinaryContent],
	toolCallType:     decodePart[ToolCall],
	toolResultType:   decodePart[ToolResult],
	finishType:       decodePart[Finish],
	shellCommandType: decodePart[ShellCommand],
	subagentNoteType: decodePart[SubagentNote],
	contextNoteType:  decodePart[ContextNote],
}

func decodePart[T ContentPart](data json.RawMessage) (ContentPart, error) {
	var part T
	if err := json.Unmarshal(data, &part); err != nil {
		return nil, err
	}
	return part, nil
}

// UnmarshalParts unmarshals content parts from JSON. A part type this
// build does not know (sent by a newer server) is skipped rather than
// failing the whole message, matching how the message store reads rows.
func UnmarshalParts(data []byte) ([]ContentPart, error) {
	var wrappers []struct {
		Type partType        `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &wrappers); err != nil {
		return nil, err
	}

	parts := make([]ContentPart, 0, len(wrappers))
	for _, wrapper := range wrappers {
		decode, ok := partDecoders[wrapper.Type]
		if !ok {
			slog.Warn("Skipping unknown message part type", "type", wrapper.Type)
			continue
		}
		part, err := decode(wrapper.Data)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// Attachment represents a file attachment.
type Attachment struct {
	FilePath string `json:"file_path"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	Content  []byte `json:"content"`
}

// ToMessage converts a proto Attachment to a [message.Attachment].
func (a Attachment) ToMessage() message.Attachment {
	return message.Attachment{
		FilePath: a.FilePath,
		FileName: a.FileName,
		MimeType: a.MimeType,
		Content:  a.Content,
	}
}

// AttachmentFromMessage converts a [message.Attachment] to a proto
// Attachment.
func AttachmentFromMessage(a message.Attachment) Attachment {
	return Attachment{
		FilePath: a.FilePath,
		FileName: a.FileName,
		MimeType: a.MimeType,
		Content:  a.Content,
	}
}

// AttachmentsToMessage converts a slice of proto Attachments to a slice
// of [message.Attachment].
func AttachmentsToMessage(as []Attachment) []message.Attachment {
	out := make([]message.Attachment, len(as))
	for i, a := range as {
		out[i] = a.ToMessage()
	}
	return out
}

// AttachmentsFromMessage converts a slice of [message.Attachment] to a
// slice of proto Attachments.
func AttachmentsFromMessage(as []message.Attachment) []Attachment {
	out := make([]Attachment, len(as))
	for i, a := range as {
		out[i] = AttachmentFromMessage(a)
	}
	return out
}

// MarshalJSON implements the [json.Marshaler] interface.
func (a Attachment) MarshalJSON() ([]byte, error) {
	type Alias Attachment
	return json.Marshal(&struct {
		Content string `json:"content"`
		*Alias
	}{
		Content: base64.StdEncoding.EncodeToString(a.Content),
		Alias:   (*Alias)(&a),
	})
}

// UnmarshalJSON implements the [json.Unmarshaler] interface.
func (a *Attachment) UnmarshalJSON(data []byte) error {
	type Alias Attachment
	aux := &struct {
		Content string `json:"content"`
		*Alias
	}{
		Alias: (*Alias)(a),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	content, err := base64.StdEncoding.DecodeString(aux.Content)
	if err != nil {
		return err
	}
	a.Content = content
	return nil
}
