package prompt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeContextFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestBuildRendersContextFilesInConfiguredOrder(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	dir := store.WorkingDir()

	writeContextFile(t, dir, "a.md", "content-a")
	writeContextFile(t, dir, "b.md", "content-b")
	writeContextFile(t, dir, "c.md", "content-c")
	writeContextFile(t, dir, filepath.Join("notes", "2.md"), "note-2")
	writeContextFile(t, dir, filepath.Join("notes", "1.md"), "note-1")
	writeContextFile(t, dir, "global.md", "global-1")

	store.Config().Options.ContextPaths = []string{"c.md", "b.md", "a.md", "b.md", "notes"}
	store.Config().Options.GlobalContextPaths = []string{"global.md"}

	p, err := NewPrompt("t",
		`{{range .ContextFiles}}[{{.Path}}={{.Content}}]{{end}}|{{range .GlobalContextFiles}}[{{.Path}}={{.Content}}]{{end}}`,
		WithAvailableSkillsXML(""),
	)
	require.NoError(t, err)

	got, err := p.Build(context.Background(), "provider", "model", store)
	require.NoError(t, err)

	expected := fmt.Sprintf("[%s=content-c][%s=content-b][%s=content-a][%s=note-1][%s=note-2]|[%s=global-1]",
		filepath.Join(dir, "c.md"),
		filepath.Join(dir, "b.md"),
		filepath.Join(dir, "a.md"),
		filepath.Join(dir, "notes", "1.md"),
		filepath.Join(dir, "notes", "2.md"),
		filepath.Join(dir, "global.md"),
	)
	require.Equal(t, expected, got)
}

func TestLoadContextFilesDeduplicatesRepeatedPaths(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	writeContextFile(t, store.WorkingDir(), "a.md", "content-a")

	files := loadContextFiles([]string{"a.md", "a.md", "a.md"}, store)
	require.Len(t, files, 1)
	require.Equal(t, "content-a", files[0].Content)
}

func TestBuildContextFilesDeterministicAcrossBuilds(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	dir := store.WorkingDir()

	writeContextFile(t, dir, "z.md", "content-z")
	writeContextFile(t, dir, "m.md", "content-m")
	writeContextFile(t, dir, "a.md", "content-a")
	store.Config().Options.ContextPaths = []string{"z.md", "m.md", "a.md"}
	store.Config().Options.GlobalContextPaths = []string{"z.md"}

	p, err := NewPrompt("t",
		`{{range .ContextFiles}}{{.Path}}={{.Content}};{{end}}{{range .GlobalContextFiles}}{{.Path}}={{.Content}};{{end}}`,
		WithAvailableSkillsXML(""),
	)
	require.NoError(t, err)

	first, err := p.Build(context.Background(), "provider", "model", store)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%s=content-z;%s=content-m;%s=content-a;%s=content-z;",
		filepath.Join(dir, "z.md"),
		filepath.Join(dir, "m.md"),
		filepath.Join(dir, "a.md"),
		filepath.Join(dir, "z.md"),
	), first)

	for range 10 {
		next, err := p.Build(context.Background(), "provider", "model", store)
		require.NoError(t, err)
		require.Equal(t, first, next)
	}

	rebuilt, err := NewPrompt("t",
		`{{range .ContextFiles}}{{.Path}}={{.Content}};{{end}}{{range .GlobalContextFiles}}{{.Path}}={{.Content}};{{end}}`,
		WithAvailableSkillsXML(""),
	)
	require.NoError(t, err)
	next, err := rebuilt.Build(context.Background(), "provider", "model", store)
	require.NoError(t, err)
	require.Equal(t, first, next, "a fresh Prompt renders the same bytes")
}

func TestBuildConcurrentOnOnePrompt(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	dir := store.WorkingDir()

	writeContextFile(t, dir, "a.md", "content-a")
	writeContextFile(t, dir, "b.md", "content-b")
	store.Config().Options.ContextPaths = []string{"b.md", "a.md"}

	p, err := NewPrompt("t",
		`{{range .ContextFiles}}{{.Path}}={{.Content}};{{end}}`,
		WithAvailableSkillsXML(""),
	)
	require.NoError(t, err)

	const n = 8
	results := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			results[i], errs[i] = p.Build(context.Background(), "provider", "model", store)
		})
	}
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i])
		require.Equal(t, results[0], results[i])
	}
}

func TestBuildContextFilesAppliesCapsInOrder(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	dir := store.WorkingDir()

	small := strings.Repeat("x\n", 15*1024)
	writeContextFile(t, dir, "f1.md", small)
	writeContextFile(t, dir, "f2.md", small)
	writeContextFile(t, dir, "f3.md", strings.Repeat("x\n", 20*1024))
	writeContextFile(t, dir, "f4.md", small)
	writeContextFile(t, dir, "f5.md", small)
	store.Config().Options.ContextPaths = []string{"f1.md", "f2.md", "f3.md", "f4.md", "f5.md"}

	p, err := NewPrompt("t",
		`{{range .ContextFiles}}<{{.Path}}>{{.Content}}</{{.Path}}>{{end}}`,
		WithAvailableSkillsXML(""),
	)
	require.NoError(t, err)

	got, err := p.Build(context.Background(), "provider", "model", store)
	require.NoError(t, err)

	require.Equal(t, 3, strings.Count(got, "truncated"), "the files with room render whole, the rest are cut")
	for _, name := range []string{"f1.md", "f2.md"} {
		whole := fmt.Sprintf("<%s>%s</%s>", filepath.Join(dir, name), small, filepath.Join(dir, name))
		require.Contains(t, got, whole, name+" fits under both limits and renders whole")
	}
	for _, name := range []string{"f3.md", "f4.md", "f5.md"} {
		require.Contains(t, got, fmt.Sprintf("[context file %s truncated:", filepath.Join(dir, name)))
	}

	marker := func(name string) int {
		return strings.Index(got, fmt.Sprintf("<%s>", filepath.Join(dir, name)))
	}
	require.Less(t, marker("f1.md"), marker("f2.md"))
	require.Less(t, marker("f2.md"), marker("f3.md"))
	require.Less(t, marker("f3.md"), marker("f4.md"))
	require.Less(t, marker("f4.md"), marker("f5.md"))

	f5 := got[marker("f5.md"):]
	require.True(t, strings.HasPrefix(f5, fmt.Sprintf("<%s>\n[context file", filepath.Join(dir, "f5.md"))), "a file with no room left renders only its marker")
	require.NotContains(t, f5, "x\n", "none of the dropped content survives")

	again, err := p.Build(context.Background(), "provider", "model", store)
	require.NoError(t, err)
	require.Equal(t, got, again)
}

func TestParsedTemplateIsCached(t *testing.T) {
	t.Parallel()

	p, err := NewPrompt("t", `{{.Platform}}`)
	require.NoError(t, err)

	first, err := p.parsedTemplate()
	require.NoError(t, err)
	second, err := p.parsedTemplate()
	require.NoError(t, err)
	require.Same(t, first, second)

	bad, err := NewPrompt("t", "{{.Unclosed")
	require.NoError(t, err)

	_, firstErr := bad.parsedTemplate()
	require.Error(t, firstErr)
	_, secondErr := bad.parsedTemplate()
	require.ErrorIs(t, secondErr, firstErr)
}
