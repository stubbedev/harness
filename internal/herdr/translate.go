package herdr

import "github.com/stubbedev/harness/internal/agentstate"

// The herdr event vocabulary is the neutral agent-lifecycle vocabulary
// from internal/agentstate; these aliases keep herdr's call sites and
// tests stable while Translate and the pub/sub bridge live in one
// place, shared with the tmux integration.
type (
	Event            = agentstate.Event
	AssistantMessage = agentstate.AssistantMessage
	RunComplete      = agentstate.RunComplete
	Summarizing      = agentstate.Summarizing
)

// Translate converts a pub/sub event into a herdr Event. Returns nil
// for event types herdr doesn't care about. Delegates to the shared
// translation in internal/agentstate.
func Translate(ev any) Event {
	return agentstate.Translate(ev)
}
