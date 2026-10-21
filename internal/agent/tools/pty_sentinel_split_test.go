package tools

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func twoPassSentinelSplit(m sentinel, drained string) (string, *int, string, bool) {
	loc := m.parse.FindStringIndex(drained)
	if loc == nil {
		return drained, nil, "", false
	}
	match := m.parse.FindStringSubmatch(drained)
	var exitCode *int
	if code, err := strconv.Atoi(match[1]); err == nil {
		exitCode = &code
	}
	return drained[:loc[0]], exitCode, match[2], true
}

func TestSentinelSplitParsesExitCodeAndCwd(t *testing.T) {
	t.Parallel()

	m := newSentinel(posixDialect)
	tag := strings.TrimSuffix(strings.TrimPrefix(m.parse.String(), "__exit_"), `:(-?\d+)@(.*)__`)
	drained := "line one\nline two\n__exit_" + tag + ":-2@/home/me/my_dir__\r\n$ "

	cut, code, cwd, found := m.split(drained)
	require.True(t, found)
	require.Equal(t, "line one\nline two\n", cut)
	require.NotNil(t, code)
	require.Equal(t, -2, *code)
	require.Equal(t, "/home/me/my_dir", cwd)
}

func TestSentinelSplitWithoutSentinelKeepsEverything(t *testing.T) {
	t.Parallel()

	m := newSentinel(posixDialect)
	cut, code, cwd, found := m.split("output with __exit_other:0@/x__\n")
	require.False(t, found)
	require.Equal(t, "output with __exit_other:0@/x__\n", cut)
	require.Nil(t, code)
	require.Empty(t, cwd)
}

func TestSentinelSplitMatchesTheTwoPassParse(t *testing.T) {
	t.Parallel()

	m := newSentinel(posixDialect)
	tag := strings.TrimSuffix(strings.TrimPrefix(m.parse.String(), "__exit_"), `:(-?\d+)@(.*)__`)
	mark := "__exit_" + tag
	for _, drained := range []string{
		"",
		"plain output\n",
		mark + ":0@/__",
		"a\n" + mark + ":127@/tmp/a__b__\nprompt",
		"a\n" + mark + ":1@/one__\n" + mark + ":2@/two__\n",
		"a\n" + mark + ":99999999999999999999@/big__\n",
	} {
		wantCut, wantCode, wantCwd, wantFound := twoPassSentinelSplit(m, drained)
		cut, code, cwd, found := m.split(drained)
		require.Equal(t, wantFound, found, drained)
		require.Equal(t, wantCut, cut, drained)
		require.Equal(t, wantCode, code, drained)
		require.Equal(t, wantCwd, cwd, drained)
	}
}

func sentinelBenchInput(m sentinel) string {
	tag := strings.TrimSuffix(strings.TrimPrefix(m.parse.String(), "__exit_"), `:(-?\d+)@(.*)__`)
	return strings.Repeat("some build output line that goes on for a while\n", 4<<20/48) +
		"__exit_" + tag + ":0@/home/me/project__\r\n$ "
}

func BenchmarkSentinelSplit(b *testing.B) {
	m := newSentinel(posixDialect)
	drained := sentinelBenchInput(m)
	b.SetBytes(int64(len(drained)))
	b.ReportAllocs()
	for b.Loop() {
		_, _, _, found := m.split(drained)
		require.True(b, found)
	}
}

func BenchmarkSentinelSplitTwoPass(b *testing.B) {
	m := newSentinel(posixDialect)
	drained := sentinelBenchInput(m)
	b.SetBytes(int64(len(drained)))
	b.ReportAllocs()
	for b.Loop() {
		_, _, _, found := twoPassSentinelSplit(m, drained)
		require.True(b, found)
	}
}
