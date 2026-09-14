package checkpoints

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

// newTestService wires a checkpoints service against a real SQLite
// database in a temporary data directory, with a separate temporary
// directory playing the user's working tree.
func newTestService(t *testing.T) (*Service, *session.Session, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	workingDir := t.TempDir()
	dataDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)
	svc := NewService(q, workingDir, dataDir, messages, sessions)
	require.True(t, svc.Enabled())

	sess, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	return svc, &sess, workingDir, dataDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestSnapshotAndRewindFiles(t *testing.T) {
	t.Parallel()
	svc, sess, workDir, _ := newTestService(t)

	writeFile(t, filepath.Join(workDir, "a.txt"), "one")
	writeFile(t, filepath.Join(workDir, "nested", "b.txt"), "bee")

	turn1 := createUserMessage(t, svc, sess.ID, "first prompt")

	// The turn happens: files change, one is deleted, one is added.
	writeFile(t, filepath.Join(workDir, "a.txt"), "two")
	require.NoError(t, os.Remove(filepath.Join(workDir, "nested", "b.txt")))
	writeFile(t, filepath.Join(workDir, "c.txt"), "created by the turn")

	turn2 := createUserMessage(t, svc, sess.ID, "second prompt")

	cps, err := svc.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, cps, 2)

	// Rewinding files to turn 2 restores the tree as it was when the
	// second prompt was submitted: still mid-turn state.
	require.NoError(t, svc.Rewind(t.Context(), sess.ID, turn2.ID, ModeFiles))
	content, err := os.ReadFile(filepath.Join(workDir, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "two", string(content))

	// Rewinding to turn 1 puts the tree back to submit time of the
	// first prompt: content reverted, deleted file restored, file the
	// later turn created removed.
	require.NoError(t, svc.Rewind(t.Context(), sess.ID, turn1.ID, ModeFiles))
	content, err = os.ReadFile(filepath.Join(workDir, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "one", string(content))
	content, err = os.ReadFile(filepath.Join(workDir, "nested", "b.txt"))
	require.NoError(t, err)
	require.Equal(t, "bee", string(content))
	require.NoFileExists(t, filepath.Join(workDir, "c.txt"))
}

func TestRewindRespectsGitignore(t *testing.T) {
	t.Parallel()
	svc, sess, workDir, _ := newTestService(t)

	writeFile(t, workDir+string(os.PathSeparator)+".gitignore", "ignored/\n")
	writeFile(t, filepath.Join(workDir, "ignored", "big.bin"), "never snapshotted")
	writeFile(t, filepath.Join(workDir, "kept.txt"), "kept")

	turn := createUserMessage(t, svc, sess.ID, "prompt")

	require.NoError(t, os.Remove(filepath.Join(workDir, "kept.txt")))
	writeFile(t, filepath.Join(workDir, "ignored", "big.bin"), "changed after snapshot")

	require.NoError(t, svc.Rewind(t.Context(), sess.ID, turn.ID, ModeFiles))

	// Tracked file comes back...
	require.FileExists(t, filepath.Join(workDir, "kept.txt"))
	// ...ignored file was never snapshotted, so it keeps its later
	// content.
	content, err := os.ReadFile(filepath.Join(workDir, "ignored", "big.bin"))
	require.NoError(t, err)
	require.Equal(t, "changed after snapshot", string(content))
}

func TestRewindConversation(t *testing.T) {
	t.Parallel()
	svc, sess, _, _ := newTestService(t)

	turn1 := createUserMessage(t, svc, sess.ID, "first")
	_, err := svc.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
	})
	require.NoError(t, err)
	turn2 := createUserMessage(t, svc, sess.ID, "second")
	_, err = svc.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
	})
	require.NoError(t, err)

	msgs, err := svc.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 4)

	// Rewinding the conversation to turn 2 removes turn 2 and its
	// answer, leaving turn 1 and its answer.
	require.NoError(t, svc.Rewind(t.Context(), sess.ID, turn2.ID, ModeConversation))

	msgs, err = svc.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	require.Equal(t, turn1.ID, msgs[0].ID)
}

