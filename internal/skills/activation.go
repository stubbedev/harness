package skills

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stubbedev/harness/internal/filepathext"
)

type ActivationRules struct {
	Paths        []string         `yaml:"paths,omitempty" json:"paths,omitempty"`
	Directories  []string         `yaml:"directories,omitempty" json:"directories,omitempty"`
	Tools        []ActivationTool `yaml:"tools,omitempty" json:"tools,omitempty"`
	Capabilities []string         `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Markers      []string         `yaml:"markers,omitempty" json:"markers,omitempty"`
}

type ActivationTool struct {
	Name    string   `yaml:"name" json:"name"`
	Actions []string `yaml:"actions,omitempty" json:"actions,omitempty"`
}

type ActivationEvent struct {
	Paths        []string
	Tool         string
	Action       string
	Capabilities []string
}

func (r *ActivationRules) Validate() error {
	if r == nil {
		return nil
	}
	if len(r.Paths)+len(r.Directories)+len(r.Tools)+len(r.Capabilities)+len(r.Markers) == 0 {
		return fmt.Errorf("activation must contain at least one rule")
	}
	for _, pattern := range r.Paths {
		if !activationRelativePath(pattern) || !doublestar.ValidatePattern(pattern) {
			return fmt.Errorf("invalid activation path glob %q", pattern)
		}
	}
	for _, value := range append(slices.Clone(r.Directories), r.Markers...) {
		if !activationRelativePath(value) || strings.ContainsAny(value, "*?[{") {
			return fmt.Errorf("invalid activation directory or marker %q", value)
		}
	}
	for _, tool := range r.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			return fmt.Errorf("activation tool name is required")
		}
		for _, action := range tool.Actions {
			if strings.TrimSpace(action) == "" {
				return fmt.Errorf("activation tool action must not be empty")
			}
		}
	}
	for _, capability := range r.Capabilities {
		if strings.TrimSpace(capability) == "" {
			return fmt.Errorf("activation capability must not be empty")
		}
	}
	return nil
}

func activationRelativePath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\\x00:") || path.IsAbs(value) {
		return false
	}
	return !slices.Contains(strings.Split(value, "/"), "..")
}

func (r *ActivationRules) Matches(workingDir string, event ActivationEvent) bool {
	if r == nil || r.Validate() != nil {
		return false
	}
	for _, tool := range r.Tools {
		if tool.Name == event.Tool && (len(tool.Actions) == 0 || slices.Contains(tool.Actions, event.Action)) {
			return true
		}
	}
	for _, capability := range r.Capabilities {
		if slices.Contains(event.Capabilities, capability) {
			return true
		}
	}
	for _, file := range event.Paths {
		rel, ok := activationPath(workingDir, file)
		if !ok {
			continue
		}
		for _, pattern := range r.Paths {
			if matched, _ := doublestar.Match(pattern, rel); matched {
				return true
			}
		}
		for _, dir := range r.Directories {
			dir = path.Clean(dir)
			if dir == "." || rel == dir || strings.HasPrefix(rel, dir+"/") {
				return true
			}
		}
	}
	for _, marker := range r.Markers {
		if activationMarkerExists(workingDir, marker) {
			return true
		}
	}
	return false
}

func activationPath(workingDir, file string) (string, bool) {
	if workingDir == "" || file == "" {
		return "", false
	}
	root, err := filepath.Abs(workingDir)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(root, file)
	}
	rel, ok := filepathext.RelWithin(root, filepath.Clean(file))
	if !ok {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func activationMarkerExists(workingDir, marker string) bool {
	if workingDir == "" || !activationRelativePath(marker) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(workingDir, filepath.FromSlash(marker)))
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		return false
	}
	if _, ok := activationPath(root, resolved); !ok {
		return false
	}
	info, err := os.Stat(resolved)
	return err == nil && info.Mode().IsRegular()
}

func ProjectCapabilities(workingDir string) []string {
	markers := []struct {
		name  string
		files []string
	}{
		{"go", []string{"go.mod", "go.work"}},
		{"node", []string{"package.json"}},
		{"python", []string{"pyproject.toml", "requirements.txt", "setup.py"}},
		{"rust", []string{"Cargo.toml"}},
		{"ruby", []string{"Gemfile"}},
		{"java", []string{"pom.xml", "build.gradle", "build.gradle.kts"}},
		{"dotnet", []string{"global.json"}},
	}
	var result []string
	for _, capability := range markers {
		for _, marker := range capability.files {
			if activationMarkerExists(workingDir, marker) {
				result = append(result, capability.name)
				break
			}
		}
	}
	return result
}
