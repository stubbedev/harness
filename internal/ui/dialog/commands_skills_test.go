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

// newTestPalette opens the palette over cmds, with no session.
func newTestPalette(t *testing.T, cmds []commands.CustomCommand) *Commands {
	t.Helper()

	com := paletteTestCommon()
	c, err := NewCommands(com, NewCatalog(com, CommandState{}, cmds, nil))
	require.NoError(t, err)
	return c
}

// newSkillsPalette opens the palette on its Skills tab.
func newSkillsPalette(t *testing.T, cmds []commands.CustomCommand) *Commands {
	t.Helper()

	c := newTestPalette(t, cmds)
	c.setCommandItems(SkillsCommands)
	return c
}

// TestSkillsTabListsOnlySkills pins the Skills tab: it shows skills and
// nothing else. Every discovered skill is listed - the user-invocable
// opt-in does not gate the palette.
func TestSkillsTabListsOnlySkills(t *testing.T) {
	t.Parallel()

	c := newSkillsPalette(t, paletteTestCommands())

	items := c.list.FilteredItems()
	require.Len(t, items, 1)
	item, ok := items[0].(*CommandItem)
	require.True(t, ok)
	assert.Equal(t, "rust-expert", item.title)

	// A skill with no argument hint runs as soon as it is picked.
	inv, ok := item.SelectAction().(ActionInvoke)
	require.True(t, ok, "picking a skill runs it, got %T", item.SelectAction())
	run, ok := inv.Action.(ActionRunCustomCommand)
	require.True(t, ok)
	require.NotNil(t, run.Command.Skill)
	assert.Equal(t, "/skills/rust-expert/SKILL.md", run.Command.Skill.SkillFilePath)
}

// TestUserTabExcludesSkills pins the palette split: skills have their
// own tab, so the User tab lists only custom and extension commands.
func TestUserTabExcludesSkills(t *testing.T) {
	t.Parallel()

	c := newTestPalette(t, paletteTestCommands())

	c.setCommandItems(UserCommands)
	items := c.list.FilteredItems()
	require.Len(t, items, 1)
	item, ok := items[0].(*CommandItem)
	require.True(t, ok)
	assert.Equal(t, "review", item.title)
	run, ok := item.Action().(ActionRunCustomCommand)
	require.True(t, ok)
	assert.Nil(t, run.Command.Skill, "the User tab must not offer skills")
}

// TestSkillsPaletteQueryRanksNameAboveDescription pins the issue #59
// ranking: typing "pr" must put the skill whose name contains "pr"
// above skills whose only match is the description that starts with it.
func TestSkillsPaletteQueryRanksNameAboveDescription(t *testing.T) {
	t.Parallel()

	c := newSkillsPalette(t, kontainerPaletteCommands())

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
	c := newSkillsPalette(t, cmds)

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
