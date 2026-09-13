package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/prompt"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/subagents"
)

// newPromptTestStore returns a config store for prompt-rendering tests. It
// builds the store directly instead of calling config.Init, which reads the
// host's HOME/XDG locations: a developer with a populated ~/.config/harness
// would otherwise have their own context files and skill paths rendered into
// the prompt under test.
func newPromptTestStore(t *testing.T) *config.ConfigStore {
	t.Helper()
	return config.NewTestStoreWithWorkingDir(&config.Config{Options: &config.Options{}}, t.TempDir())
}

// TestCoderPrompt_RendersAvailableSubagents verifies that wiring
// subagents.ToPromptXML through prompt.WithAvailableSubagentsXML into
// coderPrompt produces a system prompt containing the <available_subagents>
// block, the subagent's name/description, and the matching critical_rules
// delegation instruction — the exact path coordinator.go's NewCoordinator
// wires at construction time.
func TestCoderPrompt_RendersAvailableSubagents(t *testing.T) {
	t.Parallel()

	active := []*subagents.Subagent{
		{Name: "go-test-writer", Description: "Writes Go tests before implementation."},
	}

	p, err := coderPrompt(
		prompt.WithAvailableSubagentsXML(subagents.ToPromptXML(active)),
	)
	require.NoError(t, err)

	store := newPromptTestStore(t)

	systemPrompt, err := p.Build(context.Background(), "test-provider", "test-model", store)
	require.NoError(t, err)

	require.Contains(t, systemPrompt, "<available_subagents>")
	require.Contains(t, systemPrompt, "<name>go-test-writer</name>")
	require.Contains(t, systemPrompt, "<description>Writes Go tests before implementation.</description>")
	require.Contains(t, systemPrompt, "DELEGATE TO MATCHING SUBAGENTS")
}

// TestCoderPrompt_OmitsAvailableSubagentsWhenEmpty verifies that when no
// subagent XML option is supplied (no active subagents), the
// <available_subagents> block is absent from the rendered system prompt.
func TestCoderPrompt_OmitsAvailableSubagentsWhenEmpty(t *testing.T) {
	t.Parallel()

	p, err := coderPrompt()
	require.NoError(t, err)

	store := newPromptTestStore(t)

	systemPrompt, err := p.Build(context.Background(), "test-provider", "test-model", store)
	require.NoError(t, err)

	require.NotContains(t, systemPrompt, "<available_subagents>")
}

// TestCoderPrompt_MemoryBlockSteersWrites verifies that the memory block
// carries the write-steering instructions and the index when memory is
// enabled — the path coordinator.go wires whenever the feature is on.
func TestCoderPrompt_MemoryBlockSteersWrites(t *testing.T) {
	t.Parallel()

	p, err := coderPrompt(
		prompt.WithMemoryEnabled(true),
		prompt.WithMemoryIndex("- [build-commands] (project) How to build"),
	)
	require.NoError(t, err)

	store := newPromptTestStore(t)

	systemPrompt, err := p.Build(context.Background(), "test-provider", "test-model", store)
	require.NoError(t, err)

	require.Contains(t, systemPrompt, "# Memory")
	require.Contains(t, systemPrompt, "durable memory across sessions")
	require.Contains(t, systemPrompt, "Save the moment you learn")
	require.Contains(t, systemPrompt, "<memory_index>")
	require.Contains(t, systemPrompt, "[build-commands] (project) How to build")
}

// TestCoderPrompt_MemoryBlockSteersEvenWhenEmpty verifies the block still
// renders with the steering text on an empty store, so a fresh workspace
// is told to start saving memories rather than waiting for an index that
// only appears once something exists.
func TestCoderPrompt_MemoryBlockSteersEvenWhenEmpty(t *testing.T) {
	t.Parallel()

	p, err := coderPrompt(prompt.WithMemoryEnabled(true))
	require.NoError(t, err)

	store := newPromptTestStore(t)

	systemPrompt, err := p.Build(context.Background(), "test-provider", "test-model", store)
	require.NoError(t, err)

	require.Contains(t, systemPrompt, "# Memory")
	require.Contains(t, systemPrompt, "Save the moment you learn")
	require.Contains(t, systemPrompt, "No memories saved yet")
	require.NotContains(t, systemPrompt, "<memory_index>")
}

// TestCoderPrompt_OmitsMemoryWhenDisabled verifies the whole memory block
// is absent when the enabled option was never supplied (feature off).
func TestCoderPrompt_OmitsMemoryWhenDisabled(t *testing.T) {
	t.Parallel()

	p, err := coderPrompt()
	require.NoError(t, err)

	store := newPromptTestStore(t)

	systemPrompt, err := p.Build(context.Background(), "test-provider", "test-model", store)
	require.NoError(t, err)

	require.NotContains(t, systemPrompt, "# Memory")
}
