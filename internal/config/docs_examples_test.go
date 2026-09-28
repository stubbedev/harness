package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// configDocs are the documents that teach config: the built-in skills are
// the agent's own instructions for editing it, so an example there that
// names a key the loader does not know is one the agent will write.
var configDocs = []string{
	"../skills/builtin/harness-config/SKILL.md",
	"../skills/builtin/harness-hooks/SKILL.md",
	"../../docs/config/README.md",
	"../../docs/hooks/README.md",
	"../../docs/memory/README.md",
	"../../docs/skills/README.md",
	"../../README.md",
}

var yamlBlock = regexp.MustCompile("(?s)```ya?ml\n(.*?)```")

// TestDocumentedConfigExamplesDecode decodes every YAML config example in
// the docs the way the loader reads a config file, rejecting unknown
// fields, so a renamed or removed key cannot live on in the docs.
func TestDocumentedConfigExamplesDecode(t *testing.T) {
	t.Parallel()

	topLevel := map[string]bool{}
	configType := reflect.TypeFor[Config]()
	for field := range configType.Fields() {
		if name, _, _ := strings.Cut(field.Tag.Get("json"), ","); name != "" && name != "-" {
			topLevel[name] = true
		}
	}

	for _, doc := range configDocs {
		content, err := os.ReadFile(doc)
		require.NoError(t, err)
		for _, match := range yamlBlock.FindAllSubmatchIndex(content, -1) {
			block := content[match[2]:match[3]]
			line := bytes.Count(content[:match[0]], []byte("\n")) + 1
			where := filepath.Base(filepath.Dir(doc)) + "/" + filepath.Base(doc) + ":" + strconv.Itoa(line)
			if bytes.Contains(block, []byte("\n---")) || bytes.HasPrefix(block, []byte("---")) {
				continue // Frontmatter, not config.
			}
			jsonBytes, err := decodeConfig(block)
			if err != nil {
				continue // Not a YAML document on its own (a fragment or a template).
			}
			var keys map[string]json.RawMessage
			if json.Unmarshal(jsonBytes, &keys) != nil {
				continue
			}
			isConfig := false
			for key := range keys {
				isConfig = isConfig || topLevel[key]
			}
			if !isConfig {
				continue
			}
			dec := json.NewDecoder(bytes.NewReader(jsonBytes))
			dec.DisallowUnknownFields()
			var cfg Config
			require.NoError(t, dec.Decode(&cfg), "%s: config example does not decode", where)
		}
	}
}

// TestHookSkillNamesEveryEvent keeps the agent's hook instructions in step
// with the events this build fires.
func TestHookSkillNamesEveryEvent(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("../skills/builtin/harness-hooks/SKILL.md")
	require.NoError(t, err)
	for _, event := range HookEvents() {
		require.Contains(t, string(content), "`"+event+"`", "the hooks skill does not mention %s", event)
	}
}
