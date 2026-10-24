package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

// TestPreparePrompt_DropsImageOnlyTurnWithoutImageSupport pins the history
// side of an image-only prompt: a model that cannot read images must not
// receive the user turn with its images stripped, which providers reject as
// an empty message.
func TestPreparePrompt_DropsImageOnlyTurnWithoutImageSupport(t *testing.T) {
	env := testEnv(t)
	agent := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: ""},
			message.BinaryContent{Path: "cat.png", MIMEType: "image/png", Data: []byte("png")},
		},
	})
	require.NoError(t, err)

	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, files := agent.preparePrompt(msgs, false)
	require.Empty(t, history, "an image-only turn has nothing to say to a text-only model")
	require.Empty(t, files)

	history, files = agent.preparePrompt(msgs, true)
	require.Len(t, history, 1)
	file, ok := fantasy.AsMessagePart[fantasy.FilePart](history[0].Content[0])
	require.True(t, ok)
	require.Equal(t, "cat.png", file.Filename)
	require.Empty(t, files, "a stored attachment is already part of the history")
}
