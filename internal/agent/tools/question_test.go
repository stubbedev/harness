package tools

import (
	"encoding/json"
	"reflect"
	"testing"

	"charm.land/fantasy/schema"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/question"
)

func TestQuestionParamsUnmarshalJSON_NativeArray(t *testing.T) {
	t.Parallel()
	input := `{"questions": [{"type": "yes_no", "question": "OK?", "description": "test"}]}`
	var p QuestionParams
	require.NoError(t, json.Unmarshal([]byte(input), &p))
	require.Len(t, p.Questions, 1)
	require.Equal(t, "OK?", p.Questions[0].Question)
}

func TestQuestionParamsUnmarshalJSON_StringEncodedArray(t *testing.T) {
	t.Parallel()
	// Simulates a model that double-serializes the questions field.
	inner := `[{"type":"yes_no","question":"OK?","description":"test"}]`
	encoded, _ := json.Marshal(inner)
	input := `{"questions": ` + string(encoded) + `}`
	var p QuestionParams
	require.NoError(t, json.Unmarshal([]byte(input), &p))
	require.Len(t, p.Questions, 1)
	require.Equal(t, "OK?", p.Questions[0].Question)
}

func TestQuestionParamsUnmarshalJSON_StringEncodedWithWhitespace(t *testing.T) {
	t.Parallel()
	inner := `  [{"type":"single_choice","question":"Pick","description":"d","choices":[{"id":"a","label":"A"}]}]  `
	encoded, _ := json.Marshal(inner)
	input := `{"questions": ` + string(encoded) + `, "confirm_title": "Go?"}`
	var p QuestionParams
	require.NoError(t, json.Unmarshal([]byte(input), &p))
	require.Len(t, p.Questions, 1)
	require.Equal(t, "Pick", p.Questions[0].Question)
	require.Equal(t, "Go?", p.ConfirmTitle)
}

func TestQuestionParamsUnmarshalJSON_InvalidString(t *testing.T) {
	t.Parallel()
	encoded, _ := json.Marshal("not valid json")
	input := `{"questions": ` + string(encoded) + `}`
	var p QuestionParams
	require.Error(t, json.Unmarshal([]byte(input), &p))
}

func TestFormatAnswer_MultiChoiceWithFillIn(t *testing.T) {
	answer := question.Answer{
		SelectedIDs: []string{"speed", "readability"},
		FillInText:  "maintainability",
	}
	resp, err := formatAnswer(&answer, question.TypeMultiChoice)
	require.NoError(t, err)
	require.Contains(t, resp.Content, `User selected: ["speed","readability"]`)
	require.Contains(t, resp.Content, "User provided: maintainability")
}

func TestFormatAnswer_SelectionsOnly(t *testing.T) {
	answer := question.Answer{SelectedIDs: []string{"gardening"}}
	resp, err := formatAnswer(&answer, question.TypeSingleChoice)
	require.NoError(t, err)
	require.Contains(t, resp.Content, `User selected: ["gardening"]`)
	require.NotContains(t, resp.Content, "User provided")
}

func TestFormatAnswer_Skipped(t *testing.T) {
	answer := question.Answer{}
	resp, err := formatAnswer(&answer, question.TypeFreeText)
	require.NoError(t, err)
	require.Equal(t, "User skipped this question", resp.Content)
}

// `options` is accepted as an alias for `choices` on decode but is not
// part of the schema, so the duplicate nested schema is never sent.
func TestQuestionItemOptionsAliasDecodesButIsNotInSchema(t *testing.T) {
	t.Parallel()
	var item QuestionItem
	require.NoError(t, json.Unmarshal([]byte(`{"type":"single_choice","question":"q","description":"d","options":[{"id":"a","label":"A"}]}`), &item))
	require.Len(t, item.GetChoices(), 1)
	require.Equal(t, "a", item.GetChoices()[0].ID)

	data, err := json.Marshal(schema.ToMap(schema.Generate(reflect.TypeFor[QuestionParams]())))
	require.NoError(t, err)
	require.NotContains(t, string(data), `"options"`)
	require.Contains(t, string(data), `"choices"`)
	require.Contains(t, string(data), `"yes_no"`)
}
