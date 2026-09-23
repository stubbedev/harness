package dialog

import (
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/notification"
)

const (
	// NotificationsID is the identifier for the notification style picker dialog.
	NotificationsID              ID = "notifications"
	notificationsDialogMaxHeight    = 12
)

// NotificationStyle represents a notification backend option.
type NotificationStyle struct {
	ID          string
	Title       string
	Description string
}

// AllNotificationStyles lists all available notification styles in order.
var AllNotificationStyles = []NotificationStyle{
	{ID: "auto", Title: "Auto", Description: "Automatically detect the best backend"},
	{ID: "native", Title: "Native", Description: "Use system notifications (macOS/Linux/Windows)"},
	{ID: "osc", Title: "OSC", Description: "Use terminal OSC escape sequences"},
	{ID: "bell", Title: "Bell", Description: "Use terminal bell character"},
	{ID: "disabled", Title: "Disabled", Description: "Turn off notifications"},
}

// NewNotifications creates a new notification style picker dialog.
func NewNotifications(com *common.Common) Dialog {
	current := "auto"
	if cfg := com.Config(); cfg != nil && cfg.Options != nil && cfg.Options.Notifications != "" {
		current = cfg.Options.Notifications
	}

	options := make([]pickerOption, 0, len(AllNotificationStyles))
	for _, style := range AllNotificationStyles {
		// Native OS notifications don't build on every platform
		// (illumos/solaris); hide the option where it can't work.
		if style.ID == "native" && !notification.NativeSupported {
			continue
		}
		options = append(options, pickerOption{value: style.ID, title: style.Title})
	}

	return newSimplePicker(com, NotificationsID, "Notification Style", 0, notificationsDialogMaxHeight,
		options, current, func(style string) Action {
			return ActionSelectNotificationStyle{Style: style}
		})
}