func TestRewindConversationClearsSummaryPointer(t *testing.T) {
	t.Parallel()
	svc, sess, _, _ := newTestService(t)

	turn := createUserMessage(t, svc, sess.ID, "prompt")
	summary, err := svc.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:             message.Assistant,
		IsSummaryMessage: true,
	})
	require.NoError(t, err)
	sess.SummaryMessageID = summary.ID
	saved, err := svc.sessions.Save(t.Context(), *sess)
	require.NoError(t, err)
	sess = &saved

	require.NoError(t, svc.Rewind(t.Context(), sess.ID, turn.ID, ModeConversation))

	updated, err := svc.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Empty(t, updated.SummaryMessageID)
}

func TestRewindFilesWithoutCheckpoint(t *testing.T) {
	t.Parallel()
	svc, sess, _, _ := newTestService(t)

	// A message that never got a snapshot (snapshot failed, or the
	// turn predates checkpoints).
	turn, err := svc.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "no snapshot"}},
	})
	require.NoError(t, err)

	err = svc.Rewind(t.Context(), sess.ID, turn.ID, ModeFiles)
	require.Error(t, err)
	// Conversation mode still works without a snapshot.
	require.NoError(t, svc.Rewind(t.Context(), sess.ID, turn.ID, ModeConversation))
	msgs, err := svc.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Empty(t, msgs)
}

func TestSnapshotSkipsDataDirInsideWorktree(t *testing.T) {
	t.Parallel()
	svc, sess, workDir, dataDir := newTestService(t)

	// A legacy in-repo data directory: present in the working tree but
	// never part of a snapshot.
	legacyData := filepath.Join(workDir, ".harness")
	writeFile(t, filepath.Join(legacyData, "state.yaml"), "options: {}")
	svc.dataDir = legacyData
	require.DirExists(t, legacyData)
	require.NotEqual(t, legacyData, dataDir)

	turn := createUserMessage(t, svc, sess.ID, "prompt")
	require.NoError(t, svc.Rewind(t.Context(), sess.ID, turn.ID, ModeFiles))
	require.FileExists(t, filepath.Join(legacyData, "state.yaml"))
}

func TestDeleteSessionRemovesObjects(t *testing.T) {
	t.Parallel()
	svc, sess, workDir, dataDir := newTestService(t)
	_ = workDir

	createUserMessage(t, svc, sess.ID, "prompt")

	objects := filepath.Join(dataDir, "checkpoints", sess.ID)
	require.DirExists(t, objects)

	svc.DeleteSession(sess.ID)
	require.NoDirExists(t, objects)

	// Deleting rows via the sessions foreign key also drops the rows.
	cps, err := svc.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, cps, 1, "DeleteSession only reclaims disk objects; rows follow the session")
}

// createUserMessage persists a user message and snapshots it, the way
// a dispatched turn does.
func createUserMessage(t *testing.T, svc *Service, sessionID, prompt string) message.Message {
	t.Helper()
	msg, err := svc.messages.Create(t.Context(), sessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: prompt}},
	})
	require.NoError(t, err)
	require.NoError(t, svc.Snapshot(t.Context(), sessionID, msg.ID))
	return msg
}

// TestRejectsUnsafeIdentifiers pins the guard on what reaches git and
// the data directory. Session and message IDs are generated by Harness,
// so anything that could walk out of the data directory or be read by
// git as a flag is a bug somewhere upstream, and is refused here rather
// than acted on.
func TestRejectsUnsafeIdentifiers(t *testing.T) {
	t.Parallel()
	svc, _, _, dataDir := newTestService(t)

	for _, id := range []string{
		"../../escape",
		"nested/child",
		"-upload-pack=touch",
		"",
		`..\windows`,
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			require.Error(t, svc.ensureShadow(t.Context(), id),
				"session id %q must be refused", id)

			_, err := svc.git(t.Context(), id, "status")
			require.Error(t, err)

			require.Error(t, removeShadow(dataDir, id))
		})
	}

	// A message id is a ref name and a commit message; the same rule
	// applies to it.
	_, err := svc.commitTree(t.Context(), "session-1", "../../escape")
	require.Error(t, err)

	// A commit has to look like an object name before it goes back to git.
	require.Error(t, svc.restore(t.Context(), "session-1", "--upload-pack=touch"))
}
