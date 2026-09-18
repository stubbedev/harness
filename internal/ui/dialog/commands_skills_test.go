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
