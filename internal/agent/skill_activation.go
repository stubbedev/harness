package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/skills"
)

const (
	skillActivationLimit      = 32
	skillActivationByteBudget = 128 * 1024
	skillActivationEventLimit = 256
)

type SkillActivationConfig struct {
	WorkingDir    string
	Skills        func() []*skills.Skill
	SkillPaths    []string
	Tracker       *skills.Tracker
	AllowedSkills []string
	Capabilities  []string
}

type SkillActivation struct {
	config *SkillActivationConfig
	loaded map[string]string
	bytes  int
}

func NewSkillActivation(config *SkillActivationConfig) *SkillActivation {
	return &SkillActivation{config: config, loaded: make(map[string]string)}
}

func (a *SkillActivation) Prepare(ctx context.Context, messages []fantasy.Message) []fantasy.Message {
	if a == nil || a.config == nil || a.config.Skills == nil || ctx.Err() != nil {
		return messages
	}
	available := slices.DeleteFunc(slices.Clone(a.config.Skills()), func(skill *skills.Skill) bool {
		return skill == nil || skill.DisableModelInvocation || (a.config.AllowedSkills != nil && !slices.Contains(a.config.AllowedSkills, skill.Name))
	})
	slices.SortFunc(available, func(left, right *skills.Skill) int {
		return strings.Compare(left.Name, right.Name)
	})
	eligible := make(map[string]bool, len(available))
	for _, skill := range available {
		eligible[skill.Name+"\x00"+skill.SkillFilePath] = true
	}
	events := skillActivationEvents(messages)
	capabilities := append(slices.Clone(a.config.Capabilities), skills.ProjectCapabilities(a.config.WorkingDir)...)
	events = append(events, skills.ActivationEvent{Capabilities: capabilities})
	for _, skill := range available {
		if ctx.Err() != nil {
			return messages
		}
		if skill.Activation == nil {
			continue
		}
		key := skill.Name + "\x00" + skill.SkillFilePath
		if _, ok := a.loaded[key]; ok {
			continue
		}
		if len(a.loaded) >= skillActivationLimit {
			break
		}
		if !slices.ContainsFunc(events, func(event skills.ActivationEvent) bool {
			return skill.Activation.Matches(a.config.WorkingDir, event)
		}) {
			continue
		}
		content, _, err := skills.ReadContent(available, a.config.SkillPaths, a.config.WorkingDir, skill.SkillFilePath)
		if err != nil {
			slog.Warn("Failed to activate skill", "skill", skill.Name, "error", err)
			continue
		}
		body := skills.FormatLoadedSkill(skill.Name, skill.SkillFilePath, string(content))
		if a.bytes+len(body) > skillActivationByteBudget {
			continue
		}
		a.loaded[key] = body
		a.bytes += len(body)
		a.config.Tracker.MarkLoaded(skill.Name)
	}
	keys := make([]string, 0, len(a.loaded))
	for key := range a.loaded {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if !eligible[key] {
			continue
		}
		body := a.loaded[key]
		if !skillActivationPresent(messages, body) {
			messages = append(messages, fantasy.NewUserMessage(body))
		}
	}
	return messages
}

func skillActivationPresent(messages []fantasy.Message, body string) bool {
	for _, message := range messages {
		for _, part := range message.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && strings.Contains(text.Text, body) {
				return true
			}
			if result, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part); ok {
				if text, ok := result.Output.(fantasy.ToolResultOutputContentText); ok && strings.Contains(text.Text, body) {
					return true
				}
			}
		}
	}
	return false
}

func skillActivationEvents(messages []fantasy.Message) []skills.ActivationEvent {
	var events []skills.ActivationEvent
	for i := len(messages) - 1; i >= 0 && len(events) < skillActivationEventLimit; i-- {
		for _, part := range messages[i].Content {
			call, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part)
			if !ok {
				continue
			}
			events = appendSkillActivationEvents(events, call.ToolName, json.RawMessage(call.Input))
		}
	}
	return events
}

func appendSkillActivationEvents(events []skills.ActivationEvent, name string, input json.RawMessage) []skills.ActivationEvent {
	if len(events) >= skillActivationEventLimit {
		return events
	}
	var args struct {
		Action     string `json:"action"`
		FilePath   string `json:"file_path"`
		Path       string `json:"path"`
		WorkingDir string `json:"working_dir"`
		Files      []struct {
			FilePath string `json:"file_path"`
		} `json:"files"`
	}
	if len(input) > 1024*1024 || json.Unmarshal(input, &args) != nil {
		return events
	}
	event := skills.ActivationEvent{Tool: name, Action: args.Action}
	for _, file := range []string{args.FilePath, args.Path, args.WorkingDir} {
		if file != "" {
			event.Paths = append(event.Paths, file)
		}
	}
	for _, file := range args.Files {
		if len(event.Paths) >= skillActivationEventLimit {
			break
		}
		if file.FilePath != "" {
			event.Paths = append(event.Paths, file.FilePath)
		}
	}
	return append(events, event)
}

func (c *coordinator) skillActivationConfig(isSubAgent bool) *SkillActivationConfig {
	if isSubAgent {
		return nil
	}
	config := &SkillActivationConfig{
		WorkingDir: c.cfg.WorkingDir(),
		Skills:     c.activeSkillsList,
		Tracker:    c.skillTracker,
	}
	if c.skillsMgr != nil {
		config.SkillPaths = c.skillsMgr.ResolvedPaths()
	}
	return config
}
