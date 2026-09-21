package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
