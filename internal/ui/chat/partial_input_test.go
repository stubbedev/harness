package chat

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPartialStringField(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		key   string
		want  string
		found bool
	}{
		{`{"command": "go test ./...`, "command", "go test ./...", true},
		{`{"command": "go test ./..."}`, "command", "go test ./...", true},
		{`{"command":"echo \"hi\" && printf 'a\nb`, "command", "echo \"hi\" && printf 'a\nb", true},
		{`{"comm`, "command", "", false},
		{`{"command"`, "command", "", false},
		{`{"command": `, "command", "", false},
		{`{"command": "`, "command", "", true},
		{`{"description": "x", "file_path": "/tmp/a.go", "content": "pack`, "file_path", "/tmp/a.go", true},
		{`{"command": "ls \`, "command", "ls ", true},
		{`{"command": "café x`, "command", "café x", true},
	}
	for _, tc := range cases {
		got, found := partialStringField(tc.input, tc.key)
		assert.Equal(t, tc.found, found, tc.input)
		assert.Equal(t, tc.want, got, tc.input)
	}
}

func TestPendingDetail(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "go build ./...", pendingDetail("  go build ./...\ngo test", 80))
	assert.Equal(t, "a b", pendingDetail("a\tb", 80))
	assert.Equal(t, "abcd…", pendingDetail("abcdefgh", 5))
}
