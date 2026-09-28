package cmd

import (
	"regexp"
	"strings"
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

// TestHookEventNamesPatternAcceptsWhatConfigAccepts keeps the schema's
// hook-key pattern in step with config.CanonicalHookEvent for the forms
// the docs name: the canonical spelling, all lower case and snake case.
func TestHookEventNamesPatternAcceptsWhatConfigAccepts(t *testing.T) {
	t.Parallel()

	schema := new(jsonschema.Reflector).Reflect(&config.Config{})
	setHookEventNames(schema)
	hooks, ok := schema.Definitions["Config"].Properties.Get("hooks")
	require.True(t, ok)
	pattern := regexp.MustCompile(hooks.PropertyNames.Pattern)

	snake := regexp.MustCompile(`([a-z])([A-Z])`)
	for _, event := range config.HookEvents() {
		for _, form := range []string{event, strings.ToLower(event), strings.ToLower(snake.ReplaceAllString(event, "${1}_${2}"))} {
			_, known := config.CanonicalHookEvent(form)
			require.True(t, known, form)
			require.True(t, pattern.MatchString(form), "schema rejects %q", form)
		}
	}
	require.False(t, pattern.MatchString("PreToolUsed"))
	require.False(t, pattern.MatchString("OnSave"))
}
