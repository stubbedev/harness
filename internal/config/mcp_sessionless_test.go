package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPConfig_IsSessionless(t *testing.T) {
	t.Parallel()

	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name string
		cfg  MCPConfig
		want bool
	}{
		{
			name: "explicit true",
			cfg:  MCPConfig{Type: MCPHttp, URL: "https://mcp.example.com/", Sessionless: boolPtr(true)},
			want: true,
		},
		{
			name: "explicit false",
			cfg:  MCPConfig{Type: MCPHttp, URL: "https://mcp.example.com/", Sessionless: boolPtr(false)},
			want: false,
		},
		{
			name: "unset defaults to false",
			cfg:  MCPConfig{Type: MCPHttp, URL: "https://mcp.example.com/mcp"},
			want: false,
		},
		{
			name: "empty url defaults to false",
			cfg:  MCPConfig{Type: MCPHttp},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.cfg.IsSessionless())
		})
	}
}
