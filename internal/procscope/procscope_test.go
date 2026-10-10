package procscope

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeLabel(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{"lsp-gopls", "lsp-gopls"},
		{"mcp-My Server", "mcp-My_Server"},
		{"lsp-@vue/language-server", "lsp-_vue_language-server"},
		{"mcp-a:b\\c", "mcp-a_b_c"},
		{"lsp-rust_analyzer.v2", "lsp-rust_analyzer.v2"},
		{"mcp-ñandú", "mcp-_and_"},
		{"", "proc"},
	} {
		require.Equal(t, tc.want, SanitizeLabel(tc.in), "label %q", tc.in)
	}
	require.Len(t, SanitizeLabel(strings.Repeat("x", 300)), maxLabel)
}

func TestUnitName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "harness-lsp-gopls-123-4.scope", UnitName("lsp-gopls", 123, 4))
	require.Equal(t, "harness-mcp-my_server-1-1.scope", UnitName("mcp-my server", 1, 1))
	require.Equal(t, "harness-shell-9-2.scope", UnitName("shell", 9, 2))
}

func TestScopeProps(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"OOMPolicy=continue", "MemoryMax=50%", "MemorySwapMax=0"}, scopeProps("50%"))
	require.Equal(t, []string{"OOMPolicy=continue", "MemoryMax=8G", "MemorySwapMax=0"}, scopeProps("8G"))
	require.Equal(t, []string{"OOMPolicy=continue"}, scopeProps("infinity"))
	require.Equal(t, []string{"OOMPolicy=continue"}, scopeProps("INFINITY"))
}

// TestWrapArgs pins the systemd-run invocation: a quiet, collected user
// scope with the policy's properties, and the command after "--" exactly
// as given, so arguments that look like systemd-run options reach the
// command untouched.
func TestWrapArgs(t *testing.T) {
	t.Parallel()

	got := wrapArgs("harness-lsp-gopls-1-1.scope", scopeProps("50%"), "/usr/bin/gopls", []string{"-rpc.trace", "--", "serve"})
	require.Equal(t, []string{
		"--user", "--scope", "--quiet", "--collect", "--unit=harness-lsp-gopls-1-1.scope",
		"--property=OOMPolicy=continue", "--property=MemoryMax=50%", "--property=MemorySwapMax=0",
		"--", "/usr/bin/gopls", "-rpc.trace", "--", "serve",
	}, got)

	require.Equal(t, []string{
		"--user", "--scope", "--quiet", "--collect", "--unit=u.scope",
		"--property=OOMPolicy=continue", "--", "true",
	}, wrapArgs("u.scope", scopeProps("infinity"), "true", nil))
}

// TestPolicyOff checks that "off" leaves commands as they are, with no
// scope, on every platform.
func TestPolicyOff(t *testing.T) {
	const envVar = "PROCSCOPE_TEST_LIMIT_OFF"
	t.Setenv(envVar, "off")
	p := NewPolicy("test", envVar, "50%", "Test children")

	name, args, scope := p.Command("lsp-x", "sh", []string{"-c", "true"})
	require.Equal(t, "sh", name)
	require.Equal(t, []string{"-c", "true"}, args)
	require.Nil(t, scope)
}

// TestPolicyLimit checks the cap's source: the environment, then the
// fallback.
func TestPolicyLimit(t *testing.T) {
	const envVar = "PROCSCOPE_TEST_LIMIT"
	p := NewPolicy("test", envVar, "50%", "Test children")
	t.Setenv(envVar, "")
	require.Equal(t, "50%", p.limit())
	t.Setenv(envVar, " 2G ")
	require.Equal(t, "2G", p.limit())
}

// TestNilScope checks that an uncontained child's scope is safe to use.
func TestNilScope(t *testing.T) {
	t.Parallel()

	var s *Scope
	require.Empty(t, s.Unit())
	require.Empty(t, s.Dir())
	s.Kill()
	s.RaiseOOMScore()
	_, ok := s.OOMKills()
	require.False(t, ok)
	_, ok = s.MemoryMax()
	require.False(t, ok)
}
