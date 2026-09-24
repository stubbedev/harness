package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripShellDisplayPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{
			name: "project root reset is stripped",
			cmd:  "cd /home/user/proj && go build .",
			want: "go build .",
		},
		{
			name: "trailing slash root is stripped",
			cmd:  "cd /home/user/proj/ && go test ./...",
			want: "go test ./...",
		},
		{
			name: "subdirectory reset is stripped",
			cmd:  "cd /home/user/proj/internal/agent && go test",
			want: "go test",
		},
		{
			name: "relative subdirectory is stripped",
			cmd:  "cd internal/agent && go test",
			want: "go test",
		},
		{
			name: "semicolon separator is stripped",
			cmd:  "cd /home/user/proj; go test",
			want: "go test",
		},
		{
			name: "chained cds collapse",
			cmd:  "cd /home/user/proj && cd internal/agent && go test",
			want: "go test",
		},
		{
			name: "quoted path is stripped",
			cmd:  `cd "/home/user/my proj" && make`,
			want: "make",
		},
		{
			name: "single-quoted path is stripped",
			cmd:  "cd '/home/user/my proj' && make",
			want: "make",
		},
		{
			name: "newline separator is stripped",
			cmd:  "cd /home/user/proj\ngo test ./...",
			want: "go test ./...",
		},
		{
			name: "bare cd with no command is kept",
			cmd:  "cd /home/user/proj",
			want: "cd /home/user/proj",
		},
		{
			name: "command with no cd prefix is unchanged",
			cmd:  "go build .",
			want: "go build .",
		},
		{
			name: "cdrecord is not mistaken for cd",
			cmd:  "cdrecord -v dev=/dev/cdrom image.iso",
			want: "cdrecord -v dev=/dev/cdrom image.iso",
		},
		{
			name: "leading whitespace before cd is trimmed",
			cmd:  "  cd /home/user/proj && go build",
			want: "go build",
		},
		{
			name: "no space before && is stripped",
			cmd:  "cd /home/user/proj&&go build",
			want: "go build",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, StripShellDisplayPrefix(tc.cmd))
		})
	}
}
