package tools

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// breakGated must find exactly what the regexp finds over the whole
// buffer: the same leftmost match, or none. The inputs are built from
// the pieces that decide a credential match - keywords, word characters
// glued to them, colons, newlines, long runs, multibyte text - so the
// stretch edges are exercised as well as the matches.
func TestCredPromptScanMatchesRegexp(t *testing.T) {
	t.Parallel()

	pieces := []string{
		"password", "Password", "PASSPHRASE", "pin", "PIN", "[sudo]", "[sudo] password for me",
		"contraseña", "mot de passe", "pass phrase", "Passwort",
		"x", "spin", "pinned", "_", "1", " ", "  ", ":", "::", "\n", "\r\n", "é", "ñ", "\x1b[0m",
		strings.Repeat("a", 70), strings.Repeat("é", 70), strings.Repeat(" ", 300),
		"Enter ", "for user: ", "ok  \tpkg\t0.01s\n", "file.go:12:3: error",
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range 20000 {
		var sb strings.Builder
		for range rng.IntN(12) {
			sb.WriteString(pieces[rng.IntN(len(pieces))])
		}
		in := []byte(sb.String())
		require.Equal(t, credPromptRe.FindIndex(in), credPromptScan.FindIndex(in), "case %d: %q", i, in)
	}
}

func TestCredPromptScanCases(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"[sudo] password for alice: ",
		"Enter passphrase for key '/home/a/.ssh/id_ed25519': ",
		"Password:",
		"spin: not a prompt",
		"a: b: c: Password: ",
		strings.Repeat("x", 1000) + "\nPIN code:",
		"no colon here password",
	} {
		require.Equal(t, credPromptRe.FindIndex([]byte(in)), credPromptScan.FindIndex([]byte(in)), "%q", in)
	}
}

func BenchmarkCredPromptScan(b *testing.B) {
	var sb strings.Builder
	for i := 0; sb.Len() < 600*1024; i++ {
		fmt.Fprintf(&sb, "ok  \tgithub.com/stubbedev/harness/internal/pkg%d\t0.%03ds\n", i, i%1000)
		if i%10 == 0 {
			fmt.Fprintf(&sb, "internal/pkg%d/file.go:%d:%d: undefined: thing\n", i, i, i%80)
		}
	}
	in := []byte(sb.String())
	b.Run("regexp", func(b *testing.B) {
		for b.Loop() {
			_ = credPromptRe.FindIndex(in)
		}
	})
	b.Run("gated", func(b *testing.B) {
		for b.Loop() {
			_ = credPromptScan.FindIndex(in)
		}
	})
}
