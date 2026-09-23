// Package skills implements the Agent Skills open standard.
// See https://agentskills.io for the specification.
package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/stubbedev/harness/internal/discovery"
	"github.com/stubbedev/harness/internal/stringext"

	"github.com/stubbedev/harness/internal/pubsub"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	SkillFileName          = "SKILL.md"
	MaxNameLength          = 64
	MaxDescriptionLength   = 1024
	MaxCompatibilityLength = 500
)

var (
	namePattern = regexp.MustCompile(`^[\p{L}\p{N}]+(-[\p{L}\p{N}]+)*$`)

	latestStates   []*SkillState
	latestStatesMu sync.RWMutex
)

// Skill represents a parsed SKILL.md file.
type Skill struct {
	Name        string           `yaml:"name" json:"name"`
	Description string           `yaml:"description" json:"description"`
	Activation  *ActivationRules `yaml:"activation,omitempty" json:"activation,omitempty"`
	// UserInvocable mirrors the Claude Code field of the same name. The
	// default is user-invocable: a nil pointer (field absent from the
	// frontmatter) means the skill belongs in the user's / palette, and
	// only an explicit `user-invocable: false` opts out — background
	// knowledge the user should not invoke directly. The Agent Skills
	// spec itself defines no invocation-control field, so spec-only
	// skills land on the default.
	UserInvocable          *bool          `yaml:"user-invocable" json:"user_invocable,omitempty"`
	DisableModelInvocation bool           `yaml:"disable-model-invocation" json:"disable_model_invocation"`
	License                string         `yaml:"license,omitempty" json:"license,omitempty"`
	Compatibility          string         `yaml:"compatibility,omitempty" json:"compatibility,omitempty"`
	Metadata               map[string]any `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Instructions           string         `yaml:"-" json:"instructions"`
	Path                   string         `yaml:"-" json:"path"`
	SkillFilePath          string         `yaml:"-" json:"skill_file_path"`
	Builtin                bool           `yaml:"-" json:"builtin"`
}

// DiscoveryState represents the outcome of discovering a single skill file.
type DiscoveryState = discovery.Outcome

const (
	// StateNormal indicates the skill was parsed and validated successfully.
	StateNormal = discovery.StateNormal
	// StateError indicates discovery encountered a scan/parse/validate error.
	StateError = discovery.StateError
)

// SkillState represents the latest discovery status of a skill file.
type SkillState = discovery.State

// Event is published when skill discovery completes.
type Event struct {
	States []*SkillState
}

var broker = pubsub.NewBroker[Event]()

// SubscribeEvents returns a channel that receives events when skill discovery state changes.
func SubscribeEvents(ctx context.Context) <-chan pubsub.Event[Event] {
	return broker.Subscribe(ctx)
}

// PublishStates publishes a skill discovery event with the given states.
func PublishStates(states []*SkillState) {
	broker.Publish(pubsub.UpdatedEvent, Event{States: discovery.CloneStates(states)})
}

// GetLatestStates returns the latest discovery states.
func GetLatestStates() []*SkillState {
	latestStatesMu.RLock()
	defer latestStatesMu.RUnlock()
	return discovery.CloneStates(latestStates)
}

// SetLatestStates stores the given states in the package-level cache so that
// GetLatestStates can return them synchronously before the first pubsub event
// arrives.
func SetLatestStates(states []*SkillState) {
	latestStatesMu.Lock()
	latestStates = discovery.CloneStates(states)
	latestStatesMu.Unlock()
}

// IsUserInvocable reports whether the skill belongs in the user's
// palette: the default (nil UserInvocable) is yes, matching Claude
// Code's documented default; only `user-invocable: false` opts out.
func (s *Skill) IsUserInvocable() bool {
	return s.UserInvocable == nil || *s.UserInvocable
}

// Validate checks if the skill meets spec requirements.
func (s *Skill) Validate() error {
	var errs []error

	if s.Name == "" {
		errs = append(errs, errors.New("name is required"))
	} else {
		if len(s.Name) > MaxNameLength {
			errs = append(errs, fmt.Errorf("name exceeds %d characters", MaxNameLength))
		}
		// Compare in NFC form so that names written in one Unicode
		// normalization match directories stored in another (e.g. macOS
		// reports NFD filenames).
		name := norm.NFC.String(s.Name)
		if !namePattern.MatchString(name) {
			errs = append(errs, errors.New("name must be alphanumeric with hyphens, no leading/trailing/consecutive hyphens"))
		}
		if s.Path != "" && !strings.EqualFold(norm.NFC.String(filepath.Base(s.Path)), name) {
			errs = append(errs, fmt.Errorf("name %q must match directory %q", s.Name, filepath.Base(s.Path)))
		}
	}

	if s.Description == "" {
		errs = append(errs, errors.New("description is required"))
	} else if len(s.Description) > MaxDescriptionLength {
		errs = append(errs, fmt.Errorf("description exceeds %d characters", MaxDescriptionLength))
	}

	if len(s.Compatibility) > MaxCompatibilityLength {
		errs = append(errs, fmt.Errorf("compatibility exceeds %d characters", MaxCompatibilityLength))
	}

	if err := s.Activation.Validate(); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// Parse parses a SKILL.md file from disk.
func Parse(path string) (*Skill, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	skill, err := ParseContent(content)
	if err != nil {
		return nil, err
	}

	skill.Path = filepath.Dir(path)
	skill.SkillFilePath = path

	return skill, nil
}

// ParseContent parses a SKILL.md from raw bytes.
func ParseContent(content []byte) (*Skill, error) {
	frontmatter, body, err := stringext.SplitFrontmatter(string(content))
	if err != nil {
		return nil, err
	}

	var skill Skill
	if err := yaml.Unmarshal([]byte(frontmatter), &skill); err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}

	skill.Instructions = strings.TrimSpace(body)

	return &skill, nil
}

// Discover finds all valid skills in the given paths.
func Discover(paths []string) []*Skill {
	skills, _ := DiscoverWithStates(paths)
	return skills
}

// DiscoverWithStates finds all valid skills in the given paths and also
// returns a per-file state slice describing parse/validation outcomes. Useful
// for diagnostics and UI reporting. Results keep the caller's path order,
// so with Deduplicate a later path overrides an earlier one.
func DiscoverWithStates(paths []string) ([]*Skill, []*SkillState) {
	return discovery.Walk("skill", paths,
		func(name string) bool { return name == SkillFileName },
		func(path string) (*Skill, string, error) {
			skill, err := Parse(path)
			if err != nil {
				return nil, "", err
			}
			return skill, skill.Name, skill.Validate()
		},
	)
}

// ToPromptXML generates XML for injection into the system prompt.
// Skills with DisableModelInvocation set to true are excluded.
func ToPromptXML(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<available_skills>\n")
	for _, s := range skills {
		// Skip skills that have disable-model-invocation set
		if s.DisableModelInvocation {
			continue
		}
		sb.WriteString("  <skill>\n")
		fmt.Fprintf(&sb, "    <name>%s</name>\n", stringext.EscapeXML(s.Name))
		fmt.Fprintf(&sb, "    <description>%s</description>\n", stringext.EscapeXML(s.Description))
		fmt.Fprintf(&sb, "    <location>%s</location>\n", stringext.EscapeXML(s.SkillFilePath))
		if s.Builtin {
			sb.WriteString("    <type>builtin</type>\n")
		}
		sb.WriteString("  </skill>\n")
	}
	sb.WriteString("</available_skills>")
	return sb.String()
}

// FormatInvocation generates XML for a skill when invoked as a user command.
func (s *Skill) FormatInvocation() string {
	var sb strings.Builder
	sb.WriteString("<loaded_skill>\n")
	fmt.Fprintf(&sb, "  <name>%s</name>\n", stringext.EscapeXML(s.Name))
	fmt.Fprintf(&sb, "  <description>%s</description>\n", stringext.EscapeXML(s.Description))
	fmt.Fprintf(&sb, "  <location>%s</location>\n", stringext.EscapeXML(s.SkillFilePath))
	sb.WriteString("  <instructions>\n")
	sb.WriteString(stringext.EscapeXML(s.Instructions))
	sb.WriteString("\n  </instructions>\n")
	sb.WriteString("</loaded_skill>")
	return sb.String()
}

// LoadedSkillPrecedence rides with every skill body the harness injects,
// keeping an explicit user command above the skill's instructions even in
// agent contexts that never see the coder system prompt.
const LoadedSkillPrecedence = "An explicit user command overrides this skill's instructions."

// FormatLoadedSkill renders a loaded skill's SKILL.md the way the harness
// injects it into the conversation. Auto-activation and skill_search loads
// both go through here, so every injected skill carries the precedence line
// by construction.
func FormatLoadedSkill(name, path, content string) string {
	return fmt.Sprintf(
		"<loaded_skill name=%q path=%q>\n%s\n%s\n</loaded_skill>",
		name, path, LoadedSkillPrecedence, strings.TrimRight(content, "\n"),
	)
}

// DeduplicateStates removes duplicate skill states by name, keeping the
// last so it agrees with Deduplicate. Error states are kept per file.
func DeduplicateStates(all []*SkillState) []*SkillState {
	return discovery.DedupeStates(all)
}

// Deduplicate removes duplicate skills by name. When duplicates exist, the
// last occurrence wins. This means user skills (appended after builtins)
// override builtin skills with the same name.
func Deduplicate(all []*Skill) []*Skill {
	return discovery.Dedupe(all, skillName)
}

func skillName(s *Skill) string { return s.Name }

// ApproxTokenCount returns a rough estimate of how many tokens a string
// occupies when sent to an LLM. Uses the common ~4-chars-per-token heuristic
// that approximates GPT/Claude tokenizers well enough for diagnostic logging.
func ApproxTokenCount(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// Filter removes skills whose names appear in the disabled list.
func Filter(all []*Skill, disabled []string) []*Skill {
	return discovery.Filter(all, disabled, skillName)
}
