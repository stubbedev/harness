package proto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/checkpoints"
)

func malformedUUIDs() []string {
	id := uuid.New().String()
	return []string{
		"",
		"not-a-uuid",
		"../etc",
		strings.ToUpper(id),
		"{" + id + "}",
		"urn:uuid:" + id,
		strings.ReplaceAll(id, "-", ""),
		id + "\n",
	}
}

func TestParseSessionID(t *testing.T) {
	t.Parallel()
	id := uuid.New().String()
	got, err := ParseSessionID(id)
	require.NoError(t, err)
	require.Equal(t, SessionID(id), got)
	for _, bad := range malformedUUIDs() {
		_, err := ParseSessionID(bad)
		require.ErrorIs(t, err, ErrInvalidSessionID, "%q", bad)
	}
}

func TestParseClientID(t *testing.T) {
	t.Parallel()
	id := uuid.New().String()
	got, err := ParseClientID(id)
	require.NoError(t, err)
	require.Equal(t, ClientID(id), got)
	for _, bad := range malformedUUIDs() {
		_, err := ParseClientID(bad)
		require.ErrorIs(t, err, ErrInvalidClientID, "%q", bad)
	}
}

func TestValidateRunID(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateRunID(""))
	require.NoError(t, ValidateRunID(uuid.New().String()))
	for _, bad := range malformedUUIDs()[1:] {
		require.ErrorIs(t, ValidateRunID(bad), ErrInvalidRunID, "%q", bad)
	}
}

func TestRewindRequestDecodesMode(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]checkpoints.Mode{
		`{"message_id":"m"}`:                       checkpoints.ModeBoth,
		`{"message_id":"m","mode":""}`:             checkpoints.ModeBoth,
		`{"message_id":"m","mode":"files"}`:        checkpoints.ModeFiles,
		`{"message_id":"m","mode":"conversation"}`: checkpoints.ModeConversation,
	} {
		var req RewindRequest
		require.NoError(t, json.Unmarshal([]byte(body), &req), body)
		require.Equal(t, want, req.Mode, body)
		require.Equal(t, "m", req.MessageID, body)
	}
	var req RewindRequest
	require.Error(t, json.Unmarshal([]byte(`{"message_id":"m","mode":"bogus"}`), &req))
}

func TestRewindRequestRoundTrips(t *testing.T) {
	t.Parallel()
	in := RewindRequest{MessageID: "m", Mode: checkpoints.ModeFiles}
	data, err := json.Marshal(in)
	require.NoError(t, err)
	require.JSONEq(t, `{"message_id":"m","mode":"files"}`, string(data))
	var out RewindRequest
	require.NoError(t, json.Unmarshal(data, &out))
	require.Equal(t, in, out)
}
