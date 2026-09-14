package agent

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/prompt"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"

	_ "github.com/joho/godotenv/autoload"
)

// fakeEnv is an environment for testing.
type fakeEnv struct {
	workingDir  string
	sessions    session.Service
	messages    message.Service
	history     history.Service
	filetracker *filetracker.Service
	lspClients  *csync.Map[string, *lsp.Client]
}

func testEnv(t *testing.T) fakeEnv {
	workingDir := t.TempDir()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)

	history := history.NewService(q, conn)
	filetrackerService := filetracker.NewService(q)
	lspClients := csync.NewMap[string, *lsp.Client]()

	t.Cleanup(func() {
		conn.Close()
	})

	return fakeEnv{
		workingDir,
		sessions,
		messages,
		history,
		&filetrackerService,
		lspClients,
	}
}

func testSessionAgent(env fakeEnv, large, small fantasy.LanguageModel, systemPrompt string, tools ...fantasy.AgentTool) SessionAgent {
	largeModel := Model{
		Model: large,
		CatalogCfg: catalog.Model{
			ContextWindow:    200000,
			DefaultMaxTokens: 10000,
		},
	}
	smallModel := Model{
		Model: small,
		CatalogCfg: catalog.Model{
			ContextWindow:    200000,
			DefaultMaxTokens: 10000,
		},
	}
	agent := NewSessionAgent(SessionAgentOptions{
		LargeModel:   largeModel,
		SmallModel:   smallModel,
		SystemPrompt: systemPrompt,
		Sessions:     env.sessions,
		Messages:     env.messages,
		Tools:        tools,
	})
	return agent
}

// coderAgent builds the coder agent the TestCoderAgent subtests drive. A
// nil client is fine for everything but the network-facing tools.
func coderAgent(client *http.Client, env fakeEnv, large, small fantasy.LanguageModel) (SessionAgent, error) {
	fixedTime := func() time.Time {
		t, _ := time.Parse("1/2/2006", "1/1/2025")
		return t
	}
	prompt, err := coderPrompt(
		prompt.WithTimeFunc(fixedTime),
		prompt.WithPlatform("linux"),
		prompt.WithWorkingDir(filepath.ToSlash(env.workingDir)),
	)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Init(env.workingDir, "", false)
	if err != nil {
		return nil, err
	}

	// Pin the config so a developer.s own
	// `$HOME/.config/harness/harness.yaml` cannot change what these tests
	// assert.
	cfg.Config().Options.Attribution = &config.Attribution{
		TrailerStyle:  "co-authored-by",
		GeneratedWith: true,
	}

	// Clear the fields that would otherwise pull in machine-specific state
	// (skills on disk, context files, language servers).
	cfg.Config().Options.SkillsPaths = nil
	cfg.Config().Options.DisabledSkills = []string{"harness-config"}
	cfg.Config().Options.ContextPaths = nil
	cfg.Config().Options.GlobalContextPaths = nil
	cfg.Config().LSP = nil

	systemPrompt, err := prompt.Build(context.TODO(), large.Provider(), large.Model(), cfg)
	if err != nil {
		return nil, err
	}

	// Get the model name for the shell tool
	modelName := large.Model() // fallback to ID if Name not available
	if model := cfg.Config().GetModel(large.Provider(), large.Model()); model != nil {
		modelName = model.Name
	}

	allTools := []fantasy.AgentTool{
		tools.NewShellTool(env.workingDir, "coder", cfg.Config().Options.Attribution, modelName, nil),
		tools.NewEditTool(nil, env.history, *env.filetracker, env.workingDir),
		tools.NewEditTool(nil, env.history, *env.filetracker, env.workingDir),
		tools.NewFetchTool(client),
		tools.NewViewTool(nil, *env.filetracker, nil, env.workingDir),
		tools.NewWriteTool(nil, env.history, *env.filetracker, env.workingDir),
	}

	agent := testSessionAgent(env, large, small, systemPrompt, allTools...)
	// A scripted model never benefits from a retry: it cannot fail
	// transiently, and the default backoff would turn a genuine scripting
	// mistake into 35s of waiting before the real error surfaces.
	if sa, ok := agent.(*sessionAgent); ok {
		noRetries := 0
		sa.maxRetries = &noRetries
	}
	return agent, nil
}

// createSimpleGoProject creates a simple Go project structure in the given directory.
// It creates a go.mod file and a main.go file with a basic hello world program.
func createSimpleGoProject(t *testing.T, dir string) {
	goMod := `module example.com/testproject

go 1.23
`
	err := os.WriteFile(dir+"/go.mod", []byte(goMod), 0o644)
	require.NoError(t, err)

	mainGo := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	err = os.WriteFile(dir+"/main.go", []byte(mainGo), 0o644)
	require.NoError(t, err)
}
