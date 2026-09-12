package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func newTestYesNo(t *testing.T) *YesNo {
	t.Helper()
	s := styles.CharmtonePantera()
	return NewYesNo(&s, question.Question{
		ID:   "q1",
		Type: question.TypeYesNo,
		Text: "Deploy build 412 to production?",
	})
}

// The form reads answers back through Response, which reports the live
// selection. A shortcut that recorded its answer anywhere else was
// discarded: pressing "y" submitted No, because the selection still sat
// on the default.
func TestYesNoKeys(t *testing.T) {
	t.Parallel()

	press := func(t *testing.T, key string) (*YesNo, bool) {
		t.Helper()
		d := newTestYesNo(t)
		done, _ := d.HandleKey(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		return d, done
	}

	t.Run("y answers yes", func(t *testing.T) {
		t.Parallel()

		d, done := press(t, "y")
		require.True(t, done)
		resp := d.Response()
		require.NotNil(t, resp.Yes)
		require.True(t, *resp.Yes)
	})

	t.Run("n answers no", func(t *testing.T) {
		t.Parallel()

		d, done := press(t, "n")
		require.True(t, done)
		resp := d.Response()
		require.NotNil(t, resp.Yes)
		require.False(t, *resp.Yes)
	})

	t.Run("the default is no", func(t *testing.T) {
		t.Parallel()

		d := newTestYesNo(t)
		resp := d.Response()
		require.NotNil(t, resp.Yes)
		require.False(t, *resp.Yes)
	})

	t.Run("moving the selection then confirming answers yes", func(t *testing.T) {
		t.Parallel()

		d := newTestYesNo(t)
		done, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
		require.False(t, done)
		done, _ = d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		require.True(t, done)

		resp := d.Response()
		require.NotNil(t, resp.Yes)
		require.True(t, *resp.Yes)
	})

	// Tabbing away from an answered question and back must keep the
	// answer: the form re-reads Response every time it collects.
	t.Run("the answer survives a re-read", func(t *testing.T) {
		t.Parallel()

		d, _ := press(t, "y")
		first := d.Response()
		second := d.Response()
		require.Equal(t, *first.Yes, *second.Yes)
		require.True(t, *second.Yes)
	})
}
