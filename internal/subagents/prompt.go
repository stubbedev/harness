package subagents

import (
	"fmt"
	"strings"

	"github.com/stubbedev/harness/internal/stringext"
)

// ToPromptXML generates XML for injection into the coder system prompt so the
// model can proactively notice a matching subagent before deciding how to
// handle a task. Mirrors skills.ToPromptXML.
//
// Each entry carries its model and effort alongside the description because
// the two drive different delegation decisions: the description answers
// "does this subagent fit the task", the model answers "what does a
// dispatch of it spend". Emitting only the description left the
// coordinator unable to reason about cost.
func ToPromptXML(active []*Subagent) string {
	if len(active) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<available_subagents>\n")
	for _, sa := range active {
		sb.WriteString("  <subagent>\n")
		fmt.Fprintf(&sb, "    <name>%s</name>\n", stringext.EscapeXML(sa.Name))
		fmt.Fprintf(&sb, "    <description>%s</description>\n", stringext.EscapeXML(sa.Description))
		fmt.Fprintf(&sb, "    <model>%s</model>\n", stringext.EscapeXML(sa.ModelLabel()))
		if sa.Effort != "" {
			fmt.Fprintf(&sb, "    <effort>%s</effort>\n", stringext.EscapeXML(sa.Effort))
		}
		if sa.IsCheap() {
			sb.WriteString("    <cost>cheap: runs on the small model</cost>\n")
		}
		sb.WriteString("  </subagent>\n")
	}
	sb.WriteString("</available_subagents>")
	return sb.String()
}
