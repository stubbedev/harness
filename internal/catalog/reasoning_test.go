package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultReasoningLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		levels []string
		want   string
	}{
		{"empty", nil, ""},
		{"single", []string{"low"}, "low"},
		{"two picks the higher", []string{"high", "max"}, "max"},
		{"three picks the middle", []string{"low", "medium", "high"}, "medium"},
		{"descending order", []string{"high", "medium", "low"}, "medium"},
		{"unordered", []string{"medium", "max", "low"}, "medium"},
		{"four picks the upper middle", []string{"low", "medium", "high", "max"}, "high"},
		{"five picks the middle", []string{"minimal", "low", "medium", "high", "xhigh"}, "medium"},
		{"glm", []string{"low", "high", "max"}, "high"},
		{"with none", []string{"none", "low", "high"}, "low"},
		{"minimal beats none", []string{"none", "minimal"}, "minimal"},
		{"unknown names keep their order", []string{"custom", "turbo", "ludicrous"}, "turbo"},
		{"unknown among known keeps the order", []string{"custom", "low"}, "low"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, DefaultReasoningLevel(tc.levels))
		})
	}
}

func TestLowestReasoningLevel(t *testing.T) {
	t.Parallel()
	assert.Empty(t, LowestReasoningLevel(nil))
	assert.Equal(t, "low", LowestReasoningLevel([]string{"max", "high", "low"}))
	assert.Equal(t, "none", LowestReasoningLevel([]string{"low", "none"}))
	assert.Equal(t, "custom", LowestReasoningLevel([]string{"custom", "turbo"}), "unknown names keep their order")
}

func TestStepDownReasoningLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		levels []string
		from   string
		want   string
	}{
		{"glm max to high", []string{"low", "high", "max"}, "max", "high"},
		{"glm high skips the missing medium", []string{"low", "high", "max"}, "high", "low"},
		{"glm low stays", []string{"low", "high", "max"}, "low", ""},
		{"max to xhigh", []string{"low", "medium", "high", "xhigh", "max"}, "max", "xhigh"},
		{"high to medium", []string{"low", "medium", "high"}, "high", "medium"},
		{"medium to low", []string{"low", "medium", "high"}, "medium", "low"},
		{"descending order", []string{"max", "high", "low"}, "max", "high"},
		{"never into minimal", []string{"minimal", "low", "medium"}, "low", ""},
		{"never into none", []string{"none", "high"}, "high", ""},
		{"only levels the model has", []string{"high", "max"}, "high", ""},
		{"unknown from", []string{"low", "high"}, "turbo", ""},
		{"unknown levels are skipped", []string{"custom", "high", "max"}, "max", "high"},
		{"empty", nil, "high", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, StepDownReasoningLevel(tc.levels, tc.from))
		})
	}
}

func TestWeakerReasoningLevel(t *testing.T) {
	t.Parallel()
	require.True(t, WeakerReasoningLevel("low", "high"))
	require.False(t, WeakerReasoningLevel("high", "low"))
	require.False(t, WeakerReasoningLevel("high", "high"))
	require.False(t, WeakerReasoningLevel("custom", "high"), "unknown names do not compare")
}
