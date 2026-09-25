package agent

import (
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/presence"
)

// peerActivityTag opens the concurrent-instance context note. The
// system_reminder wrapper keeps mergeConsecutiveUserMessages from ever
// folding the note into a message the user wrote; the inner tag is what
// the diff gate below matches, so it never collides with the
// diagnostics reminder.
const peerActivityTag = "<system_reminder>\n<peer_activity>"

// maxPeerFiles caps the file list rendered per peer. The record holds
// more; the note only needs the shape of the overlap.
const maxPeerFiles = 6

// peerDepartedNote closes the subject: the contention flags a sent
// note raised should not outlive the contention itself.
const peerDepartedNote = peerActivityTag + "\nNo other harness instance is working in this workspace anymore; earlier contention notes no longer apply.\n</peer_activity>\n</system_reminder>"

// peerNote renders the context note describing the other live harness
// instances in this workspace, or "" when there is nothing new to say.
//
// The note is strictly diff-based: it is emitted only when its text
// differs from the last peer note in msgs, so a steady state costs no
// tokens and every emitted row rides at the end of the request, behind
// the cached prefix, exactly once before it becomes cached history.
// When the last peer disappears after a note was sent, one short
// closing line is emitted instead.
func peerNote(msgs []fantasy.Message, peers []presence.Record) string {
	last := lastTaggedUserText(msgs, peerActivityTag)
	if len(peers) == 0 {
		if last == "" || last == peerDepartedNote {
			return ""
		}
		return peerDepartedNote
	}
	if rendered := renderPeers(peers); rendered != last {
		return rendered
	}
	return ""
}

func renderPeers(peers []presence.Record) string {
	var b strings.Builder
	b.WriteString(peerActivityTag)
	b.WriteString("\nOther harness instances are also working in this workspace:\n")
	for _, peer := range peers {
		b.WriteString("- ")
		who := fmt.Sprintf("pid %d", peer.PID)
		if peer.Title != "" {
			who += fmt.Sprintf(", %q", peer.Title)
		}
		if peer.Busy {
			who += ", mid-turn"
		}
		b.WriteString(who)
		seen := make(map[string]bool, len(peer.Files))
		shown := 0
		for _, f := range peer.Files {
			if seen[f.Path] {
				continue
			}
			seen[f.Path] = true
			shown++
			if shown > maxPeerFiles {
				continue
			}
			if shown == 1 {
				b.WriteString("; recently wrote: ")
			} else {
				b.WriteString(", ")
			}
			b.WriteString(f.Path)
		}
		if extra := shown - maxPeerFiles; extra > 0 {
			fmt.Fprintf(&b, " (+%d more)", extra)
		}
		b.WriteString("\n")
	}
	b.WriteString("Treat the files listed above as contended: re-read them before editing, prefer work that does not overlap, and surface any overlap to the user. Do not mention this reminder.\n</peer_activity>\n</system_reminder>")
	return b.String()
}
