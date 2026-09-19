package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActivationFrontmatter(t *testing.T) {
	t.Parallel()
	skill, err := ParseContent([]byte(`---
name: explicit
description: Explicit activation
activation:
  paths: ["internal/**/*.go"]
  directories: [docs]
  tools:
    - name: lsp
      actions: [rename, references]
  capabilities: [go]
  markers: [go.mod]
---
Follow this procedure.
`))
	require.NoError(t, err)
	require.NoError(t, skill.Validate())
	require.Equal(t, []string{"internal/**/*.go"}, skill.Activation.Paths)
	require.Equal(t, []ActivationTool{{Name: "lsp", Actions: []string{"rename", "references"}}}, skill.Activation.Tools)
	legacy, err := ParseContent([]byte("---\nname: legacy\ndescription: Still semantic\n---\nBody"))
	require.NoError(t, err)
	require.Nil(t, legacy.Activation)
	require.NoError(t, legacy.Validate())
}

func TestActivationMatches(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example"), 0o600))
	cases := []struct {
		name  string
		rules ActivationRules
		event ActivationEvent
		want  bool
	}{
		{"glob", ActivationRules{Paths: []string{"internal/**/*.go"}}, ActivationEvent{Paths: []string{"internal/pkg/file.go"}}, true},
		{"glob root", ActivationRules{Paths: []string{"**/*.go"}}, ActivationEvent{Paths: []string{"main.go"}}, true},
		{"absolute", ActivationRules{Paths: []string{"**/*.go"}}, ActivationEvent{Paths: []string{filepath.Join(root, "main.go")}}, true},
		{"outside", ActivationRules{Paths: []string{"**/*.go"}}, ActivationEvent{Paths: []string{filepath.Join(root, "..", "main.go")}}, false},
		{"directory", ActivationRules{Directories: []string{"internal/agent"}}, ActivationEvent{Paths: []string{"internal/agent/tools/view.go"}}, true},
		{"directory boundary", ActivationRules{Directories: []string{"internal/agent"}}, ActivationEvent{Paths: []string{"internal/agents/view.go"}}, false},
		{"tool action", ActivationRules{Tools: []ActivationTool{{Name: "lsp", Actions: []string{"rename"}}}}, ActivationEvent{Tool: "lsp", Action: "rename"}, true},
		{"wrong action", ActivationRules{Tools: []ActivationTool{{Name: "lsp", Actions: []string{"rename"}}}}, ActivationEvent{Tool: "lsp", Action: "definition"}, false},
		{"wrong tool", ActivationRules{Tools: []ActivationTool{{Name: "lsp", Actions: []string{"rename"}}}}, ActivationEvent{Tool: "shell", Action: "rename"}, false},
		{"tool any action", ActivationRules{Tools: []ActivationTool{{Name: "view"}}}, ActivationEvent{Tool: "view"}, true},
		{"marker", ActivationRules{Markers: []string{"go.mod"}}, ActivationEvent{}, true},
		{"missing marker", ActivationRules{Markers: []string{"Cargo.toml"}}, ActivationEvent{}, false},
		{"capability", ActivationRules{Capabilities: []string{"go"}}, ActivationEvent{Capabilities: ProjectCapabilities(root)}, true},
		{"exact capability", ActivationRules{Capabilities: []string{"Go"}}, ActivationEvent{Capabilities: ProjectCapabilities(root)}, false},
		{"or rules", ActivationRules{Paths: []string{"*.rs"}, Tools: []ActivationTool{{Name: "view"}}}, ActivationEvent{Tool: "view"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, tc.rules.Validate())
			require.Equal(t, tc.want, tc.rules.Matches(root, tc.event))
		})
	}
}

func TestActivationValidation(t *testing.T) {
	t.Parallel()
	for _, rules := range []ActivationRules{
		{},
		{Paths: []string{"["}},
		{Paths: []string{"/etc/*"}},
		{Paths: []string{"../*"}},
		{Directories: []string{"src/../outside"}},
		{Markers: []string{"**/go.mod"}},
		{Markers: []string{""}},
		{Tools: []ActivationTool{{Name: ""}}},
		{Tools: []ActivationTool{{Name: "lsp", Actions: []string{""}}}},
		{Capabilities: []string{""}},
	} {
		require.Error(t, rules.Validate())
		require.False(t, rules.Matches(t.TempDir(), ActivationEvent{}))
	}
}

func TestActivationMarkersStayInWorkspace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "go.mod")
	require.NoError(t, os.WriteFile(outside, []byte("module external"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "go.mod")))
	rules := &ActivationRules{Markers: []string{"go.mod"}}
	require.False(t, rules.Matches(root, ActivationEvent{}))
	require.Empty(t, ProjectCapabilities(root))
	require.NoError(t, os.Mkdir(filepath.Join(root, "package.json"), 0o700))
	require.Empty(t, ProjectCapabilities(root))
}
