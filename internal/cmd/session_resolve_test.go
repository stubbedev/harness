package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/session"
)

func resolveFromList(ctx context.Context, id string, sessions []session.Session) (session.Session, error) {
	get := func(context.Context, string) (session.Session, error) {
		return session.Session{}, errors.New("not found")
	}
	list := func(context.Context) ([]session.Session, error) { return sessions, nil }
	return resolveSessionPrefix(ctx, id, get, list, sessionID, nil)
}

func TestResolveSessionPrefixMatchesHashPrefix(t *testing.T) {
	t.Parallel()

	only := session.Session{ID: "only-session"}
	got, err := resolveFromList(t.Context(), session.HashID(only.ID)[:7], []session.Session{only})
	require.NoError(t, err)
	require.Equal(t, only.ID, got.ID)
}

func TestResolveSessionPrefixRejectsEmptyID(t *testing.T) {
	t.Parallel()

	_, err := resolveFromList(t.Context(), "", []session.Session{{ID: "only-session"}})
	require.ErrorIs(t, err, errEmptySessionID)
}
