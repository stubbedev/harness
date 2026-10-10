package vt10x

import (
	"fmt"
	"strings"
	"testing"
)

// buildLog is about 600KB of build output, one line per package, the
// shape a terminal session feeds the emulator when a test run finishes.
func buildLog(color bool) []byte {
	var sb strings.Builder
	for i := 0; sb.Len() < 600*1024; i++ {
		if color {
			fmt.Fprintf(&sb, "\x1b[32mok\x1b[0m  \texample.com/module/internal/pkg%d\t0.%03ds\r\n", i, i%1000)
		} else {
			fmt.Fprintf(&sb, "ok  \texample.com/module/internal/pkg%d\t0.%03ds\r\n", i, i%1000)
		}
	}
	return []byte(sb.String())
}

func benchWrite(b *testing.B, in []byte) {
	for b.Loop() {
		term := New(WithSize(280, 80))
		for off := 0; off < len(in); off += 4096 {
			_, _ = term.Write(in[off:min(off+4096, len(in))])
		}
	}
}

func BenchmarkWritePlainLog(b *testing.B) { benchWrite(b, buildLog(false)) }

func BenchmarkWriteColorLog(b *testing.B) { benchWrite(b, buildLog(true)) }
