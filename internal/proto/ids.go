package proto

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	ErrInvalidSessionID = errors.New("invalid session_id")
	ErrInvalidClientID  = errors.New("invalid client_id")
	ErrInvalidRunID     = errors.New("invalid run_id")
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type SessionID string

type ClientID string

func ParseSessionID(s string) (SessionID, error) {
	if !uuidPattern.MatchString(s) {
		return "", fmt.Errorf("%w: %q", ErrInvalidSessionID, s)
	}
	return SessionID(s), nil
}

func ParseClientID(s string) (ClientID, error) {
	if !uuidPattern.MatchString(s) {
		return "", fmt.Errorf("%w: %q", ErrInvalidClientID, s)
	}
	return ClientID(s), nil
}

func ValidateRunID(s string) error {
	if s != "" && !uuidPattern.MatchString(s) {
		return fmt.Errorf("%w: %q", ErrInvalidRunID, s)
	}
	return nil
}
