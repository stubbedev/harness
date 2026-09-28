package agent

import (
	"errors"

	"github.com/stubbedev/harness/internal/session"
)

var (
	ErrRequestCancelled = errors.New("request canceled by user")
	ErrSessionBusy      = session.ErrBusy
	ErrEmptyPrompt      = errors.New("prompt is empty")
	ErrSessionMissing   = errors.New("session id is missing")
)
