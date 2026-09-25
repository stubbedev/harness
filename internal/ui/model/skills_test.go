package model

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/skills"
	"github.com/stubbedev/harness/internal/ui/common"
	uistyles "github.com/stubbedev/harness/internal/ui/styles"
)

// TestSkillStatusItemsIncludesBuiltinSkills verifies sidebar skills include
// both runtime-discovered skill states and builtin skills that may not have
// emitted a SkillState event yet.
func TestSkillStatusItemsIncludesBuiltinSkills(t *testing.T) {
	t.Parallel()

	st := uistyles.CharmtonePantera()
	ui := &UI{
		com: &common.Common{Styles: &st},
		skillStates: []*skills.SkillState{
			{Name: "go-doc", Path: "/tmp/go-doc/SKILL.md", State: skills.StateNormal},
		},
	}

	items := ui.skillStatusItems()
	require.NotEmpty(t, items)

	var hasGoDoc bool
	for _, item := range items {
		if item.title == st.Resource.Name.Render("go-doc") {
			hasGoDoc = true
			break
		}
	}
	require.True(t, hasGoDoc)

	builtinSkills := skills.DiscoverBuiltin()
	require.NotEmpty(t, builtinSkills)

	var hasBuiltin bool
	for _, skill := range builtinSkills {
		if skill.Name == "go-doc" {
			continue
		}
		expected := st.Resource.Name.Render(skill.Name)
		for _, item := range items {
			if item.title == expected {
				hasBuiltin = true
				break
			}
		}
		if hasBuiltin {
			break
		}
	}
	require.True(t, hasBuiltin)
}

func TestSkillStatusItemsExcludesDisabledSkills(t *testing.T) {
	t.Parallel()

	st := uistyles.CharmtonePantera()
	ui := &UI{
		com: &common.Common{
			Styles:    &st,
			Workspace: &testWorkspace{cfg: &config.Config{Options: &config.Options{DisabledSkills: []string{"go-doc", "harness-config"}}}},
		},
		skillStates: []*skills.SkillState{
			{Name: "go-doc", Path: "/tmp/go-doc/SKILL.md", State: skills.StateNormal},
		},
	}

	items := ui.skillStatusItems()

	for _, item := range items {
		require.NotEqual(t, "go-doc", item.name)
		require.NotEqual(t, "harness-config", item.name)
	}
}

// skillWorkspace serves one known SKILL.md body for the run-skill
// path.
type skillWorkspace struct {
	*countingWorkspace
}

const testSkillID = "/skills/review/SKILL.md"

func (w *skillWorkspace) ReadSkill(_ context.Context, skillID string) ([]byte, skills.SkillReadResult, error) {
	if skillID != testSkillID {
		return nil, skills.SkillReadResult{}, fmt.Errorf("skill not found: %s", skillID)
	}
	content := "---\nname: review\ndescription: Review the diff.\n---\nReview every file of the diff."
	return []byte(content), skills.SkillReadResult{Name: "review", Description: "Review the diff."}, nil
}

// TestRunSkillSendsInvocationImmediately pins the skills palette's
// select behavior: the skill's body is loaded and sent as a
// <loaded_skill> invocation right away, with no intermediate
// attachment to compose against.
func TestRunSkillSendsInvocationImmediately(t *testing.T) {
	t.Parallel()

	m := newBusyUI(&countingWorkspace{ready: true})
	m.com.Workspace = &skillWorkspace{countingWorkspace: &countingWorkspace{ready: true}}

	msg := m.runSkill(testSkillID, "review")()
	send, ok := msg.(sendMessageMsg)
	require.True(t, ok, "selecting a skill sends immediately, got %T", msg)
	require.Empty(t, send.Name, "the <loaded_skill> wrapper is its own invocation shape")
	require.True(t, strings.HasPrefix(send.Content, "<loaded_skill>"))
	require.Contains(t, send.Content, "<name>review</name>")
	require.Contains(t, send.Content, "Review every file of the diff.",
		"the parsed body rides the invocation, frontmatter stripped")
	require.NotContains(t, send.Content, "---", "frontmatter must not leak into the invocation")
}
