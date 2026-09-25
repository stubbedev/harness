package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/presence"
)

func peerFixture() []presence.Record {
	now := time.Now()
	return []presence.Record{{
		ID:      "peer1",
		PID:     4242,
		Started: now.Add(-time.Hour),
		Beat:    now,
		Title:   "fix the tui rows",
		Busy:    true,
		Files: []presence.Activity{
			{Path: "internal/ui/model/ui.go", At: now.Add(-time.Second)},
			{Path: "internal/ui/model/history.go", At: now.Add(-time.Minute)},
		},
	}}
}

func TestPeerNoteFirstEmission(t *testing.T) {
	t.Parallel()
	note := peerNote(nil, peerFixture())
	require.True(t, strings.HasPrefix(note, peerActivityTag))
	require.Contains(t, note, "pid 4242")
	require.Contains(t, note, `"fix the tui rows"`)
	require.Contains(t, note, "mid-turn")
	require.Contains(t, note, "internal/ui/model/ui.go")
	require.Contains(t, note, "Treat the files listed above as contended")
	require.True(t, strings.HasSuffix(note, "</peer_activity>\n</system_reminder>"))
}

func TestPeerNoteNoPeersNoHistory(t *testing.T) {
	t.Parallel()
	require.Empty(t, peerNote(nil, nil))
}

func TestPeerNoteUnchangedStateIsSilent(t *testing.T) {
	t.Parallel()
	msgs := []fantasy.Message{fantasy.NewUserMessage(renderPeers(peerFixture()))}
	require.Empty(t, peerNote(msgs, peerFixture()))
}

func TestPeerNoteChangedStateReEmits(t *testing.T) {
	t.Parallel()
	peers := peerFixture()
	msgs := []fantasy.Message{fantasy.NewUserMessage(renderPeers(peers))}
	peers[0].Files = append(peers[0].Files, presence.Activity{Path: "internal/ui/x.go", At: time.Now()})
	require.NotEmpty(t, peerNote(msgs, peers))
}

func TestPeerNoteDepartureClosesOnce(t *testing.T) {
	t.Parallel()
	msgs := []fantasy.Message{fantasy.NewUserMessage(renderPeers(peerFixture()))}
	note := peerNote(msgs, nil)
	require.Equal(t, peerDepartedNote, note)

	closed := []fantasy.Message{fantasy.NewUserMessage(note)}
	require.Empty(t, peerNote(closed, nil), "the closing note must not repeat")
	require.NotEmpty(t, peerNote(closed, peerFixture()), "a returning peer re-opens the subject")
}

func TestRenderPeersCapsFileList(t *testing.T) {
	t.Parallel()
	now := time.Now()
	files := make([]presence.Activity, 10)
	for i := range files {
		files[i] = presence.Activity{Path: fmt.Sprintf("f%d.go", i), At: now.Add(-time.Duration(i) * time.Second)}
	}
	rendered := renderPeers([]presence.Record{{ID: "p", PID: 1, Files: files}})
	require.Contains(t, rendered, "(+4 more)")
	require.Contains(t, rendered, "f0.go")
	require.NotContains(t, rendered, "f6.go")
}

func TestRenderPeersCollapsesDuplicatePaths(t *testing.T) {
	t.Parallel()
	now := time.Now()
	files := make([]presence.Activity, 10)
	for i := range files {
		files[i] = presence.Activity{Path: "f.go", At: now.Add(-time.Duration(i) * time.Second)}
	}
	rendered := renderPeers([]presence.Record{{ID: "p", PID: 1, Files: files}})
	require.Equal(t, 1, strings.Count(rendered, "f.go"), "duplicate paths collapse to one entry")
	require.NotContains(t, rendered, "(+", "nothing is hidden when duplicates collapse")
}

func TestRenderPeersOmitsEmptyDetails(t *testing.T) {
	t.Parallel()
	rendered := renderPeers([]presence.Record{{ID: "p", PID: 1}})
	require.Contains(t, rendered, "- pid 1")
	require.NotContains(t, rendered, "recently wrote")
	require.NotContains(t, rendered, "mid-turn")
}
