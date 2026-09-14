package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/skills"
)

// newSkillSearchTool builds the tool over a temp-dir skill set, with the
// coordinator reduced to the fields the tool actually reads.
func newSkillSearchTool(t *testing.T, defs map[string]string) *skillSearchTool {
	t.Helper()

	dir := t.TempDir()
	active := make([]*skills.Skill, 0, len(defs))
	for name, body := range defs {
		path := filepath.Join(dir, name, skills.SkillFileName)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
		skill, err := skills.Parse(path)
		require.NoError(t, err)
		active = append(active, skill)
	}

	return &skillSearchTool{coord: &coordinator{
		cfg:          config.NewTestStore(&config.Config{}),
		activeSkills: active,
		skillTracker: skills.NewTracker(active),
	}}
}

func runSkillSearch(t *testing.T, tool *skillSearchTool, input map[string]any) fantasy.ToolResponse {
	t.Helper()
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-1", Name: SkillSearchToolName, Input: string(raw)})
	require.NoError(t, err)
	return resp
}

func skillFile(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n\nStep one.\n"
}

func TestSkillSearchQueryReturnsTriggers(t *testing.T) {
	t.Parallel()

	tool := newSkillSearchTool(t, map[string]string{
		"jq":            skillFile("jq", "Use when reshaping or filtering JSON data."),
		"shell-builtin": skillFile("shell-builtin", "Use when adding a command to the embedded shell."),
	})

	resp := runSkillSearch(t, tool, map[string]any{"query": "json"})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "jq: Use when reshaping or filtering JSON data.")
	require.NotContains(t, resp.Content, "shell-builtin", "a non-matching skill stays out of the result")
}

func TestSkillSearchLoadReturnsWholeSkill(t *testing.T) {
	t.Parallel()

	tool := newSkillSearchTool(t, map[string]string{
		"jq": skillFile("jq", "Use when reshaping or filtering JSON data."),
	})

	resp := runSkillSearch(t, tool, map[string]any{"load": []string{"jq"}})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "<loaded_skill")
	require.Contains(t, resp.Content, "Step one.", "the body is what a load is for")
	require.True(t, tool.coord.skillTracker.IsLoaded("jq"), "a loaded skill counts as loaded")
}

func TestSkillSearchRejectsUnknownName(t *testing.T) {
	t.Parallel()

	tool := newSkillSearchTool(t, map[string]string{
		"jq": skillFile("jq", "Use when reshaping or filtering JSON data."),
	})

	resp := runSkillSearch(t, tool, map[string]any{"load": []string{"nope"}})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "nope")
}

// TestSkillSearchHidesNonInvocableSkills covers the one exclusion the old
// <available_skills> list also made: a skill the user reserved for themselves
// is not offered to the model.
func TestSkillSearchHidesNonInvocableSkills(t *testing.T) {
	t.Parallel()

	tool := newSkillSearchTool(t, map[string]string{
		"jq": skillFile("jq", "Use when reshaping or filtering JSON data."),
	})
	tool.coord.activeSkills[0].DisableModelInvocation = true

	require.Empty(t, tool.skillNames())
	resp := runSkillSearch(t, tool, map[string]any{"load": []string{"jq"}})
	require.True(t, resp.IsError)
}
