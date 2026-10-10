package vt10x

import (
	"strconv"
	"testing"
)

func TestCSIParse(t *testing.T) {
	var csi csiEscape
	csi.reset()
	csi.buf = []byte("s")
	csi.parse()
	if csi.mode != 's' || csi.arg(0, 17) != 17 || len(csi.args) != 0 {
		t.Fatal("CSI parse mismatch")
	}

	csi.reset()
	csi.buf = []byte("31T")
	csi.parse()
	if csi.mode != 'T' || csi.arg(0, 0) != 31 || len(csi.args) != 1 {
		t.Fatal("CSI parse mismatch")
	}

	csi.reset()
	csi.buf = []byte("48;2f")
	csi.parse()
	if csi.mode != 'f' || csi.arg(0, 0) != 48 || csi.arg(1, 0) != 2 || len(csi.args) != 2 {
		t.Fatal("CSI parse mismatch")
	}

	csi.reset()
	csi.buf = []byte("?25l")
	csi.parse()
	if csi.mode != 'l' || csi.arg(0, 0) != 25 || csi.priv != true || len(csi.args) != 1 {
		t.Fatal("CSI parse mismatch")
	}
}

// atoiBytes agrees with strconv.Atoi, which it replaced, on what it
// accepts and what it returns.
func TestAtoiBytesMatchesAtoi(t *testing.T) {
	inputs := []string{
		"", "0", "1", "38", "-5", "+7", "-", "+", "1a", "a1", " 1", "1 ",
		"007", "9223372036854775807", "9223372036854775808",
		"-9223372036854775808", "-9223372036854775809", "99999999999999999999",
		">", "?25", "1.5",
	}
	for _, in := range inputs {
		want, err := strconv.Atoi(in)
		got, ok := atoiBytes([]byte(in))
		if ok != (err == nil) || (ok && got != want) {
			t.Fatalf("%q: got (%d, %v), Atoi (%d, %v)", in, got, ok, want, err)
		}
	}
}

func TestCSIParseArgs(t *testing.T) {
	for in, want := range map[string][]int{
		"1;32m":   {1, 32},
		"m":       nil,
		"0m":      {0},
		"1;;2m":   {1},
		";5H":     nil,
		"38;5;2m": {38, 5, 2},
		">c":      nil,
	} {
		var c csiEscape
		for i := range len(in) {
			c.put(in[i])
		}
		if len(c.args) != len(want) {
			t.Fatalf("%q: args %v, want %v", in, c.args, want)
		}
		for i := range want {
			if c.args[i] != want[i] {
				t.Fatalf("%q: args %v, want %v", in, c.args, want)
			}
		}
	}
}
