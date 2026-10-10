// Package envvars names every environment variable Harness reads to
// configure itself. Code reads them through these constants, never through
// a string literal of its own, and each one is documented in the
// "Environment variables" section of docs/config/README.md; the package
// test holds both to that.
//
// The variables Harness sets for the commands it runs (hooks, the shell)
// are not here: those are documented where they are set.
package envvars

// Directories and files.
const (
	// GlobalConfig overrides the directory the global config.yaml lives in.
	GlobalConfig = "HARNESS_GLOBAL_CONFIG"
	// GlobalData overrides the global data directory (state, catalog,
	// sessions of workspaces without their own data directory).
	GlobalData = "HARNESS_GLOBAL_DATA"
	// CacheDir overrides the global cache directory (logs, server sockets).
	CacheDir = "HARNESS_CACHE_DIR"
	// CrashDir overrides where crash reports are written.
	CrashDir = "HARNESS_CRASH_DIR"
	// ScratchDir overrides the root of per-session scratch directories.
	ScratchDir = "HARNESS_SCRATCH_DIR"
	// SkillsDir replaces the global skill directories.
	SkillsDir = "HARNESS_SKILLS_DIR"
	// SubagentsDir replaces the global subagent directories.
	SubagentsDir = "HARNESS_SUBAGENTS_DIR"
	// ExtensionsDir replaces the global extension directories.
	ExtensionsDir = "HARNESS_EXTENSIONS_DIR"
	// CommandsDir replaces the global custom-command directories.
	CommandsDir = "HARNESS_COMMANDS_DIR"
	// SkipDataDirLock bypasses the lock that keeps two instances off one
	// data directory.
	SkipDataDirLock = "HARNESS_SKIP_DATADIR_LOCK"
)

// Providers and requests.
const (
	// DisableProviderAutoUpdate is the environment form of
	// options.disable_provider_auto_update.
	DisableProviderAutoUpdate = "HARNESS_DISABLE_PROVIDER_AUTO_UPDATE"
	// DisableDefaultProviders is the environment form of
	// options.disable_default_providers.
	DisableDefaultProviders = "HARNESS_DISABLE_DEFAULT_PROVIDERS"
	// DisableAnthropicCache turns off Anthropic prompt-cache breakpoints.
	DisableAnthropicCache = "HARNESS_DISABLE_ANTHROPIC_CACHE"
	// DisablePromptCacheKey turns off the per-session prompt_cache_key sent
	// to OpenAI-style APIs.
	DisablePromptCacheKey = "HARNESS_DISABLE_PROMPT_CACHE_KEY"
)

// The shell and terminal sessions.
const (
	// CoreUtils forces the built-in Go coreutils on or off (default: on
	// Windows only).
	CoreUtils = "HARNESS_CORE_UTILS"
	// PTYRows and PTYCols set the size new terminal sessions open with.
	PTYRows = "HARNESS_PTY_ROWS"
	PTYCols = "HARNESS_PTY_COLS"
	// PTYTerm overrides the TERM terminal sessions run with.
	PTYTerm = "HARNESS_PTY_TERM"
	// PromptStateFile names, for a terminal session's shell, the file
	// its prompt hook writes its per-prompt account to. Harness sets it
	// into the session; the hook the session setup installs reads it.
	PromptStateFile = "HARNESS_PROMPT_STATE"
	// ShellMemoryMax caps the memory a terminal session's processes may
	// hold, as systemd's MemoryMax reads it (default 75% of RAM);
	// "infinity" keeps the session's scope without a cap and "off" runs
	// shells without one (Linux with a systemd user manager only).
	ShellMemoryMax = "HARNESS_SHELL_MEMORY_MAX"
)

// The client/server split.
const (
	// ClientServer runs the TUI against a Harness server process.
	ClientServer = "HARNESS_CLIENT_SERVER"
	// ServerIdleTimeout is how long, in seconds, a server with no clients
	// lingers before exiting; 0 disables lingering.
	ServerIdleTimeout = "HARNESS_SERVER_IDLE_TIMEOUT"
	// ServerDetachGrace is how long, in seconds, a workspace outlives its
	// last client detaching.
	ServerDetachGrace = "HARNESS_SERVER_DETACH_GRACE"
	// ServerReadyTimeout bounds the wait for a started server to answer, as
	// a Go duration.
	ServerReadyTimeout = "HARNESS_SERVER_READY_TIMEOUT"
)

// Diagnostics.
const (
	// Profile serves pprof on localhost:6060.
	Profile = "HARNESS_PROFILE"
	// UIDebug paints a changing block on every TUI redraw.
	UIDebug = "HARNESS_UI_DEBUG"
)

// All lists every variable above.
func All() []string {
	return []string{
		GlobalConfig, GlobalData, CacheDir, CrashDir, ScratchDir, SkillsDir,
		SubagentsDir, ExtensionsDir, CommandsDir, SkipDataDirLock,
		DisableProviderAutoUpdate, DisableDefaultProviders,
		DisableAnthropicCache, DisablePromptCacheKey,
		CoreUtils, PTYRows, PTYCols, PTYTerm, PromptStateFile, ShellMemoryMax,
		ClientServer, ServerIdleTimeout, ServerDetachGrace, ServerReadyTimeout,
		Profile, UIDebug,
	}
}
