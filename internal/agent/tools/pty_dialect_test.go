package tools

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/term"
)

// TestDialectFor pins which protocol each shell gets. An unrecognised
// shell falls back to POSIX rather than to no protocol at all.
func TestDialectFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
	}{
		{"/bin/bash", posixDialect.sentinelCmd},
		{"/usr/bin/zsh", posixDialect.sentinelCmd},
		{`C:\Program Files\PowerShell\7\pwsh.exe`, powershellDialect.sentinelCmd},
		{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.EXE`, powershellDialect.sentinelCmd},
		{`C:\Windows\System32\cmd.exe`, cmdDialect.sentinelCmd},
		{"/usr/local/bin/some-new-shell", posixDialect.sentinelCmd},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, dialectFor(tc.path).sentinelCmd)
		})
	}
}

// TestDialectMarkersRoundTrip is the property every dialect has to hold:
// the command it prints the exit marker with must produce text its own
// pattern parses back into an exit code and a working directory. The
// commands themselves can only be run on their own platform, so this
// checks the shape each one is contracted to print.
func TestDialectMarkersRoundTrip(t *testing.T) {
	t.Parallel()

	dialects := map[string]shellDialect{
		"posix":      posixDialect,
		"powershell": powershellDialect,
		"cmd":        cmdDialect,
	}
	for name, d := range dialects {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mark := newSentinel(d)
			require.NotEmpty(t, mark.cmd)
			require.NotEmpty(t, mark.begin)

			// The tag must have been substituted, not left as a verb. A
			// dialect may still carry its own verbs past that point --
			// POSIX prints through printf, whose %s is the shell's, not
			// ours -- so this checks the tag landed rather than that no
			// percent survives.
			require.Regexp(t, `__exit_[0-9a-f]+`, mark.cmd)
			require.Regexp(t, `__begin_[0-9a-f]+`, mark.begin)

			tag := regexp.MustCompile(`__begin_([0-9a-f]+):ok__`).FindStringSubmatch(mark.beginRe.String())
			require.Len(t, tag, 2, "the fence pattern carries the session tag")

			printed := fmt.Sprintf("__exit_%s:%d@%s__", tag[1], 42, "/tmp/some_dir")
			got := mark.parse.FindStringSubmatch(printed)
			require.Len(t, got, 3, "the dialect's own pattern must parse what it prints")
			require.Equal(t, "42", got[1])
			require.Equal(t, "/tmp/some_dir", got[2])
			require.True(t, mark.loose.MatchString(printed))
			require.True(t, mark.beginRe.MatchString(fmt.Sprintf("__begin_%s:ok__", tag[1])))
		})
	}
}

// TestShellKinds pins the shell-name mapping the dialects key off.
func TestShellKinds(t *testing.T) {
	t.Parallel()

	require.Equal(t, term.KindPosix, term.KindOf("/bin/bash"))
	require.Equal(t, term.KindPowerShell, term.KindOf("pwsh"))
	require.Equal(t, term.KindPowerShell, term.KindOf(`C:\x\PowerShell.exe`))
	require.Equal(t, term.KindCmd, term.KindOf("CMD.EXE"))
	require.Equal(t, term.KindUnknown, term.KindOf("/usr/bin/python"))
}
