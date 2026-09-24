package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/skills"
)

func activationTestSkill(t *testing.T, root, name string, rules *skills.ActivationRules) *skills.Skill {
	t.Helper()
	file := filepath.Join(root, name, skills.SkillFileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, os.WriteFile(file, []byte("---\nname: "+name+"\ndescription: Procedure\n---\nFollow "+name+" instructions."), 0o600))
	return &skills.Skill{Name: name, SkillFilePath: file, Path: filepath.Dir(file), Activation: rules}
}

func activationTestCall(name, input string) fantasy.Message {
	return fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
		fantasy.ToolCallPart{ToolCallID: "call", ToolName: name, Input: input},
	}}
}

func activationTestText(messages []fantasy.Message) string {
	var result strings.Builder
	for _, message := range messages {
		for _, part := range message.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				result.WriteString(text.Text)
			}
		}
	}
	return result.String()
}

func TestSkillActivationPrepare(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	skill := activationTestSkill(t, root, "go-procedure", &skills.ActivationRules{Paths: []string{"**/*.go"}})
	available := []*skills.Skill{skill}
	tracker := skills.NewTracker(available)
	activation := NewSkillActivation(&SkillActivationConfig{WorkingDir: root, Skills: func() []*skills.Skill { return available }, Tracker: tracker})
	messages := []fantasy.Message{fantasy.NewUserMessage("Please work on main.go and search for go-procedure")}
	require.Len(t, activation.Prepare(t.Context(), messages), 1)
	messages = append(messages, activationTestCall("view", `{"file_path":"main.go"}`))
	prepared := activation.Prepare(t.Context(), messages)
	require.Len(t, prepared, 3)
	require.Contains(t, activationTestText(prepared), "Follow go-procedure instructions.")
	require.Contains(t, activationTestText(prepared), skills.LoadedSkillPrecedence)
	require.True(t, tracker.IsLoaded(skill.Name))
	require.Len(t, activation.Prepare(t.Context(), prepared), 3)
	require.Len(t, activation.Prepare(t.Context(), messages), 3)
	require.Len(t, activation.loaded, 1)
}

func TestSkillActivationBoundaries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rules := &skills.ActivationRules{Tools: []skills.ActivationTool{{Name: "lsp", Actions: []string{"rename"}}}}
	enabled := activationTestSkill(t, root, "enabled", rules)
	disabled := activationTestSkill(t, root, "disabled", rules)
	disabled.DisableModelInvocation = true
	legacy := activationTestSkill(t, root, "legacy", nil)
	available := []*skills.Skill{disabled, legacy, enabled}
	messages := []fantasy.Message{activationTestCall("lsp", `{"action":"rename"}`)}
	for _, tc := range []struct {
		name    string
		allowed []string
		want    int
	}{
		{"unrestricted", nil, 2},
		{"empty denies", []string{}, 1},
		{"allowed", []string{"enabled"}, 2},
		{"disabled cannot override", []string{"disabled"}, 1},
		{"unknown", []string{"unknown"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			activation := NewSkillActivation(&SkillActivationConfig{WorkingDir: root, Skills: func() []*skills.Skill { return available }, AllowedSkills: tc.allowed})
			prepared := activation.Prepare(t.Context(), messages)
			require.Len(t, prepared, tc.want)
			require.NotContains(t, activationTestText(prepared), "Follow disabled")
			require.NotContains(t, activationTestText(prepared), "Follow legacy")
		})
	}
}

func TestSkillActivationProjectAndToolCalls(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example"), 0o600))
	available := []*skills.Skill{
		activationTestSkill(t, root, "project", &skills.ActivationRules{Capabilities: []string{"go"}}),
		activationTestSkill(t, root, "rename", &skills.ActivationRules{Tools: []skills.ActivationTool{{Name: "lsp", Actions: []string{"rename"}}}}),
		activationTestSkill(t, root, "docs", &skills.ActivationRules{Directories: []string{"docs"}}),
	}
	activation := NewSkillActivation(&SkillActivationConfig{WorkingDir: root, Skills: func() []*skills.Skill { return available }})
	messages := activation.Prepare(t.Context(), nil)
	require.Len(t, messages, 1)
	require.Contains(t, activationTestText(messages), "Follow project")
	messages = append(messages,
		activationTestCall("lsp", `{"action":"rename"}`),
		activationTestCall("view", `{"files":[{"file_path":"docs/usage.md"}]}`))
	prepared := activation.Prepare(t.Context(), messages)
	require.Len(t, prepared, 5)
	require.Contains(t, activationTestText(prepared), "Follow rename")
	require.Contains(t, activationTestText(prepared), "Follow docs")
}

func TestSkillActivationLimitsAndFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rules := &skills.ActivationRules{Tools: []skills.ActivationTool{{Name: "view"}}}
	var available []*skills.Skill
	for i := range skillActivationLimit + 2 {
		available = append(available, activationTestSkill(t, root, fmt.Sprintf("skill-%02d", i), rules))
	}
	tracker := skills.NewTracker(available)
	activation := NewSkillActivation(&SkillActivationConfig{WorkingDir: root, Skills: func() []*skills.Skill { return available }, Tracker: tracker})
	messages := []fantasy.Message{activationTestCall("view", `{}`)}
	prepared := activation.Prepare(t.Context(), messages)
	require.Len(t, prepared, skillActivationLimit+1)
	require.Len(t, activation.loaded, skillActivationLimit)
	require.Equal(t, skillActivationLimit, tracker.LoadedCount())
	require.Len(t, activation.Prepare(t.Context(), prepared), len(prepared))
	large := activationTestSkill(t, root, "large", rules)
	require.NoError(t, os.WriteFile(large.SkillFilePath, []byte(strings.Repeat("x", skillActivationByteBudget)), 0o600))
	missing := &skills.Skill{Name: "missing", SkillFilePath: filepath.Join(root, "absent"), Activation: rules}
	available = []*skills.Skill{large, missing}
	tracker = skills.NewTracker(available)
	activation = NewSkillActivation(&SkillActivationConfig{WorkingDir: root, Skills: func() []*skills.Skill { return available }, Tracker: tracker})
	require.Len(t, activation.Prepare(t.Context(), messages), 1)
	require.Empty(t, tracker.LoadedNames())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Equal(t, messages, activation.Prepare(ctx, messages))
	require.Equal(t, messages, NewSkillActivation(nil).Prepare(t.Context(), messages))
}

func TestSkillActivationEventsBoundedAndLiteral(t *testing.T) {
	t.Parallel()
	messages := []fantasy.Message{
		fantasy.NewUserMessage("lsp rename main.go"),
		activationTestCall("shell", `{"command":"cat main.go"}`),
		activationTestCall("lsp", `{"action":`),
	}
	events := skillActivationEvents(messages)
	require.Len(t, events, 1)
	require.Empty(t, events[0].Paths)
	require.Empty(t, events[0].Action)
	for range skillActivationEventLimit + 1 {
		messages = append(messages, activationTestCall("view", `{}`))
	}
	require.Len(t, skillActivationEvents(messages), skillActivationEventLimit)
}

func TestSkillActivationLiveBoundariesAndExplicitLoad(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	skill := activationTestSkill(t, root, "procedure", &skills.ActivationRules{Tools: []skills.ActivationTool{{Name: "view"}}})
	available := []*skills.Skill{skill}
	config := &SkillActivationConfig{WorkingDir: root, Skills: func() []*skills.Skill { return available }}
	activation := NewSkillActivation(config)
	messages := []fantasy.Message{activationTestCall("view", `{}`)}
	prepared := activation.Prepare(t.Context(), messages)
	require.Len(t, prepared, 2)
	body := activationTestText(prepared)
	manual := append(slices.Clone(messages), fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
		fantasy.ToolResultPart{ToolCallID: "load", Output: fantasy.ToolResultOutputContentText{Text: body}},
	}})
	require.Len(t, NewSkillActivation(config).Prepare(t.Context(), manual), 2)
	available = nil
	require.Len(t, activation.Prepare(t.Context(), messages), 1)
	available = []*skills.Skill{skill}
	skill.DisableModelInvocation = true
	require.Len(t, activation.Prepare(t.Context(), messages), 1)
	skill.DisableModelInvocation = false
	config.AllowedSkills = []string{}
	require.Len(t, activation.Prepare(t.Context(), messages), 1)
}
