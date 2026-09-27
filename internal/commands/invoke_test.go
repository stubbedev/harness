package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/skills"
)

var prefixes = []string{"/", ":"}

func TestParseInvocation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		text, name, args string
		ok               bool
	}{
		{"/compact", "compact", "", true},
		{"  /compact keep the notes  ", "compact", "keep the notes", true},
		{":goal all tests pass", "goal", "all tests pass", true},
		{"/git:fixup main", "git:fixup", "main", true},
		{"/review 12\nfocus on auth", "review", "12\nfocus on auth", true},
		{"/etc/hosts is broken", "", "", false},
		{"/", "", "", false},
		{"/ compact", "", "", false},
		{"compact", "", "", false},
		{"hello /compact", "", "", false},
	}
	for _, tc := range cases {
		name, args, ok := ParseInvocation(tc.text, prefixes)
		assert.Equal(t, tc.ok, ok, tc.text)
		assert.Equal(t, tc.name, name, tc.text)
		assert.Equal(t, tc.args, args, tc.text)
	}
}

func TestSplitArgs(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"a", "b c", "d'e", ""}, SplitArgs(`a "b c" "d'e" ''`))
	assert.Empty(t, SplitArgs("   "))
}

func TestArgSpecBind(t *testing.T) {
	t.Parallel()

	one := ArgSpec{Fields: []Argument{{ID: "FOCUS"}}}
	args, err := one.Bind("  keep the notes ")
	require.NoError(t, err)
	assert.Equal(t, "keep the notes", args.Raw)
	assert.Equal(t, "keep the notes", args.Value("FOCUS"), "a single field takes the whole text")

	two := ArgSpec{Fields: []Argument{{ID: "PR", Required: true}, {ID: "FOCUS"}}}
	args, err = two.Bind(`1234 focus on "auth code"`)
	require.NoError(t, err)
	assert.Equal(t, "1234", args.Value("PR"))
	assert.Equal(t, "focus on auth code", args.Value("FOCUS"), "the last field takes the rest")
	assert.True(t, two.Complete(args))

	args, err = two.Bind("")
	require.NoError(t, err)
	assert.False(t, two.Complete(args), "a required field left out")

	_, err = ArgSpec{}.Bind("anything")
	require.Error(t, err, "text for a command that takes none")
	args, err = ArgSpec{FreeText: true}.Bind("anything")
	require.NoError(t, err)
	assert.Equal(t, "anything", args.Raw)
}

func TestArgSpecHintText(t *testing.T) {
	t.Parallel()

	assert.Empty(t, ArgSpec{FreeText: true}.HintText(), "free text alone asks for nothing")
	assert.Equal(t, "<pr> [focus]", ArgSpec{Fields: []Argument{{ID: "PR", Required: true}, {ID: "FOCUS"}}}.HintText())
	assert.Equal(t, "<n>", ArgSpec{Fields: []Argument{{ID: "PR"}}, Hint: "<n>"}.HintText(), "an explicit hint wins")
}

func TestArgsExpand(t *testing.T) {
	t.Parallel()

	args := Args{Raw: "main.go now", Values: map[string]string{"TARGET": "a", "TARGET_DIR": "b"}}
	assert.Equal(t, "a b main.go now", args.Expand("$TARGET $TARGET_DIR $ARGUMENTS"),
		"a longer name is not eaten by a shorter one")
	assert.Equal(t, "Review.\n\nARGUMENTS: main.go", Args{Raw: "main.go"}.Expand("Review.\n"),
		"text with nowhere to go follows the content")
	assert.Equal(t, "Review .", Args{}.Expand("Review $ARGUMENTS."))
}

func TestCustomCommandSpec(t *testing.T) {
	t.Parallel()

	file := CustomCommand{Name: "user:review", Content: "Review $ARGUMENTS"}
	spec := file.Spec()
	assert.True(t, spec.FreeText)
	assert.Equal(t, "[arguments]", spec.HintText(), "a $ARGUMENTS file hints that it takes text")

	plain := CustomCommand{Name: "user:hello", Content: "Say hello"}
	assert.Empty(t, plain.Spec().HintText(), "a file that asks for nothing runs on pick")

	skill := CustomCommand{Name: "user:pr", Skill: &skills.Skill{Name: "pr"}, ArgumentHint: "<number>"}
	assert.Equal(t, "<number>", skill.Spec().HintText())

	ext := CustomCommand{Name: "ext:x:y", ExtensionID: "ext:x:y"}
	assert.False(t, ext.Spec().TakesArgs(), "an extension takes exactly what it registered")
}

func TestLoadCommandArguments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "fixup.md")
	require.NoError(t, os.WriteFile(path, []byte("---\nargument-hint: <branch>\n---\nRebase onto $BRANCH. $ARGUMENTS\n"), 0o644))

	cmd, err := loadCommand(path, dir, userCommandPrefix)
	require.NoError(t, err)
	require.Len(t, cmd.Arguments, 1, "$ARGUMENTS is not a named field")
	assert.Equal(t, "BRANCH", cmd.Arguments[0].ID)
	assert.Equal(t, "<branch>", cmd.Spec().HintText())
}
