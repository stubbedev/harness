package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/skills"
)

func TestLoadFromSource_NonExistentDir(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "does-not-exist")

	cmds, err := loadFromSource(commandSource{path: dir, prefix: userCommandPrefix})
	require.NoError(t, err)
	require.Empty(t, cmds)

	// directory must NOT have been created
	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr))
}

func TestLoadFromSource_ExistingDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.md"), []byte("say hello"), 0o644))

	cmds, err := loadFromSource(commandSource{path: dir, prefix: userCommandPrefix})
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	require.Equal(t, "user:hello", cmds[0].ID)
	require.Equal(t, "say hello", cmds[0].Content)
}

func TestLoadAll_MixedSources(t *testing.T) {
	t.Parallel()

	existing := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(existing, "cmd.md"), []byte("content"), 0o644))

	missing := filepath.Join(t.TempDir(), "nope")

	cmds, err := loadAll([]commandSource{
		{path: existing, prefix: userCommandPrefix},
		{path: missing, prefix: projectCommandPrefix},
	})
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	require.Equal(t, "user:cmd", cmds[0].ID)
}

func TestFromSkillCatalog_ListsUserInvocableSkills(t *testing.T) {
	t.Parallel()

	cmds := FromSkillCatalog([]skills.CatalogEntry{
		{
			ID:            "/skills/on/SKILL.md",
			Name:          "on",
			Description:   "Visible.",
			Label:         "user:on",
			UserInvocable: true,
		},
		{
			ID:            "/skills/off/SKILL.md",
			Name:          "off",
			Description:   "Opted out with user-invocable: false.",
			Label:         "user:off",
			UserInvocable: false,
		},
	})

	// The palette lists every user-invocable skill, which is the
	// default: lazy loading keeps bodies out of LLM context, not the
	// skill out of the user's / search. Only an explicit opt-out hides
	// a skill.
	require.Len(t, cmds, 1)
	require.Equal(t, "user:on", cmds[0].ID)
	require.Equal(t, "user:on", cmds[0].Name)
	require.Equal(t, "on", cmds[0].Skill.Name)
	require.Equal(t, "Visible.", cmds[0].Skill.Description)
	require.Equal(t, "/skills/on/SKILL.md", cmds[0].Skill.SkillFilePath)
}

func TestFromSkillCatalog_UsesDiscoveredSymlinkedSkills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires special privileges on Windows")
	}
	t.Parallel()

	root := t.TempDir()
	targetParent := t.TempDir()
	targetSkillDir := filepath.Join(targetParent, "linked-skill")
	require.NoError(t, os.MkdirAll(targetSkillDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(targetSkillDir, skills.SkillFileName),
		[]byte("---\nname: linked-skill\ndescription: Symlinked.\n---\nUse me.\n"),
		0o644,
	))

	link := filepath.Join(root, "linked-skill")
	require.NoError(t, os.Symlink(targetSkillDir, link))

	_, activeSkills, _ := skills.DiscoverFromConfig(skills.DiscoveryConfig{
		SkillsPaths: []string{root},
	})
	entries := skills.Catalog(activeSkills, []string{root}, "")
	cmds := FromSkillCatalog(entries)

	// The builtins are listed too (they are user-invocable by default
	// now), so assert on the linked skill's presence rather than on the
	// total count.
	var linked *CustomCommand
	for i := range cmds {
		if cmds[i].Skill != nil && cmds[i].Skill.Name == "linked-skill" {
			linked = &cmds[i]
			break
		}
	}
	require.NotNil(t, linked, "the discovered symlinked skill must be listed")
	require.Equal(t, "user:linked-skill", linked.ID)
	require.Equal(t, filepath.Join(link, skills.SkillFileName), linked.Skill.SkillFilePath)
}
