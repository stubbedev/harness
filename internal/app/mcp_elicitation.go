package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
// (URL elicitations, schemas beyond five fields) is declined so the
// server can fall back rather than hang.
func wireMCPElicitation(questions question.Service) {
	harnessmcp.SetElicitationHandler(func(ctx context.Context, server string, params *mcpsdk.ElicitParams) (*mcpsdk.ElicitResult, error) {
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
				return &mcpsdk.ElicitResult{Action: "decline"}, nil
			}
			return nil, askErr
		}
		content, contentErr := elicitationContent(answers, params.RequestedSchema)
		if contentErr != nil {
			return nil, contentErr
		}
		return &mcpsdk.ElicitResult{Action: "accept", Content: content}, nil
	})
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

// elicitationQuestion converts an ElicitParams into a question.Request.
func elicitationQuestion(server string, params *mcpsdk.ElicitParams) (*question.Request, error) {
	schema := parseElicitationSchema(params.RequestedSchema)

	message := strings.TrimSpace(params.Message)
	if message == "" {
		message = fmt.Sprintf("MCP server %q requests input", server)
	}
	if len(message) > question.MaxDescriptionLength {
		message = message[:question.MaxDescriptionLength]
	}

	var questions []question.Question
	for name, prop := range schema.Properties {
		if len(questions) == question.MaxQuestions {
			break
		}
		q := elicitationPropertyQuestion(name, prop, schema.Required)
		questions = append(questions, q)
	}
	// No properties: the server is asking for a bare confirmation.
	if len(questions) == 0 {
		questions = append(questions, question.Question{
			ID:          "confirm",
			Type:        question.TypeYesNo,
			Text:        message,
			Description: fmt.Sprintf("The MCP server %q asks you to confirm.", server),
		})
		return &question.Request{ID: newElicitationID(), Questions: questions}, nil
	}

	// The elicitation message travels as the description on single-field
	// forms (where the TUI shows it next to the input) and as the
	// confirmation header on multi-field forms.
	if len(questions) == 1 {
		if questions[0].Description == "" {
			questions[0].Description = message
		}
	} else {
		confirmTitle := fmt.Sprintf("%s needs input", server)
		confirmDesc := message
		return &question.Request{
			ID:                 newElicitationID(),
			Questions:          questions,
			ConfirmTitle:       confirmTitle,
			ConfirmDescription: confirmDesc,
		}, nil
	}
	return &question.Request{ID: newElicitationID(), Questions: questions}, nil
}

func elicitationPropertyQuestion(name string, prop elicitationProperty, required []string) question.Question {
	isRequired := false
	for _, r := range required {
		if r == name {
			isRequired = true
			break
		}
	}
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
	if vals := parseEnum(prop.Enum); len(vals) >= 2 && len(vals) <= question.MaxChoices {
		q.Type = question.TypeSingleChoice
		for _, v := range vals {
			q.Choices = append(q.Choices, question.Choice{ID: v, Label: v})
		}
		return q
	}

	switch strings.ToLower(prop.Type) {
	case "boolean":
		q.Type = question.TypeYesNo
	default:
		q.Type = question.TypeFreeText
	}
	return q
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

func parseEnum(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var vals []string
	if err := json.Unmarshal(raw, &vals); err != nil {
		return nil
	}
	return vals
}

// elicitationContent converts answered questions back into the
// map[string]any the server expects, coercing to the schema's declared
// types where they are parseable.
func elicitationContent(answers []question.Answer, rawSchema any) (map[string]any, error) {
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
	return content, nil
}

// coerceElicitationValue parses a textual answer into the numeric type
// the schema asked for. ok is false when the value should stay a string.
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
