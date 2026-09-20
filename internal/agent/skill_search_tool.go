package agent

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/skills"
)

// skillSearchTool stands in for the <available_skills> discovery list the
// coder prompt used to carry. That list spent a description and a location
// on every skill installed, in every request, whether or not the session
// ever touched one; this tool carries the names -- which are what make a
// skill discoverable at all -- and hands over a description on search and
// the whole SKILL.md on load.
type skillSearchTool struct {
	coord *coordinator
	opts  fantasy.ProviderOptions
}

// SkillSearchToolName is the tool name the model calls.
const SkillSearchToolName = "skill_search"

// skillSearchResultLimit caps how many matches one search returns, and
// skillSearchNameBudget how many characters of the name list the tool's own
// description carries. Same reasoning as the MCP tool search: the names are
// the discovery surface and are cheap, the bodies are not.
const (
	skillSearchResultLimit = 20
	skillSearchNameBudget  = 2000
)

func (s *skillSearchTool) Info() fantasy.ToolInfo {
	names := s.skillNames()
	return fantasy.ToolInfo{
		Name: SkillSearchToolName,
		Description: fmt.Sprintf(
			`Written-down procedures for specific kinds of task. %d available, named below; their triggers and instructions load on demand.

Skills: %s

`+"`query`"+` lists matching skills with the trigger saying when each applies (keywords fuzzy-matched against names and triggers, all must match). `+"`load`"+` returns the whole SKILL.md — no follow-up %s call. Both fields may be combined; a name above can be loaded without searching first.

Skills with explicit activation rules load automatically when their configured project or tool conditions match. Use search for relevant procedures not already loaded; a trigger describes when a skill applies, not its instructions. Load a selected skill before following it. A skill's scripts and assets live beside the SKILL.md whose path the load reports.`,
			len(names), nameList(names, skillSearchNameBudget), tools.ViewToolName,
		),
		Parameters: map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Keywords, space-separated; all must fuzzy-match a name or trigger.",
			},
			"load": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Exact skill names to read in full.",
			},
		},
		// Both fields are optional, but the empty slice has to be
		// explicit: a nil Required marshals to JSON null, which the
		// OpenAI Responses API rejects as not an array.
		Required: []string{},
	}
}

func (s *skillSearchTool) ProviderOptions() fantasy.ProviderOptions        { return s.opts }
func (s *skillSearchTool) SetProviderOptions(opts fantasy.ProviderOptions) { s.opts = opts }

func (s *skillSearchTool) Run(_ context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	params, resp, ok := decodeSearchLoadParams(call, "read skills")
	if !ok {
		return resp, nil
	}

	available := s.availableSkills()
	var b strings.Builder

	if params.Query != "" {
		candidates := make([]searchCandidate, len(available))
		for i, sk := range available {
			candidates[i] = searchCandidate{name: sk.Name, desc: sk.Description}
		}
		matches, matched := rankCandidates(params.Query, candidates, skillSearchResultLimit)
		switch {
		case matched == 0:
			fmt.Fprintf(&b, "No skills matching %q.\n", params.Query)
		case matched > len(matches):
			fmt.Fprintf(&b, "%d skill(s) match %q (of %d available); showing the best %d. Narrow the query to see the others:\n",
				matched, params.Query, len(available), len(matches))
			s.writeMatches(&b, available, matches)
		default:
			fmt.Fprintf(&b, "%d skill(s) matching %q (of %d available):\n", matched, params.Query, len(available))
			s.writeMatches(&b, available, matches)
		}
	}

	if len(params.Load) > 0 {
		unknown := unknownNamesFunc(params.Load, func(name string) bool { return findSkill(available, name) != nil })
		if len(unknown) > 0 {
			return fantasy.NewTextErrorResponse(
				fmt.Sprintf("unknown skill(s): %s", joinNames(unknown))), nil
		}
		for _, name := range params.Load {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			if err := s.writeSkill(&b, available, name); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
		}
	}

	return fantasy.NewTextResponse(strings.TrimRight(b.String(), "\n")), nil
}

// writeMatches lists one "- name: trigger" line per skill. Triggers are
// capped at MaxDescriptionLength by validation, so they are printed whole:
// a truncated trigger is a trigger the model can misjudge.
func (s *skillSearchTool) writeMatches(b *strings.Builder, available []*skills.Skill, names []string) {
	for _, name := range names {
		if sk := findSkill(available, name); sk != nil {
			fmt.Fprintf(b, "- %s: %s\n", sk.Name, sk.Description)
		}
	}
}

// writeSkill renders one skill's full SKILL.md the way an invoked skill is
// rendered, and records it as loaded so the session's skill diagnostics see
// a searched-and-loaded skill the same as a viewed one.
func (s *skillSearchTool) writeSkill(b *strings.Builder, available []*skills.Skill, name string) error {
	sk := findSkill(available, name)
	if sk == nil {
		return fmt.Errorf("unknown skill: %s", name)
	}
	// ReadContent addresses a skill by its file path, not its name: that is
	// the identifier that stays unique once a user skill shadows a builtin
	// of the same name.
	content, _, err := skills.ReadContent(available, s.skillPaths(), s.coord.cfg.WorkingDir(), sk.SkillFilePath)
	if err != nil {
		return fmt.Errorf("read skill %q: %w", name, err)
	}
	b.WriteString(skills.FormatLoadedSkill(sk.Name, sk.SkillFilePath, string(content)))
	b.WriteByte('\n')
	s.coord.skillTracker.MarkLoaded(sk.Name)
	return nil
}

// availableSkills returns the active skills the model may invoke. Skills
// marked disable-model-invocation are the user's alone, so they are left out
// here exactly as ToPromptXML leaves them out of the discovery list.
func (s *skillSearchTool) availableSkills() []*skills.Skill {
	all := s.coord.activeSkillsList()
	available := make([]*skills.Skill, 0, len(all))
	for _, sk := range all {
		if sk.DisableModelInvocation {
			continue
		}
		available = append(available, sk)
	}
	return available
}

func (s *skillSearchTool) skillNames() []string {
	available := s.availableSkills()
	names := make([]string, len(available))
	for i, sk := range available {
		names[i] = sk.Name
	}
	return names
}

// skillPaths returns the resolved skills directories, used only to label a
// loaded skill's source. Nil when no manager is wired, which costs nothing
// but the label.
func (s *skillSearchTool) skillPaths() []string {
	if s.coord.skillsMgr == nil {
		return nil
	}
	return s.coord.skillsMgr.ResolvedPaths()
}

func findSkill(available []*skills.Skill, name string) *skills.Skill {
	for _, sk := range available {
		if sk.Name == name {
			return sk
		}
	}
	return nil
}
