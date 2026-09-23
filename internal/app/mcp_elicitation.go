package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	harnessmcp "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/question"
)

// wireMCPElicitation routes server-initiated elicitation requests
// (the server asking the human a question mid-tool-call) into the TUI
// question form. Form elicitations become a question batch derived from
// the server's requested schema; anything the terminal cannot present
// faithfully (URL elicitations, schemas beyond the form's width, enums
// outside the choice window, structured field types) is declined so the
// server can fall back rather than receive a reshaped answer. A
// dismissed form answers "cancel" and an explicit refusal "decline".
func wireMCPElicitation(questions question.Service) {
	harnessmcp.SetElicitationHandler(elicitationHandler(questions))
}

// elicitationHandler builds the handler wireMCPElicitation installs.
func elicitationHandler(questions question.Service) harnessmcp.ElicitationHandler {
	return func(ctx context.Context, server string, params *mcpsdk.ElicitParams) (*mcpsdk.ElicitResult, error) {
		if params == nil {
			return &mcpsdk.ElicitResult{Action: "decline"}, nil
		}
		if params.URL != "" || params.Mode == "url" {
			slog.Warn("Declining URL elicitation; not supported in this terminal",
				"name", server, "url", params.URL)
			return &mcpsdk.ElicitResult{Action: "decline"}, nil
		}

		req, err := elicitationQuestion(server, params)
		if err != nil {
			slog.Warn("Declining elicitation; schema not presentable",
				"name", server, "error", err)
			return &mcpsdk.ElicitResult{Action: "decline"}, nil
		}

		answers, askErr := questions.Ask(ctx, *req)
		if askErr != nil {
			if errors.Is(askErr, question.ErrCancelled) {
				// The form was dismissed without an explicit
				// choice: the protocol's "cancel", distinct from
				// the explicit refusal "decline" carries.
				return &mcpsdk.ElicitResult{Action: "cancel"}, nil
			}
			return nil, askErr
		}
		if isBareConfirmation(params) {
			return bareConfirmationResult(answers), nil
		}
		return &mcpsdk.ElicitResult{Action: "accept", Content: elicitationContent(answers, params.RequestedSchema)}, nil
	}
}

// isBareConfirmation reports whether the server asked for a plain
// confirmation: a message and no fields to fill.
func isBareConfirmation(params *mcpsdk.ElicitParams) bool {
	return len(parseElicitationSchema(params.RequestedSchema).Properties) == 0
}

// bareConfirmationResult maps a bare confirmation's answer onto the
// protocol's three actions: an explicit yes accepts with empty content,
// an explicit no declines, and anything else is a dismissal.
func bareConfirmationResult(answers []question.Answer) *mcpsdk.ElicitResult {
	var yes *bool
	if len(answers) > 0 {
		yes = answers[0].Yes
	}
	switch {
	case yes != nil && *yes:
		return &mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{}}
	case yes != nil:
		return &mcpsdk.ElicitResult{Action: "decline"}
	default:
		return &mcpsdk.ElicitResult{Action: "cancel"}
	}
}

// elicitationSchema is the subset of JSON schema the elicitation form
// understands: top-level properties plus a required list.
type elicitationSchema struct {
	Properties map[string]elicitationProperty `json:"properties"`
	Required   []string                       `json:"required"`
}

type elicitationProperty struct {
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Enum        json.RawMessage `json:"enum"`
}

// elicitationConfirmID is the synthesized question ID for a schema with
// no fields: the server asked for a bare confirmation.
const elicitationConfirmID = "confirm"

