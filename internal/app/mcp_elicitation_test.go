package app

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/question"
)

func schemaOf(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func TestElicitationQuestion(t *testing.T) {
	t.Parallel()

	t.Run("no properties asks for a bare confirmation", func(t *testing.T) {
		t.Parallel()

		req, err := elicitationQuestion("forge", &mcpsdk.ElicitParams{Message: "Proceed with the deploy?"})
		require.NoError(t, err)
		require.Len(t, req.Questions, 1)
		assert.Equal(t, question.TypeYesNo, req.Questions[0].Type)
		assert.Equal(t, "Proceed with the deploy?", req.Questions[0].Text)
		assert.Contains(t, req.Questions[0].Description, `"forge"`)
	})

	t.Run("a server that says nothing still names itself", func(t *testing.T) {
		t.Parallel()

		req, err := elicitationQuestion("forge", &mcpsdk.ElicitParams{})
		require.NoError(t, err)
		require.Len(t, req.Questions, 1)
		assert.Contains(t, req.Questions[0].Text, `MCP server "forge" requests input`)
	})

	t.Run("one field carries the message as its description", func(t *testing.T) {
		t.Parallel()

		req, err := elicitationQuestion("forge", &mcpsdk.ElicitParams{
			Message: "Which branch should I tag?",
			RequestedSchema: schemaOf(map[string]any{
				"branch": map[string]any{"type": "string"},
			}, "branch"),
		})
		require.NoError(t, err)
		require.Len(t, req.Questions, 1)
		assert.Equal(t, question.TypeFreeText, req.Questions[0].Type)
		assert.Equal(t, "branch", req.Questions[0].ID)
		assert.Equal(t, "Which branch should I tag?", req.Questions[0].Description)
		assert.Empty(t, req.ConfirmTitle)
	})

	t.Run("several fields become a confirmed batch", func(t *testing.T) {
		t.Parallel()

		req, err := elicitationQuestion("forge", &mcpsdk.ElicitParams{
			Message: "Tag a release",
			RequestedSchema: schemaOf(map[string]any{
				"branch": map[string]any{"type": "string"},
				"draft":  map[string]any{"type": "boolean"},
			}, "branch"),
		})
		require.NoError(t, err)
		require.Len(t, req.Questions, 2)
		assert.Equal(t, "forge needs input", req.ConfirmTitle)
		assert.Equal(t, "Tag a release", req.ConfirmDescription)

		byID := map[string]question.Question{}
		for _, q := range req.Questions {
			byID[q.ID] = q
		}
		assert.Equal(t, question.TypeFreeText, byID["branch"].Type)
		assert.Equal(t, question.TypeYesNo, byID["draft"].Type)
		assert.Contains(t, byID["branch"].Description, "Required field")
		assert.Contains(t, byID["draft"].Description, "Optional field")
	})

	t.Run("an enum becomes a choice list whatever the declared type", func(t *testing.T) {
		t.Parallel()

		req, err := elicitationQuestion("forge", &mcpsdk.ElicitParams{
			RequestedSchema: schemaOf(map[string]any{
				"level": map[string]any{"type": "integer", "enum": []any{"1", "2", "3"}},
			}),
		})
		require.NoError(t, err)
		require.Len(t, req.Questions, 1)
		assert.Equal(t, question.TypeSingleChoice, req.Questions[0].Type)
		assert.Equal(t, []question.Choice{
			{ID: "1", Label: "1"},
			{ID: "2", Label: "2"},
			{ID: "3", Label: "3"},
		}, req.Questions[0].Choices)
	})

	t.Run("a one-value enum stays a free-text field", func(t *testing.T) {
		t.Parallel()

		req, err := elicitationQuestion("forge", &mcpsdk.ElicitParams{
			RequestedSchema: schemaOf(map[string]any{
				"only": map[string]any{"type": "string", "enum": []any{"one"}},
			}),
		})
		require.NoError(t, err)
		assert.Equal(t, question.TypeFreeText, req.Questions[0].Type)
	})

	// The dialog cannot present an unbounded form, so a wide schema is
	// truncated rather than refused.
	t.Run("more fields than the dialog holds are capped", func(t *testing.T) {
		t.Parallel()

		props := map[string]any{}
		for i := range question.MaxQuestions + 3 {
			props["field_"+string(rune('a'+i))] = map[string]any{"type": "string"}
		}
		req, err := elicitationQuestion("forge", &mcpsdk.ElicitParams{RequestedSchema: schemaOf(props)})
		require.NoError(t, err)
		assert.Len(t, req.Questions, question.MaxQuestions)
	})

	t.Run("over-long text is trimmed to what the dialog can show", func(t *testing.T) {
		t.Parallel()

		req, err := elicitationQuestion("forge", &mcpsdk.ElicitParams{
			Message: strings.Repeat("m", question.MaxDescriptionLength*2),
			RequestedSchema: schemaOf(map[string]any{
				"field": map[string]any{"type": "string", "description": strings.Repeat("d", question.MaxQuestionLength*2)},
			}),
		})
		require.NoError(t, err)
		assert.Len(t, req.Questions[0].Text, question.MaxQuestionLength)
	})
}

func TestElicitationContent(t *testing.T) {
	t.Parallel()

	yes := true
	no := false

	tests := []struct {
		name    string
		answers []question.Answer
		schema  map[string]any
		want    map[string]any
	}{
		{
			name:    "string stays a string",
			answers: []question.Answer{{QuestionID: "branch", FillInText: "main"}},
			schema:  schemaOf(map[string]any{"branch": map[string]any{"type": "string"}}),
			want:    map[string]any{"branch": "main"},
		},
		{
			name:    "number is coerced to a float",
			answers: []question.Answer{{QuestionID: "ratio", FillInText: "1.5"}},
			schema:  schemaOf(map[string]any{"ratio": map[string]any{"type": "number"}}),
			want:    map[string]any{"ratio": 1.5},
		},
		{
			name:    "integer is coerced to an int",
			answers: []question.Answer{{QuestionID: "count", FillInText: "42"}},
			schema:  schemaOf(map[string]any{"count": map[string]any{"type": "integer"}}),
			want:    map[string]any{"count": int64(42)},
		},
		{
			name:    "a non-integral answer to an integer field stays text",
			answers: []question.Answer{{QuestionID: "count", FillInText: "1.5"}},
			schema:  schemaOf(map[string]any{"count": map[string]any{"type": "integer"}}),
			want:    map[string]any{"count": "1.5"},
		},
		{
			name:    "an unparseable number stays text",
			answers: []question.Answer{{QuestionID: "ratio", FillInText: "many"}},
			schema:  schemaOf(map[string]any{"ratio": map[string]any{"type": "number"}}),
			want:    map[string]any{"ratio": "many"},
		},
		{
			name:    "yes and no become booleans",
			answers: []question.Answer{{QuestionID: "draft", Yes: &yes}, {QuestionID: "sign", Yes: &no}},
			schema:  schemaOf(map[string]any{"draft": map[string]any{"type": "boolean"}, "sign": map[string]any{"type": "boolean"}}),
			want:    map[string]any{"draft": true, "sign": false},
		},
		{
			name:    "a chosen enum value is coerced to the declared type",
			answers: []question.Answer{{QuestionID: "level", SelectedIDs: []string{"3"}}},
			schema:  schemaOf(map[string]any{"level": map[string]any{"type": "integer"}}),
			want:    map[string]any{"level": int64(3)},
		},
		{
			name:    "a field the schema never declared is passed through as text",
			answers: []question.Answer{{QuestionID: "extra", FillInText: "7"}},
			schema:  schemaOf(map[string]any{}),
			want:    map[string]any{"extra": "7"},
		},
		{
			name:    "an unanswered question is left out entirely",
			answers: []question.Answer{{QuestionID: "branch"}},
			schema:  schemaOf(map[string]any{"branch": map[string]any{"type": "string"}}),
			want:    map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			content, err := elicitationContent(tt.answers, tt.schema)
			require.NoError(t, err)
			assert.Equal(t, tt.want, content)
		})
	}
}

