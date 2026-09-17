package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHighestReasoningLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		levels []string
		want   string
	}{
		{"empty", nil, ""},
		{"single", []string{"low"}, "low"},
		{"ascending order", []string{"low", "medium", "high"}, "high"},
		{"descending order", []string{"high", "medium", "low"}, "high"},
		{"unordered", []string{"medium", "max", "low"}, "max"},
		{"with none", []string{"none", "low", "high"}, "high"},
		{"minimal beats none", []string{"none", "minimal"}, "minimal"},
		{"xhigh beats high", []string{"high", "xhigh"}, "xhigh"},
		{"unknown names only", []string{"custom", "turbo"}, "turbo"},
		{"known beats unknown", []string{"custom", "low"}, "low"},
		{"unknown does not beat known", []string{"high", "turbo"}, "high"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, HighestReasoningLevel(tc.levels))
		})
	}
}
