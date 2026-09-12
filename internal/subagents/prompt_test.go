package subagents

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToPromptXML(t *testing.T) {
	t.Parallel()

	active := []*Subagent{
		{Name: "go-test-writer", Description: "Writes Go tests before implementation."},
		{Name: "code-reviewer", Description: "Reviews diffs & flags issues."},
	}

	xml := ToPromptXML(active)

	require.Contains(t, xml, "<available_subagents>")
	require.Contains(t, xml, "<name>go-test-writer</name>")
	require.Contains(t, xml, "<description>Writes Go tests before implementation.</description>")
	require.Contains(t, xml, "&amp;") // XML escaping
	require.Contains(t, xml, "</available_subagents>")
}

func TestToPromptXMLEmpty(t *testing.T) {
	t.Parallel()
	require.Empty(t, ToPromptXML(nil))
	require.Empty(t, ToPromptXML([]*Subagent{}))
}

func TestToPromptXML_EscapesSpecialChars(t *testing.T) {
	t.Parallel()

	active := []*Subagent{
		{Name: "html-writer", Description: `Handles <script> tags & "quotes" 'n stuff.`},
	}

	xml := ToPromptXML(active)

	require.NotContains(t, xml, "<script>")
	require.Contains(t, xml, "&lt;script&gt;")
	require.Contains(t, xml, "&amp;")
	require.Contains(t, xml, "&quot;quotes&quot;")
	require.Contains(t, xml, "&apos;n")
}

func TestToPromptXML_ModelAndEffort(t *testing.T) {
	t.Parallel()

	active := []*Subagent{
		{Name: "scout", Description: "Narrow lookups.", Model: ModelAliasSmall, Effort: "low"},
		{Name: "architect", Description: "Designs changes."},
		{Name: "pinned", Description: "Runs a specific model.", Model: "claude-opus-5"},
	}

	xml := ToPromptXML(active)

	// The small-model entry advertises its model, its effort, and the cost
	// hint that tells the coordinator fanning it out is worthwhile.
	require.Contains(t, xml, "<model>small</model>")
	require.Contains(t, xml, "<effort>low</effort>")
	require.Contains(t, xml, "<cost>")

	// An absent model: reports the large default buildAgent actually uses,
	// rather than being omitted.
	require.Contains(t, xml, "<model>large</model>")

	// A pinned model id is reported verbatim but never assumed cheap.
	require.Contains(t, xml, "<model>claude-opus-5</model>")
	require.Equal(t, 1, strings.Count(xml, "<cost>"))

	// Effort is omitted entirely when unset, rather than emitted empty.
	require.NotContains(t, xml, "<effort></effort>")
}

func TestModelLabelAndIsCheap(t *testing.T) {
	t.Parallel()

	require.Equal(t, ModelAliasLarge, Subagent{}.ModelLabel())
	require.Equal(t, ModelAliasLarge, Subagent{Model: ModelAliasLarge}.ModelLabel())
	require.Equal(t, ModelAliasSmall, Subagent{Model: ModelAliasSmall}.ModelLabel())
	require.Equal(t, "gpt-5", Subagent{Model: "gpt-5"}.ModelLabel())

	require.True(t, Subagent{Model: ModelAliasSmall}.IsCheap())
	require.False(t, Subagent{}.IsCheap())
	require.False(t, Subagent{Model: ModelAliasLarge}.IsCheap())
	require.False(t, Subagent{Model: "gpt-5"}.IsCheap())
}