func TestParseElicitationSchema(t *testing.T) {
	t.Parallel()

	assert.Empty(t, parseElicitationSchema(nil).Properties)
	// A schema shaped nothing like one degrades to empty rather than
	// failing the whole elicitation.
	assert.Empty(t, parseElicitationSchema("not a schema").Properties)

	schema := parseElicitationSchema(schemaOf(map[string]any{
		"branch": map[string]any{"type": "string", "description": "which branch"},
	}, "branch"))
	assert.Equal(t, []string{"branch"}, schema.Required)
	assert.Equal(t, "which branch", schema.Properties["branch"].Description)
}

// fakeQuestions is a question.Service whose Ask returns whatever the test
// wants, so the handler's decline paths can be exercised without a TUI.
type fakeQuestions struct {
	question.Service
	answers []question.Answer
	err     error
	asked   *question.Request
}

func (f *fakeQuestions) Ask(_ context.Context, req question.Request) ([]question.Answer, error) {
	f.asked = &req
	return f.answers, f.err
}

func TestElicitationHandler(t *testing.T) {
	t.Parallel()

	yes := true
	schema := schemaOf(map[string]any{
		"branch": map[string]any{"type": "string"},
	}, "branch")

	t.Run("an answered form accepts", func(t *testing.T) {
		t.Parallel()

		questions := &fakeQuestions{answers: []question.Answer{{QuestionID: "branch", FillInText: "main"}}}
		res, err := elicitationHandler(questions)(t.Context(), "forge", &mcpsdk.ElicitParams{
			Message:         "Which branch?",
			RequestedSchema: schema,
		})
		require.NoError(t, err)
		assert.Equal(t, "accept", res.Action)
		assert.Equal(t, map[string]any{"branch": "main"}, res.Content)
		require.NotNil(t, questions.asked)
		assert.Len(t, questions.asked.Questions, 1)
	})

	// Esc on the form cancels the pending question. The server gets a
	// decline, which is an answer it can act on, rather than an error or
	// a call that never returns.
	t.Run("a cancelled form declines", func(t *testing.T) {
		t.Parallel()

		res, err := elicitationHandler(&fakeQuestions{err: question.ErrCancelled})(
			t.Context(), "forge", &mcpsdk.ElicitParams{Message: "Which branch?", RequestedSchema: schema})
		require.NoError(t, err)
		assert.Equal(t, "decline", res.Action)
		assert.Nil(t, res.Content)
	})

	// A turn cancelled while the question is open takes the context down
	// with it. That is not a decline the user chose, so it surfaces as an
	// error; the MCP layer turns it into a decline for the server.
	t.Run("a cancelled context is an error", func(t *testing.T) {
		t.Parallel()

		res, err := elicitationHandler(&fakeQuestions{err: context.Canceled})(
			t.Context(), "forge", &mcpsdk.ElicitParams{Message: "Which branch?", RequestedSchema: schema})
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, res)
	})

	t.Run("declines what the terminal cannot present", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name   string
			params *mcpsdk.ElicitParams
		}{
			{"no params", nil},
			{"a URL elicitation", &mcpsdk.ElicitParams{URL: "https://example.com/authorize"}},
			{"url mode", &mcpsdk.ElicitParams{Mode: "url"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				questions := &fakeQuestions{answers: []question.Answer{{QuestionID: "branch", Yes: &yes}}}
				res, err := elicitationHandler(questions)(t.Context(), "forge", tt.params)
				require.NoError(t, err)
				assert.Equal(t, "decline", res.Action)
				// Nothing was put in front of the user.
				assert.Nil(t, questions.asked)
			})
		}
	})
}
