package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// oomTerminal is a session that counts out-of-memory kills. Only the
// counter is real; nothing else of the terminal is used.
type oomTerminal struct {
	ptyTerminal
	kills int
	limit int64
}

func (o *oomTerminal) OOMKills() (int, bool) { return o.kills, true }

func (o *oomTerminal) MemoryLimit() (int64, bool) { return o.limit, o.limit > 0 }

func TestOOMNoteReportsEachKillOnce(t *testing.T) {
	t.Parallel()

	s := &oomTerminal{limit: 24 << 30}
	r := &ptyRunner{session: s}
	require.Empty(t, r.oomNote())

	s.kills = 1
	require.Equal(t, "[out of memory: the kernel killed a process at the session's 24 GiB memory limit]", r.oomNote())
	require.Empty(t, r.oomNote(), "a kill is reported once")

	s.kills = 3
	s.limit = 0
	require.Equal(t, "[out of memory: the kernel killed 2 processes]", r.oomNote())

	// A replacement shell counts from its own start.
	r.session = &oomTerminal{kills: 1}
	require.Equal(t, "[out of memory: the kernel killed a process]", r.oomNote())
}

func TestOOMNoteWithoutCounter(t *testing.T) {
	t.Parallel()

	require.Empty(t, (&ptyRunner{}).oomNote())
}
