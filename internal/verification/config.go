package verification

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	DefaultTimeout        = 120 * time.Second
	DefaultMaxOutputBytes = 32 * 1024
)

type VerificationConfig struct {
	Rules               []Rule `json:"rules,omitempty" jsonschema:"description=Explicit checks selected by changed workspace paths"`
	RequireOnCompletion bool   `json:"require_on_completion,omitempty" jsonschema:"description=Require current successful applicable checks before completion,default=false"`
	MaxRepairAttempts   int    `json:"max_repair_attempts,omitempty" jsonschema:"description=Maximum completion repair attempts,minimum=0,maximum=10,default=0"`
}

type Rule struct {
	Name           string   `json:"name" jsonschema:"required,description=Unique check name,minLength=1"`
	Paths          []string `json:"paths" jsonschema:"required,description=Workspace relative doublestar globs selecting this check,minItems=1"`
	Inputs         []string `json:"inputs,omitempty" jsonschema:"description=Additional input globs included in the revision fingerprint"`
	Command        []string `json:"command" jsonschema:"required,description=Literal executable and arguments without shell expansion,minItems=1"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty" jsonschema:"description=Command timeout in seconds,minimum=0,maximum=3600,default=120"`
	MaxOutputBytes int      `json:"max_output_bytes,omitempty" jsonschema:"description=Combined stdout and stderr byte limit,minimum=0,maximum=1048576,default=32768"`
}

func (c VerificationConfig) Validate() error {
	if c.MaxRepairAttempts < 0 || c.MaxRepairAttempts > 10 {
		return fmt.Errorf("max_repair_attempts must be between 0 and 10")
	}
	names := map[string]bool{}
	for _, r := range c.Rules {
		if strings.TrimSpace(r.Name) == "" || names[r.Name] {
			return fmt.Errorf("verification rule name must be nonempty and unique: %q", r.Name)
		}
		names[r.Name] = true
		if len(r.Paths) == 0 {
			return fmt.Errorf("rule %q requires paths", r.Name)
		}
		for _, glob := range append(append([]string{}, r.Paths...), r.Inputs...) {
			if glob == "" || strings.Contains(glob, "\\") || strings.HasPrefix(glob, "/") || strings.Contains(glob, ":") || path.Clean(glob) != glob || glob == ".." || strings.HasPrefix(glob, "../") || !doublestar.ValidatePattern(glob) {
				return fmt.Errorf("rule %q has invalid workspace glob %q", r.Name, glob)
			}
		}
		if len(r.Command) == 0 || strings.TrimSpace(r.Command[0]) == "" {
			return fmt.Errorf("rule %q requires command argv", r.Name)
		}
		for _, arg := range r.Command {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("rule %q command contains NUL", r.Name)
			}
		}
		if r.TimeoutSeconds < 0 || r.TimeoutSeconds > 3600 {
			return fmt.Errorf("rule %q timeout_seconds must be between 0 and 3600", r.Name)
		}
		if r.MaxOutputBytes < 0 || r.MaxOutputBytes > 1024*1024 {
			return fmt.Errorf("rule %q max_output_bytes must be between 0 and 1048576", r.Name)
		}
	}
	return nil
}

func (r Rule) timeout() time.Duration {
	if r.TimeoutSeconds == 0 {
		return DefaultTimeout
	}
	return time.Duration(r.TimeoutSeconds) * time.Second
}

func (r Rule) outputLimit() int {
	if r.MaxOutputBytes == 0 {
		return DefaultMaxOutputBytes
	}
	return r.MaxOutputBytes
}
