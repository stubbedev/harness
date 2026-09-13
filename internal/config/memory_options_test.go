package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryOptionsDefaults(t *testing.T) {
	var nilOpts *MemoryOptions
	require.True(t, nilOpts.IsEnabled())
	require.Equal(t, DefaultMaxMemories, nilOpts.GetMaxMemories())
	require.Equal(t, DefaultMemoryIndexBudget, nilOpts.GetIndexBudget())

	empty := &MemoryOptions{}
	require.True(t, empty.IsEnabled())
	require.Equal(t, DefaultMaxMemories, empty.GetMaxMemories())
	require.Equal(t, DefaultMemoryIndexBudget, empty.GetIndexBudget())
}

func TestMemoryOptionsConfigured(t *testing.T) {
	disabled := false
	small := 10
	tiny := 100
	opts := &MemoryOptions{Enabled: &disabled, MaxMemories: &small, IndexBudget: &tiny}

	require.False(t, opts.IsEnabled())
	require.Equal(t, 10, opts.GetMaxMemories())
	require.Equal(t, 200, opts.GetIndexBudget(), "index budget is clamped to 200")
}
