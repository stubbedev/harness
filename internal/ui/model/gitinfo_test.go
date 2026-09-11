package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatGitStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		branch string
		status string
		want   string
	}{
		{
			name:   "clean branch",
			branch: "main",
			status: "## main...origin/main",
			want:   "\ue0a0 main",
		},
		{
			name:   "dirty working tree",
			branch: "main",
			status: "## main...origin/main\n M a.go\n M b.go\n?? c.go\nM  d.go\nR  e.go",
			want:   "\ue0a0 main [?1 !2 +1 »1]",
		},
		{
			name:   "ahead and behind shows diverged",
			branch: "main",
			status: "## main...origin/main [ahead 2, behind 3]",
			want:   "\ue0a0 main [⇕]",
		},
		{
			name:   "ahead only",
			branch: "main",
			status: "## main...origin/main [ahead 2]",
			want:   "\ue0a0 main [⇡2]",
		},
		{
			name:   "behind only",
			branch: "feature",
			status: "## feature...origin/feature [behind 5]",
			want:   "\ue0a0 feature [⇣5]",
		},
		{
			name:   "conflicts and deletions",
			branch: "main",
			status: "## main\nUU merge.go\n D gone.go\nAD both.go",
			want:   "\ue0a0 main [=1 +1 ✘2]",
		},
		{
			name:   "no branch line",
			branch: "main",
			status: " M a.go",
			want:   "\ue0a0 main [!1]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, formatGitStatus(tt.branch, tt.status))
		})
	}
}

func TestParseGitSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want gitSummary
	}{
		{
			name: "clean branch",
			in:   "\ue0a0 main",
			want: gitSummary{branch: "main"},
		},
		{
			name: "dirty and remote",
			in:   "\ue0a0 main [?1 !2 ⇡3]",
			want: gitSummary{branch: "main", dirty: "?1 !2", remote: "⇡3"},
		},
		{
			name: "diverged",
			in:   "\ue0a0 feature [✘1 ⇕]",
			want: gitSummary{branch: "feature", dirty: "✘1", remote: "⇕"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, parseGitSummary(tt.in))
		})
	}
}

func TestGitHeaderParts_EmptyOutsideRepo(t *testing.T) {
	t.Parallel()
	// gitStatusInfo with a nonexistent directory starts a refresh but
	// returns the (empty) cache, so the header drops the segment instead
	// of rendering garbage.
	require.Empty(t, gitHeaderParts(nil, "/nonexistent-dir-for-harness-test"))
}
