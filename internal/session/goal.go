package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stubbedev/harness/internal/db"
)

// GoalStatus is where a session goal stands.
type GoalStatus string

const (
	// GoalActive is a goal the agent keeps working toward: every turn
	// that ends without it met is followed by another.
	GoalActive GoalStatus = "active"
	// GoalMet is a goal the judge found satisfied.
	GoalMet GoalStatus = "met"
	// GoalImpossible is a goal the judge found cannot be satisfied.
	GoalImpossible GoalStatus = "impossible"
)

// MaxGoalConditionLength bounds a goal's condition, in runes.
const MaxGoalConditionLength = 4000

// Goal is a completion condition the agent works toward across turns.
// It lives on the session row rather than in the conversation, so
// compaction, a restart or a resumed session never loses it.
type Goal struct {
	Condition string     `json:"condition"`
	Status    GoalStatus `json:"status"`
	// Reason is the judge's latest verdict reason: what is still
	// missing while active, why it ended once met or impossible.
	Reason string `json:"reason,omitempty"`
	// Turns counts the turns judged against this goal.
	Turns     int   `json:"turns"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// Active reports whether g is set and still being worked toward.
func (g *Goal) Active() bool {
	return g != nil && g.Status == GoalActive
}

// SetGoal stores goal on the session, or clears it when goal is nil.
// It writes only the goal column, so it never races the other fields a
// fetch-modify-save would carry.
func (s *service) SetGoal(ctx context.Context, sessionID string, goal *Goal) error {
	data, err := marshalGoal(goal)
	if err != nil {
		return err
	}
	if err := s.q.UpdateSessionGoal(ctx, db.UpdateSessionGoalParams{
		ID:   sessionID,
		Goal: nullStr(data),
	}); err != nil {
		return fmt.Errorf("updating session goal: %w", err)
	}
	s.publishSessionUpdate(ctx, sessionID)
	return nil
}

func marshalGoal(goal *Goal) (string, error) {
	if goal == nil {
		return "", nil
	}
	data, err := json.Marshal(goal)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalGoal(data string) (*Goal, error) {
	if data == "" {
		return nil, nil
	}
	var goal Goal
	if err := json.Unmarshal([]byte(data), &goal); err != nil {
		return nil, err
	}
	return &goal, nil
}

// NewGoal returns an active goal for condition, validated: it must not
// be empty or longer than MaxGoalConditionLength.
func NewGoal(condition string, now time.Time) (*Goal, error) {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return nil, errors.New("goal condition is empty")
	}
	if n := utf8.RuneCountInString(condition); n > MaxGoalConditionLength {
		return nil, fmt.Errorf("goal condition is %d characters; the limit is %d", n, MaxGoalConditionLength)
	}
	return &Goal{
		Condition: condition,
		Status:    GoalActive,
		CreatedAt: now.Unix(),
		UpdatedAt: now.Unix(),
	}, nil
}
