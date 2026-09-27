package dialog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/skills"
)

func invocationCatalog() *Catalog {
	cmds := []commands.CustomCommand{
		{ID: "user:fixup", Name: "user:fixup", Content: "Rebase onto $BRANCH", Arguments: []commands.Argument{{ID: "BRANCH", Required: true}}},
		{ID: "project:fixup", Name: "project:fixup", Content: "Project fixup"},
		{ID: "project:review", Name: "project:review", Skill: &skills.Skill{Name: "review", SkillFilePath: "/s/review/SKILL.md"}},
		// A skill cannot shadow a built-in.
		{ID: "user:compact", Name: "user:compact", Skill: &skills.Skill{Name: "compact"}},
	}
	return NewCatalog(paletteTestCommon(), CommandState{SessionID: "s1", HasGoal: true}, cmds, nil)
}

func TestFindInvocableResolution(t *testing.T) {
	t.Parallel()

	items := invocationCatalog().Invocables()

	compact := FindInvocable(items, "COMPACT")
	require.NotNil(t, compact, "names match without regard to case")
	assert.IsType(t, ActionCompact{}, compact.Action(), "a built-in wins over a skill of the same name")

	assert.NotNil(t, FindInvocable(items, "new-session"), "a built-in answers to its ID spelled with dashes")
	assert.NotNil(t, FindInvocable(items, "new_session"))
	assert.NotNil(t, FindInvocable(items, "clear"), "and to its aliases")
	assert.NotNil(t, FindInvocable(items, "clear-goal"), "state-dependent built-ins are there when they apply")

	fixup := FindInvocable(items, "fixup")
	require.NotNil(t, fixup)
	assert.Equal(t, "user:fixup", fixup.Action().(ActionRunCustomCommand).Command.Name, "the first source wins a bare name")
	project := FindInvocable(items, "project:fixup")
	require.NotNil(t, project, "the full label settles the clash")
	assert.Equal(t, "project:fixup", project.Action().(ActionRunCustomCommand).Command.Name)

	assert.Nil(t, FindInvocable(items, "nope"))
}

func TestInvokeBindsThroughArgSpec(t *testing.T) {
	t.Parallel()

	items := invocationCatalog().Invocables()

	// Bound and complete: runs.
	act, err := Invoke(ActionInvoke{Action: FindInvocable(items, "compact").Action(), Name: "compact", Raw: "keep the notes"})
	require.NoError(t, err)
	run, ok := act.(ActionRun)
	require.True(t, ok, "got %T", act)
	assert.Equal(t, "keep the notes", run.Action.(ActionCompact).Args.Value("focus"))

	// A required field left out: the form, prefilled with what was typed.
	act, err = Invoke(ActionInvoke{Action: FindInvocable(items, "fixup").Action(), Name: "fixup"})
	require.NoError(t, err)
	open, ok := act.(ActionOpenArguments)
	require.True(t, ok, "got %T", act)
	assert.IsType(t, ActionRunCustomCommand{}, open.Action)

	// Text for a command that takes none is refused, not dropped.
	_, err = Invoke(ActionInvoke{Action: FindInvocable(items, "summarize").Action(), Name: "summarize", Raw: "now"})
	require.EqualError(t, err, "/summarize takes no arguments")

	// "/goal clear" clears instead of setting a goal named "clear".
	act, err = Invoke(ActionInvoke{Action: FindInvocable(items, "goal").Action(), Name: "goal", Raw: "clear"})
	require.NoError(t, err)
	assert.Equal(t, ActionRun{Action: ActionClearGoal{}}, act)
}

func TestSelectActionFollowsArgSpec(t *testing.T) {
	t.Parallel()

	items := invocationCatalog().Invocables()

	assert.Equal(t, ActionInsertInvocation{Name: "compact"}, FindInvocable(items, "compact").SelectAction(),
		"a command that asks for arguments goes to the editor")
	assert.Equal(t, "[focus]", FindInvocable(items, "compact").ArgumentHint(), "the hint is the ArgSpec's")
	_, runs := FindInvocable(items, "review").SelectAction().(ActionInvoke)
	assert.True(t, runs, "a skill without a hint runs on pick")
}
