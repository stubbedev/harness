package dialog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/skills"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func paletteTestCommands() []commands.CustomCommand {
	return []commands.CustomCommand{
		{ID: "review", Name: "review", Content: "review the diff"},
		{
			ID:   "rust-expert",
			Name: "rust-expert",
			Skill: &skills.Skill{
				Name:          "rust-expert",
				Description:   "Rust expert guidance",
				SkillFilePath: "/skills/rust-expert/SKILL.md",
			},
		},
	}
}

// kontainerPaletteCommands mirrors the skill set of issue #59, where a
// description that starts with the query word used to outrank the name
// that contains it.
func kontainerPaletteCommands() []commands.CustomCommand {
	def := []struct{ name, desc string }{
		{"kontainer-architecture", "Use when deciding where new code belongs, navigating the Kontainer codebase"},
		{"kontainer-browser-test", "Use this skill to manually QA a Bitbucket PR in a real browser"},
		{"kontainer-cut-release", "Cut a Kontainer release (release/X.Y.Z branch off develop)"},
		{"kontainer-debugging", "Use when debugging a failure, investigating an exception"},
		{"kontainer-git-workflow", "Use whenever committing, branching, opening or updating a PR"},
		{"kontainer-php", "MANDATORY whenever writing, editing, or reviewing backend PHP"},
		{"kontainer-pr-callstack-html", "Produce an interactive HTML call-stack map of a pull request"},
		{"kontainer-pr", "Drive the open PR for the current branch. Use when the user says '/pr'"},
		{"kontainer-prs", "Loop the kontainer-pr flow over every open PR you author or review"},
		{"kontainer-sync-branch", "Pull the base branch into the current branch and resolve conflicts"},
	}
	cmds := make([]commands.CustomCommand, 0, len(def))
	for _, d := range def {
		cmds = append(cmds, commands.CustomCommand{
			ID:   d.name,
			Name: d.name,
			Skill: &skills.Skill{
				Name:          d.name,
				Description:   d.desc,
				SkillFilePath: "/skills/" + d.name + "/SKILL.md",
			},
		})
	}
	return cmds
}

func paletteTestCommon() *common.Common {
	s := styles.CharmtonePantera()
	return &common.Common{Workspace: &stubWorkspace{cfg: &config.Config{}}, Styles: &s}
}

// TestSkillsPaletteListsOnlySkills pins the "/" palette: it shows
// skills and nothing else, with no tab cycling. Every discovered skill
// is listed — the user-invocable opt-in does not gate the palette.
func TestSkillsPaletteListsOnlySkills(t *testing.T) {
	t.Parallel()

	c, err := NewSkills(paletteTestCommon(), paletteTestCommands())
	require.NoError(t, err)
	assert.True(t, c.SkillsOnly())

	items := c.list.FilteredItems()
	require.Len(t, items, 1)
	item, ok := items[0].(*CommandItem)
	require.True(t, ok)
	assert.Equal(t, "rust-expert", item.title)

	// Enter runs the attach-skill action.
	action := item.Action()
	attach, ok := action.(ActionAttachSkill)
	require.True(t, ok, "selecting a skill attaches it, got %T", action)
	assert.Equal(t, "/skills/rust-expert/SKILL.md", attach.ID)
}

// TestCommandsPaletteUserTabExcludesSkills pins the ":" palette split:
// skills moved to their own palette, so the User tab lists only custom
// and extension commands.
func TestCommandsPaletteUserTabExcludesSkills(t *testing.T) {
	t.Parallel()

	c, err := NewCommands(paletteTestCommon(), "", false, false, false, paletteTestCommands(), nil)
	require.NoError(t, err)

	c.setCommandItems(UserCommands)
	items := c.list.FilteredItems()
	require.Len(t, items, 1)
	item, ok := items[0].(*CommandItem)
	require.True(t, ok)
	assert.Equal(t, "review", item.title)
	_, isAttach := item.Action().(ActionAttachSkill)
	assert.False(t, isAttach, "the commands palette must not offer skills")
}

// TestSkillsPaletteQueryRanksNameAboveDescription pins the issue #59
// ranking: typing "pr" must put the skill whose name contains "pr"
// above skills whose only match is the description that starts with it.
func TestSkillsPaletteQueryRanksNameAboveDescription(t *testing.T) {
	t.Parallel()

	c, err := NewSkills(paletteTestCommon(), kontainerPaletteCommands())
	require.NoError(t, err)

	c.list.SetFilter("pr")
	items := c.list.FilteredItems()
	require.NotEmpty(t, items)
	got := make([]string, 0, len(items))
	for _, it := range items {
		item, ok := it.(*CommandItem)
		require.True(t, ok)
		got = append(got, item.title)
	}
	assert.Equal(t, "kontainer-pr", got[0], "got %v", got)
	assert.Equal(t, "kontainer-prs", got[1], "got %v", got)
}

// TestSkillsPaletteIgnoresSourcePrefix pins that a skill's source
// prefix (project:/user:/system:) is display only: it is not part of
// the filter text, and matches on the name after it highlight on the
// title at the right offset.
func TestSkillsPaletteIgnoresSourcePrefix(t *testing.T) {
	t.Parallel()

	cmds := []commands.CustomCommand{{
		ID:   "commit",
		Name: "project:commit",
		Skill: &skills.Skill{
			Name:          "commit",
			Description:   "Commit guidance",
			SkillFilePath: "/skills/commit/SKILL.md",
		},
	}}
	c, err := NewSkills(paletteTestCommon(), cmds)
	require.NoError(t, err)

	// The prefix is not searchable: "pro" matches nothing even though
	// the label starts with it.
	c.list.SetFilter("pro")
	assert.Empty(t, c.list.FilteredItems())

	// The name after the prefix is searchable, still renders under its
	// full label, and highlights land on the name in the title.
	c.list.SetFilter("co")
	items := c.list.FilteredItems()
	require.Len(t, items, 1)
	item, ok := items[0].(*CommandItem)
	require.True(t, ok)
	assert.Equal(t, "project:commit", item.title)
	assert.Equal(t, []int{8, 9}, item.matchForTitle().MatchedIndexes)
}
