package dialog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// TestSessionItemIsPickerItem pins the shared picker row contract: a
// session row exposes the wrapped session as the value, its title as
// the label and its timestamp as the right column.
func TestSessionItemIsPickerItem(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	var item PickerItem = &SessionItem{
		Versioned: list.NewVersioned(),
		Session:   session.Session{ID: "sess-1", Title: "My Session"},
		t:         &sty,
	}

	assert.Equal(t, "My Session", item.Label())
	assert.NotEmpty(t, item.RightLabel())
	sess, ok := item.Value().(session.Session)
	require.True(t, ok, "a session row's value is its session, got %T", item.Value())
	assert.Equal(t, "sess-1", sess.ID)
}
