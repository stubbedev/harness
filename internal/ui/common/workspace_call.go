package common

import (
	"context"
	"time"
)

// WorkspaceCallTimeout bounds a workspace call the UI makes, so a server
// that stops answering fails the call instead of leaving what waits on
// it pending forever. It matches the client's dial timeout: a call that
// outlives it is not coming back.
const WorkspaceCallTimeout = 30 * time.Second

// WorkspaceCallContext bounds one workspace call, or a short run of
// them, by WorkspaceCallTimeout.
func WorkspaceCallContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), WorkspaceCallTimeout)
}
