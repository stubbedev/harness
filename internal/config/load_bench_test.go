package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkLoadFromConfigPaths measures the config load path — YAML parse,
// conversion to JSON, deep merge, and unmarshal — across the two layers a
// typical project has.
func BenchmarkLoadFromConfigPaths(b *testing.B) {
	tmpDir := b.TempDir()

	globalConfig := filepath.Join(tmpDir, "config.yaml")
	localConfig := filepath.Join(tmpDir, "harness.yaml")

	globalContent := []byte(`providers:
  openai:
    api_key: ${OPENAI_API_KEY}
    base_url: https://api.openai.com/v1
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}
    base_url: https://api.anthropic.com
options:
  tui:
    theme: charmtone
`)

	localContent := []byte(`providers:
  openai:
    api_key: sk-override-key
options:
  context_paths:
    - README.md
    - AGENTS.md
`)

	if err := os.WriteFile(globalConfig, globalContent, 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(localConfig, localContent, 0o644); err != nil {
		b.Fatal(err)
	}

	configPaths := []string{globalConfig, localConfig}

	b.ReportAllocs()
	for b.Loop() {
		_, _, err := loadFromConfigPaths(context.Background(), configPaths)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLoadFromBytes isolates the merge and unmarshal half of the load,
// so a regression there can be told apart from one in YAML parsing.
func BenchmarkLoadFromBytes(b *testing.B) {
	content := [][]byte{
		[]byte(`{"options":{"tui":{"theme":"charmtone"}}}`),
		[]byte(`{"options":{"context_paths":["README.md","AGENTS.md"]}}`),
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := loadFromBytes(content); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeConfig measures just the YAML-to-JSON conversion that every
// config file now passes through on startup and on every reload.
func BenchmarkDecodeConfig(b *testing.B) {
	content := []byte(`providers:
  openai:
    api_key: ${OPENAI_API_KEY}
    base_url: https://api.openai.com/v1
mcp:
  github:
    type: http
    url: https://api.githubcopilot.com/mcp/
    headers:
      Authorization: Bearer ${GH_PAT}
options:
  context_paths: [README.md, AGENTS.md]
  tui:
    theme: gruvbox-dark
`)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := decodeConfig(content); err != nil {
			b.Fatal(err)
		}
	}
}