// elicitationQuestion converts an ElicitParams into a question.Request.
// It returns an error for any schema the form cannot present faithfully;
// the handler turns that into a decline so the server can fall back
// rather than receive a reshaped answer.
func elicitationQuestion(server string, params *mcpsdk.ElicitParams) (*question.Request, error) {
	schema := parseElicitationSchema(params.RequestedSchema)

	// A schema wider than the form is declined outright: truncating it
	// would send accept with a content map that cannot satisfy a
	// required field the form silently dropped.
	if len(schema.Properties) > question.MaxQuestions {
		return nil, fmt.Errorf("schema has %d fields; the form presents at most %d", len(schema.Properties), question.MaxQuestions)
	}

	message := strings.TrimSpace(params.Message)
	if message == "" {
		message = fmt.Sprintf("MCP server %q requests input", server)
	}
	if len(message) > question.MaxDescriptionLength {
		message = message[:question.MaxDescriptionLength]
	}

	// Deterministic order: required fields first, then the rest, each
	// group alphabetical — iterating the map would make which fields
	// appear where random per call.
	names := slices.Sorted(maps.Keys(schema.Properties))
	slices.SortFunc(names, func(a, b string) int {
		if ra, rb := slices.Contains(schema.Required, a), slices.Contains(schema.Required, b); ra != rb {
			if ra {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})

	var questions []question.Question
	for _, name := range names {
		if name == "" {
			return nil, fmt.Errorf("schema has a field with an empty name")
		}
		q, err := elicitationPropertyQuestion(name, schema.Properties[name], schema.Required)
		if err != nil {
			return nil, err
		}
		questions = append(questions, q)
	}

	// No properties: the server is asking for a bare confirmation.
	if len(questions) == 0 {
		questions = append(questions, question.Question{
			ID:          elicitationConfirmID,
			Type:        question.TypeYesNo,
			Text:        message,
			Description: fmt.Sprintf("The MCP server %q asks you to confirm.", server),
		})
		return &question.Request{ID: newElicitationID(), Questions: questions}, nil
	}

	// The elicitation message travels as the description on single-field
	// forms (where the TUI shows it next to the input) and as the
	// confirmation header on multi-field forms.
	req := &question.Request{ID: newElicitationID(), Questions: questions}
	if len(questions) == 1 {
		// A one-field form has no confirmation header to carry the
		// message, so it becomes the description — otherwise the user is
		// asked for a value with only the generated "Required field ..."
		// line to go on and never sees what the server actually asked.
		req.Questions[0].Description = message
	} else {
		req.ConfirmTitle = fmt.Sprintf("%s needs input", server)
		req.ConfirmDescription = message
	}
	// The request must clear Ask's validation, or Ask would fail with
	// an error instead of the decline this path promises.
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return req, nil
}

// elicitationPropertyQuestion maps one schema property onto a form
// question, or an error when the property cannot be presented
// faithfully.
func elicitationPropertyQuestion(name string, prop elicitationProperty, required []string) (question.Question, error) {
	isRequired := slices.Contains(required, name)
	prefix := "Optional"
	if isRequired {
		prefix = "Required"
	}

	text := prop.Description
	if text == "" {
		text = name
	}
	if len(text) > question.MaxQuestionLength {
		text = text[:question.MaxQuestionLength]
	}
	desc := fmt.Sprintf("%s field %q requested by the MCP server.", prefix, name)
	if len(desc) > question.MaxDescriptionLength {
		desc = desc[:question.MaxDescriptionLength]
	}

	q := question.Question{
		ID:          name,
		Label:       name,
		Text:        text,
		Description: desc,
	}

	// An enum becomes a choice list regardless of the declared type.
	// Outside the presentable window the property is declined, not
	// reshaped into free text: whatever the user typed would be sent as
	// if it were one of the valid choices.
	if len(prop.Enum) > 0 {
		vals, ok := parseEnum(prop.Enum)
		if !ok {
			return question.Question{}, fmt.Errorf("field %q: enum values must be scalars (strings, numbers or booleans)", name)
		}
		if len(vals) < 2 || len(vals) > question.MaxChoices {
			return question.Question{}, fmt.Errorf("field %q: enum has %d values; the form presents 2 to %d", name, len(vals), question.MaxChoices)
		}
		q.Type = question.TypeSingleChoice
		seen := make(map[string]bool, len(vals))
		for _, v := range vals {
			if v == "" || seen[v] || len(v) > question.MaxChoiceLabelLength {
				return question.Question{}, fmt.Errorf("field %q: enum values must be unique, non-empty and at most %d characters", name, question.MaxChoiceLabelLength)
			}
			seen[v] = true
			q.Choices = append(q.Choices, question.Choice{ID: v, Label: v})
		}
		return q, nil
	}

	switch strings.ToLower(prop.Type) {
	case "", "string", "number", "integer":
		q.Type = question.TypeFreeText
	case "boolean":
		q.Type = question.TypeYesNo
	default:
		// Structured (array, object) and unknown types have no form
		// widget, and a free-text answer would not satisfy the schema.
		return question.Question{}, fmt.Errorf("field %q: type %q has no form representation", name, prop.Type)
	}
	return q, nil
}

func parseElicitationSchema(raw any) elicitationSchema {
	var schema elicitationSchema
	if raw == nil {
		return schema
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return schema
	}
	_ = json.Unmarshal(data, &schema)
	return schema
}

// parseEnum decodes an enum into choice values. Scalar members are
// stringified so numeric and boolean enums stay choices rather than
// degrading to free text; ok is false when the list is empty or holds
// non-scalars.
func parseEnum(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var vals []any
	if err := json.Unmarshal(raw, &vals); err != nil {
		return nil, false
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case float64:
			out = append(out, strconv.FormatFloat(v, 'f', -1, 64))
		case bool:
			out = append(out, strconv.FormatBool(v))
		default:
			return nil, false
		}
	}
	return out, len(out) > 0
}

// elicitationContent converts answered questions back into the
// map[string]any the server expects, coercing to the schema's declared
// types where they are parseable.
func elicitationContent(answers []question.Answer, rawSchema any) map[string]any {
	schema := parseElicitationSchema(rawSchema)

	content := make(map[string]any, len(answers))
	for _, ans := range answers {
		propType := ""
		if p, ok := schema.Properties[ans.QuestionID]; ok {
			propType = strings.ToLower(p.Type)
		}
		switch {
		case ans.Yes != nil:
			content[ans.QuestionID] = *ans.Yes
		case len(ans.SelectedIDs) > 0:
			v := ans.SelectedIDs[0]
			if coerced, ok := coerceElicitationValue(v, propType); ok {
				content[ans.QuestionID] = coerced
			} else {
				content[ans.QuestionID] = v
			}
		case ans.FillInText != "":
			if coerced, ok := coerceElicitationValue(ans.FillInText, propType); ok {
				content[ans.QuestionID] = coerced
			} else {
				content[ans.QuestionID] = ans.FillInText
			}
		}
	}
	return content
}

// coerceElicitationValue parses a textual answer into the declared
// schema type. ok is false when the value should stay a string.
func coerceElicitationValue(v, propType string) (any, bool) {
	switch propType {
	case "number":
		if f, err := parseJSONNumber(v); err == nil {
			return f, true
		}
	case "integer":
		if f, err := parseJSONNumber(v); err == nil && f == float64(int64(f)) {
			return int64(f), true
		}
	case "boolean":
		if b, err := strconv.ParseBool(v); err == nil {
			return b, true
		}
	}
	return nil, false
}

func parseJSONNumber(s string) (float64, error) {
	var f float64
	err := json.Unmarshal([]byte(s), &f)
	return f, err
}

func newElicitationID() string {
	return fmt.Sprintf("elicit-%d", time.Now().UnixNano())
}
