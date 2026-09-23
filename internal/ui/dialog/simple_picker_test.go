package dialog

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// The notification picker opens on the configured style, not the one
// after it.
func TestNotificationsPreselectsConfiguredStyle(t *testing.T) {
	t.Parallel()

	s := styles.CharmtonePantera()
	cfg := &config.Config{Options: &config.Options{Notifications: "bell"}}
	com := &common.Common{Workspace: &stubWorkspace{cfg: cfg}, Styles: &s}

	p, ok := NewNotifications(com).(*simplePicker)
	require.True(t, ok)
	item, ok := p.list.SelectedItem().(PickerItem)
	require.True(t, ok)
	require.Equal(t, "bell", item.Value())
}
