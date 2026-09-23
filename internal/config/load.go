package config

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	powernapConfig "github.com/charmbracelet/x/powernap/pkg/config"
	"github.com/qjebbs/go-jsons"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/discover"
	"github.com/stubbedev/harness/internal/env"
	"github.com/stubbedev/harness/internal/filepathext"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/home"
	"github.com/tidwall/gjson"
)

// Load loads the configuration from the default paths and returns a
// ConfigStore that owns both the pure-data Config and all runtime state.
func Load(workingDir, dataDir string, debug bool) (*ConfigStore, error) {
	disk, err := readConfig(context.Background(), workingDir, dataDir, debug)
	if err != nil {
		return nil, err
	}
	cfg := disk.cfg

	store := &ConfigStore{
		config:         cfg,
		workingDir:     workingDir,
		debug:          debug,
		globalDataPath: GlobalConfigData(),
		workspacePath:  disk.workspacePath,
		loadedPaths:    disk.loadedPaths,
	}

	// Load known providers, this loads the config from models.dev. A
	// failed refresh still yields the cached or embedded catalog, so
	// only an empty list is fatal: starting up without providers is
	// worse than starting up with slightly stale ones.
	providers, err := Providers(cfg)
	if err != nil {
		if len(providers) == 0 {
			return nil, err
		}
		slog.Warn("Continuing with the previously known providers", "error", err)
	}
	store.knownProviders = providers

	env := env.New()
	// Configure providers
	valueResolver := NewShellVariableResolver(env)
	store.resolver = valueResolver

	// Hold writeMu during initial load to prevent configureProviders
	// from triggering auto-reload via RemoveConfigField.
	store.writeMu.Lock()
	defer store.writeMu.Unlock()

	// Apply top-level env vars before configuring providers so variables
	// like AWS_PROFILE are visible to the AWS SDK credential chain.
	cfg.applyEnv(valueResolver)

	if err := cfg.configureProviders(context.Background(), store, env, valueResolver, store.knownProviders); err != nil {
		return nil, fmt.Errorf("failed to configure providers: %w", err)
	}

	// Agents depend only on the tool set, not on any provider, and callers
	// (the sub-agent dispatcher among them) look them up even when nothing is
	// configured yet, so set them up before the unconfigured early return.
	store.SetupAgents()

	if !cfg.IsConfigured() {
		slog.Warn("No providers configured")
		return store, nil
	}

	resolved, err := resolveSelectedModels(cfg, store.knownProviders)
	if err != nil {
		return nil, fmt.Errorf("failed to configure selected models: %w", err)
	}
	applyResolvedModels(cfg, resolved)

	// Persist any fallback corrections while we still hold writeMu.
	if resolved.LargeFallback {
		if err := store.updateLocked(ScopeGlobal, func(c *Config) map[string]any {
			return store.updatePreferredModelFields(c, SelectedModelTypeLarge, resolved.Large)
		}); err != nil {
			return nil, fmt.Errorf("failed to update preferred large model: %w", err)
		}
	}
	if resolved.SmallFallback {
		if err := store.updateLocked(ScopeGlobal, func(c *Config) map[string]any {
			return store.updatePreferredModelFields(c, SelectedModelTypeSmall, resolved.Small)
		}); err != nil {
			return nil, fmt.Errorf("failed to update preferred small model: %w", err)
		}
	}

	store.CaptureStalenessSnapshot(disk.trackedPaths())

	return store, nil
}

// diskConfig is the disk half of a load: every config file read, merged
// over the defaults with the workspace state file on top, and validated,
// before any provider is configured. Load and ReloadFromDisk both build it
// through readConfig so the two paths cannot drift apart.
type diskConfig struct {
	cfg           *Config
	configPaths   []string // every discovered config path, loaded or not
	loadedPaths   []string // the paths that actually loaded
	workspacePath string
}

// readConfig reads, merges, defaults and validates the configuration for
// workingDir. dataDir, when set, overrides the data directory; debug forces
// debug logging on regardless of the files.
func readConfig(ctx context.Context, workingDir, dataDir string, debug bool) (diskConfig, error) {
	configPaths := lookupConfigs(workingDir)
	cfg, loadedPaths, err := loadFromConfigPaths(ctx, configPaths)
	if err != nil {
		return diskConfig{}, fmt.Errorf("failed to load config from paths %v: %w", configPaths, err)
	}

	// A data directory set on the command line or in a config file is
	// used verbatim; only the defaulted location is migrated from the
	// legacy in-repo .harness layout.
	explicitDataDir := dataDir != "" || (cfg.Options != nil && cfg.Options.DataDirectory != "")
	cfg.setDefaults(workingDir, dataDir)
	if !explicitDataDir && cfg.Options.DataDirectory != "" {
		migrateLegacyDataDir(workingDir, cfg.Options.DataDirectory)
	}

	// Load workspace config last so it has highest priority.
	workspacePath := filepath.Join(cfg.Options.DataDirectory, stateConfigFile)
	wsJSON, err := readConfigJSON(workspacePath)
	if err != nil {
		return diskConfig{}, err
	}
	merged, err := mergeWorkspaceConfig(cfg, wsJSON)
	if err != nil {
		slog.Warn("Ignoring workspace config that failed to merge", "path", workspacePath, "error", err)
	} else if merged != nil {
		// Preserve defaults that setDefaults already applied.
		dataDir := cfg.Options.DataDirectory
		*cfg = *merged
		cfg.setDefaults(workingDir, dataDir)
		loadedPaths = append(loadedPaths, workspacePath)
	}

	if debug {
		cfg.Options.Debug = true
	}

	// Validate hooks after all config merging is complete so workspace
	// hooks also get their matcher regexes compiled.
	if err := cfg.ValidateHooks(); err != nil {
		return diskConfig{}, fmt.Errorf("invalid hook configuration: %w", err)
	}
	if err := cfg.Verification.Validate(); err != nil {
		return diskConfig{}, fmt.Errorf("invalid verification configuration: %w", err)
	}

	if worktreeRoot(workingDir) == "" {
		const depth = 2
		const items = 100
		slog.Warn("No git repository detected in working directory, will limit file walk operations", "depth", depth, "items", items)
		assignIfNil(&cfg.Options.TUI.Completions.MaxDepth, depth)
		assignIfNil(&cfg.Options.TUI.Completions.MaxItems, items)
	}

	return diskConfig{
		cfg:           cfg,
		configPaths:   configPaths,
		loadedPaths:   loadedPaths,
		workspacePath: workspacePath,
	}, nil
}

// trackedPaths is every path the staleness check watches: all discovered
// config paths, not just the ones that loaded, so a config file created
// after the load (e.g. a project harness.yaml added mid-session) is
// detected as a change.
func (d diskConfig) trackedPaths() []string {
	return append(slices.Clone(d.configPaths), d.loadedPaths...)
}

// mergeWorkspaceConfig merges the workspace config (already converted to
// JSON) over cfg and returns the result. A workspace file that carries no
// settings yields a nil config and no error, so the caller keeps what it
// already has.
func mergeWorkspaceConfig(cfg *Config, workspaceJSON []byte) (*Config, error) {
	if len(workspaceJSON) == 0 {
		return nil, nil
	}
	return loadFromBytes([][]byte{mustMarshalConfig(cfg), workspaceJSON})
}

// mustMarshalConfig marshals the config to JSON bytes, returning empty JSON on
// error.
func mustMarshalConfig(cfg *Config) []byte {
	data, err := json.Marshal(cfg)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// pushHarnessEnv makes every HARNESS_<NAME> variable visible as <NAME> for
// the duration of provider configuration, and returns the function that
// restores the previous environment. A variable that was unset before is
// unset again, not left behind as an empty string, so ${VAR-default} and
// LookupEnv checks still see it as absent.
func pushHarnessEnv() func() {
	type backup struct {
		value string
		set   bool
	}
	backups := make(map[string]backup)
	for _, ev := range os.Environ() {
		key, value, ok := strings.Cut(ev, "=")
		name, harness := strings.CutPrefix(key, "HARNESS_")
		if !ok || !harness || name == "" {
			continue
		}
		prev, set := os.LookupEnv(name)
		backups[name] = backup{value: prev, set: set}
		os.Setenv(name, value)
	}

	return func() {
		for name, b := range backups {
			if b.set {
				os.Setenv(name, b.value)
			} else {
				os.Unsetenv(name)
			}
		}
	}
}

func (c *Config) configureProviders(ctx context.Context, store *ConfigStore, env env.Env, resolver VariableResolver, knownProviders []catalog.Provider) error {
	knownProviderNames := make(map[string]bool)
	restore := pushHarnessEnv()
	defer restore()

	// When disable_default_providers is enabled, skip all default/embedded
	// providers entirely. Users must fully specify any providers they want.
	// We skip to the custom provider validation loop which handles all
	// user-configured providers uniformly.
	if c.Options.DisableDefaultProviders {
		knownProviders = nil
	}

	c.migrateLegacyProviderIDs(knownProviders)

	for _, p := range knownProviders {
		knownProviderNames[string(p.ID)] = true
		config, configExists := c.Providers.Get(string(p.ID))
		// if the user configured a known provider we need to allow it to override a couple of parameters
		if configExists {
			if config.BaseURL != "" {
				p.APIEndpoint = config.BaseURL
			}
			if config.APIKey != "" {
				p.APIKey = config.APIKey
			}
			if len(config.Models) > 0 {
				// User models first so they win over catalog entries
				// with the same ID.
				var models []catalog.Model
				seen := make(map[string]bool)
				for _, model := range slices.Concat(config.Models, p.Models) {
					if seen[model.ID] {
						continue
					}
					seen[model.ID] = true
					model.Name = cmp.Or(model.Name, model.ID)
					models = append(models, model)
				}
				p.Models = models
			}
		}

		// Provider headers use the same error contract as MCP headers:
		// a failing $(...) aborts the provider load with a clear
		// message, and a header that resolves to the empty string
		// (unset bare $VAR under lenient nounset, $(echo), or literal
		// "") is dropped from the outgoing request.
		headers := maps.Clone(p.DefaultHeaders)
		if headers == nil {
			headers = make(map[string]string)
		}
		maps.Copy(headers, config.ExtraHeaders)
		headers, err := resolveHeaders(headers, resolver)
		if err != nil {
			return fmt.Errorf("resolving provider %s: %w", p.ID, err)
		}
		// Start from user config so all user fields survive without
		// explicit copying. Overlay catwalk identity/endpoint fields
		// (already merged with user overrides above).
		prepared := config
		prepared.ID = string(p.ID)
		prepared.Name = p.Name
		prepared.BaseURL = p.APIEndpoint
		prepared.APIKey = p.APIKey
		prepared.APIKeyTemplate = p.APIKey // Store original template for re-resolution
		prepared.Type = p.Type
		prepared.Models = p.Models
		prepared.ExtraHeaders = headers
		if prepared.ExtraParams == nil {
			prepared.ExtraParams = make(map[string]string)
		}

		switch {
		case p.ID == catalog.InferenceProviderAnthropic && config.OAuthToken != nil:
			// Claude Code subscription is not supported anymore. Remove to show onboarding.
			// RemoveConfigField persists the deletion to disk. The in-memory
			// state is kept consistent by the Providers.Del call below; any
			// concurrent reload that races with this write will also see the
			// removal because it re-reads from disk.
			store.RemoveConfigField(ScopeGlobal, "providers.anthropic")
			c.Providers.Del(string(p.ID))
			continue
		case p.ID == catalog.InferenceProviderCopilot && config.OAuthToken != nil:
			prepared.SetupGitHubCopilot()
		}

		// skip leaves a provider unconfigured. Only one the user configured
		// is worth a warning, and its entry is removed so it does not show
		// as connected.
		skip := func(msg string, args ...any) {
			if configExists {
				slog.Warn(msg, append([]any{"provider", p.ID}, args...)...)
				c.Providers.Del(string(p.ID))
			}
		}

		switch p.ID {
		// Handle specific providers that require additional configuration
		case catalog.InferenceProviderVertexAI:
			var (
				project  = env.Get("VERTEXAI_PROJECT")
				location = env.Get("VERTEXAI_LOCATION")
			)
			if project == "" || location == "" {
				skip("Skipping Vertex AI provider due to missing credentials")
				continue
			}
			prepared.ExtraParams["project"] = project
			prepared.ExtraParams["location"] = location
		case catalog.InferenceProviderAzure:
			endpoint, err := resolver.ResolveValue(p.APIEndpoint)
			if err != nil || endpoint == "" {
				skip("Skipping Azure provider due to missing API endpoint", "error", err)
				continue
			}
			prepared.BaseURL = endpoint
			prepared.ExtraParams["apiVersion"] = env.Get("AZURE_OPENAI_API_VERSION")
		case catalog.InferenceProviderBedrock:
			// The catalog carries "$AWS_ACCESS_KEY_ID" as the key, so the
			// template is never empty: resolve it before deciding, or
			// Bedrock configures itself on every machine, credentials or
			// not, and then shows up as a connected provider.
			key, err := resolver.ResolveValue(p.APIKey)
			if (key == "" || err != nil) && !hasAWSCredentials(env) {
				skip("Skipping Bedrock provider due to missing AWS credentials")
				continue
			}
		default:
			// if the provider api or endpoint are missing we skip them
			v, err := resolver.ResolveValue(p.APIKey)
			if v == "" || err != nil {
				skip("Skipping provider due to missing API key")
				continue
			}
		}
		c.Providers.Set(string(p.ID), prepared)
	}

	// Discover models concurrently for custom providers that need it.
	// A provider needs discovery when discover_models is explicitly true,
	// or when the models list is empty (auto-trigger, unless opted out).
	type discoveryResult struct {
		models []catalog.Model
		err    error
	}

	discoveryResults := make(map[string]discoveryResult)
	var mu sync.Mutex
	var wg sync.WaitGroup

	discoverCtx, discoverCancel := context.WithTimeout(ctx, 3*time.Second)
	for id, pc := range c.Providers.Seq2() {
		if knownProviderNames[id] {
			continue
		}
		if pc.Disable || pc.BaseURL == "" {
			continue
		}
		wantsDiscovery := pc.AutoDiscoverModels != nil && *pc.AutoDiscoverModels
		autoTrigger := len(pc.Models) == 0 && (pc.AutoDiscoverModels == nil || *pc.AutoDiscoverModels)
		if !wantsDiscovery && !autoTrigger {
			continue
		}
		providerID := cmp.Or(pc.ID, id)
		cfg := discover.Config{
			ID:             providerID,
			BaseURL:        pc.BaseURL,
			APIKey:         pc.APIKey,
			ExtraHeaders:   pc.ExtraHeaders,
			ExistingModels: pc.Models,
		}
		providerType := cmp.Or(pc.Type, catalog.TypeOpenAICompat)
		wg.Go(func() {
			models, err := discover.DiscoverModels(discoverCtx, cfg, resolver)
			if err == nil && len(models) > 0 {
				if enricher := discover.GetEnricher(string(providerType)); enricher != nil {
					models, _ = enricher.EnrichModels(discoverCtx, cfg, resolver, models)
				}
			}
			mu.Lock()
			discoveryResults[id] = discoveryResult{models: models, err: err}
			mu.Unlock()
		})
	}
	wg.Wait()
	discoverCancel()

	// Validate the custom providers.
	for id, providerConfig := range c.Providers.Seq2() {
		if knownProviderNames[id] {
			continue
		}

		// Make sure the provider ID is set.
		providerConfig.ID = id
		providerConfig.Name = cmp.Or(providerConfig.Name, id) // Use ID as name if not set
		// Default to OpenAI if not set.
		providerConfig.Type = cmp.Or(providerConfig.Type, catalog.TypeOpenAICompat)
		if !slices.Contains(catalog.KnownProviderTypes(), providerConfig.Type) &&
			!discover.IsKnownCustomProvider(string(providerConfig.Type)) {
			slog.Warn("Skipping custom provider due to unsupported provider type", "provider", id)
			c.Providers.Del(id)
			continue
		}

		if providerConfig.Disable {
			slog.Debug("Skipping custom provider due to disable flag", "provider", id)
			c.Providers.Del(id)
			continue
		}
		apiKey, err := resolver.ResolveValue(providerConfig.APIKey)
		if apiKey == "" || err != nil {
			slog.Warn("Provider is missing API key, this might be OK for local providers", "provider", id)
		}
		baseURL, err := resolver.ResolveValue(providerConfig.BaseURL)
		if baseURL == "" || err != nil {
			slog.Warn("Skipping custom provider due to missing API endpoint", "provider", id, "error", err)
			c.Providers.Del(id)
			continue
		}

		// Apply discovery results if available.
		if result, ok := discoveryResults[id]; ok {
			if result.err != nil {
				slog.Warn("Model discovery failed", "provider", id, "error", result.err)
				if len(providerConfig.Models) == 0 {
					slog.Warn("Skipping provider with no models after failed discovery", "provider", id)
					c.Providers.Del(id)
					continue
				}
			} else if len(result.models) > 0 {
				providerConfig.Models = result.models
				slog.Info("Discovered models for provider", "provider", id, "count", len(result.models))
			}
		}

		if len(providerConfig.Models) == 0 {
			slog.Warn("Skipping custom provider because the provider has no models", "provider", id)
			c.Providers.Del(id)
			continue
		}

		// Custom-provider headers share the MCP error contract; see the
		// known-provider loop above. The resolved headers go into a fresh
		// map, leaving the loaded templates untouched.
		providerConfig.ExtraHeaders, err = resolveHeaders(providerConfig.ExtraHeaders, resolver)
		if err != nil {
			return fmt.Errorf("resolving provider %s: %w", id, err)
		}

		c.Providers.Set(id, providerConfig)
	}

	if c.Providers.Len() == 0 && c.Options.DisableDefaultProviders {
		return fmt.Errorf("default providers are disabled and there are no custom providers are configured")
	}

	return nil
}

// migrateLegacyProviderIDs rewrites provider entries written against
// the old catwalk catalog onto the ids models.dev uses. Without it such
// an entry matches no known provider, falls through to the
// custom-provider path, and is dropped for having no base URL -- the
// provider simply disappears, credentials and all.
//
// The rewrite is in-memory only: the config file is the user's, and a
// warning tells them which id to write the next time they edit it.
func (c *Config) migrateLegacyProviderIDs(knownProviders []catalog.Provider) {
	if c.Providers == nil || c.Providers.Len() == 0 {
		return
	}

	known := make(map[string]bool, len(knownProviders))
	for _, p := range knownProviders {
		known[string(p.ID)] = true
	}

	configured := slices.Sorted(maps.Keys(c.Providers.Copy()))
	for _, id := range configured {
		if known[id] {
			// An id that still exists but no longer points at the same
			// service cannot be rewritten -- both readings are
			// legitimate -- so it is only reported.
			if note, ok := catalog.ProviderMeaningChanged(id); ok {
				slog.Info("Provider id has changed meaning since catwalk", "provider", id, "note", note)
			}
			continue
		}
		current, renamed := catalog.LegacyProviderID(id)
		if !renamed || !known[current] {
			continue
		}
		providerConfig, ok := c.Providers.Get(id)
		if !ok {
			continue
		}
		if _, taken := c.Providers.Get(current); taken {
			slog.Warn("Ignoring provider entry under its former id", "provider", id, "renamed_to", current)
			c.Providers.Del(id)
			continue
		}
		slog.Warn("Provider was renamed; applying its config under the current id", "provider", id, "renamed_to", current)
		providerConfig.ID = current
		c.Providers.Del(id)
		c.Providers.Set(current, providerConfig)
	}
}

// applyEnv sets top-level env vars from the config. Keys are sorted for
// deterministic ordering so that vars referencing other vars via the
// value resolver produce consistent results.
func (c *Config) applyEnv(resolver VariableResolver) {
	for _, k := range slices.Sorted(maps.Keys(c.Env)) {
		resolved, err := resolver.ResolveValue(c.Env[k])
		if err != nil {
			slog.Warn("Skipping env var due to resolution failure.", "key", k, "value", c.Env[k], "error", err)
			continue
		}
		os.Setenv(k, resolved)
	}
}

// NormalizeOptions allocates Options and Options.TUI and fills in the option
// defaults the UI relies on, so readers can dereference them without guarding.
// Configs loaded from disk get this via setDefaults; configs arriving over the
// wire from a Harness server need the same treatment before the UI reads them.
//
// DiffMode is deliberately left alone: the permissions dialog reads its zero
// value as "choose split or unified from the terminal width".
func (c *Config) NormalizeOptions() {
	if c.Options == nil {
		c.Options = &Options{}
	}
	if c.Options.TUI == nil {
		c.Options.TUI = &TUIOptions{}
	}
	if c.Options.TUI.Scrollbar == "" {
		c.Options.TUI.Scrollbar = ScrollbarDefault
	}
	if c.Options.TUI.ExitBanner == "" {
		c.Options.TUI.ExitBanner = ExitBannerDefault
	}
}

// appendMissing appends each of items not already in dst, keeping order.
func appendMissing(dst []string, items ...string) []string {
	for _, item := range items {
		if !slices.Contains(dst, item) {
			dst = append(dst, item)
		}
	}
	return dst
}

func (c *Config) setDefaults(workingDir, dataDir string) {
	c.NormalizeOptions()
	if len(c.Options.GlobalContextPaths) == 0 {
		harnessConfigDir := filepath.Dir(GlobalConfig())
		c.Options.GlobalContextPaths = []string{
			filepath.Join(harnessConfigDir, "HARNESS.md"),
			filepath.Join(filepath.Dir(harnessConfigDir), "AGENTS.md"),
		}
	}
	slices.Sort(c.Options.GlobalContextPaths)
	c.Options.GlobalContextPaths = slices.Compact(c.Options.GlobalContextPaths)

	if dataDir != "" {
		c.Options.DataDirectory = dataDir
	} else if c.Options.DataDirectory == "" {
		// Machine-owned state lives under the global data root, one
		// directory per workspace, so nothing harness-owned is written
		// inside the user's project. An explicit options.data_directory
		// from the config file still wins.
		c.Options.DataDirectory = DefaultWorkspaceDataDirectory(workingDir)
	}
	c.Options.DataDirectory = filepath.Clean(filepathext.SmartJoin(workingDir, c.Options.DataDirectory))
	if c.Providers == nil {
		c.Providers = csync.NewMap[string, ProviderConfig]()
	}
	if c.Models == nil {
		c.Models = make(map[SelectedModelType]SelectedModel)
	}
	if c.RecentModels == nil {
		c.RecentModels = make(map[SelectedModelType][]SelectedModel)
	}
	if c.MCP == nil {
		c.MCP = make(map[string]MCPConfig)
	}
	// Drop orphaned OAuth token entries left behind when a user removes
	// an MCP from the config file. See MCPConfig.isOrphanedToken.
	for name, m := range c.MCP {
		if m.isOrphanedToken() {
			delete(c.MCP, name)
		}
	}
	if c.LSP == nil {
		c.LSP = make(map[string]LSPConfig)
	}

	// Apply defaults to LSP configurations
	c.applyLSPDefaults()

	// Add the default context paths if they are not already present
	c.Options.ContextPaths = append(slices.Clone(defaultContextPaths), c.Options.ContextPaths...)

	slices.Sort(c.Options.ContextPaths)
	c.Options.ContextPaths = slices.Compact(c.Options.ContextPaths)

	// Default skill, subagent and extension directories, global then
	// project so project entries win. setDefaults runs a second time after
	// the workspace state file is merged, so only missing entries are added;
	// an unconditional append would list every directory twice.
	c.Options.SkillsPaths = appendMissing(c.Options.SkillsPaths, GlobalSkillsDirs()...)
	c.Options.SkillsPaths = appendMissing(c.Options.SkillsPaths, ProjectSkillsDir(workingDir)...)
	c.Options.SubagentsPaths = appendMissing(c.Options.SubagentsPaths, GlobalSubagentsDirs()...)
	c.Options.SubagentsPaths = appendMissing(c.Options.SubagentsPaths, ProjectSubagentsDir(workingDir)...)
	c.Options.ExtensionsPaths = appendMissing(c.Options.ExtensionsPaths, GlobalExtensionsDirs()...)
	c.Options.ExtensionsPaths = appendMissing(c.Options.ExtensionsPaths, ProjectExtensionsDir(workingDir)...)

	if str, ok := os.LookupEnv("HARNESS_DISABLE_PROVIDER_AUTO_UPDATE"); ok {
		c.Options.DisableProviderAutoUpdate, _ = strconv.ParseBool(str)
	}

	if str, ok := os.LookupEnv("HARNESS_DISABLE_DEFAULT_PROVIDERS"); ok {
		c.Options.DisableDefaultProviders, _ = strconv.ParseBool(str)
	}

	c.Options.InitializeAs = cmp.Or(c.Options.InitializeAs, defaultInitializeAs)

	// A ratio of 1 or more would summarize on every step and negative values
	// mean nothing, so fall back to the defaults instead of wedging a session.
	if ratio := c.Options.AutoSummarizeRatio; ratio < 0 || ratio >= 1 {
		if ratio != 0 {
			slog.Warn("Ignoring out-of-range auto_summarize_ratio, expected a value above 0 and below 1", "ratio", ratio)
		}
		c.Options.AutoSummarizeRatio = 0
	}
	if buffer := c.Options.AutoSummarizeBuffer; buffer < 0 {
		slog.Warn("Ignoring negative auto_summarize_buffer", "buffer", buffer)
		c.Options.AutoSummarizeBuffer = 0
	}
}

// powernapDefaults caches the powernap default LSP server catalog. The
// catalog is static and immutable for the life of the process, but
// building it (NewManager + LoadDefaults) is expensive and was previously
// repeated on every config reload. We load it once and only ever read from
// it via GetServer, so a shared instance is safe.
var (
	powernapDefaultsOnce sync.Once
	powernapDefaults     *powernapConfig.Manager
)

func lspDefaultsManager() *powernapConfig.Manager {
	powernapDefaultsOnce.Do(func() {
		m := powernapConfig.NewManager()
		// LoadDefaults only fails on malformed embedded defaults, which
		// would be a build-time bug; treat the manager as usable either
		// way so a transient error never wedges config loading.
		_ = m.LoadDefaults()
		powernapDefaults = m
	})
	return powernapDefaults
}

// applyLSPDefaults applies default values from powernap to LSP configurations
func (c *Config) applyLSPDefaults() {
	// Reuse the process-wide default catalog; building it per reload was a
	// significant chunk of reload latency.
	configManager := lspDefaultsManager()

	// Apply defaults to each LSP configuration
	for name, cfg := range c.LSP {
		// Try to get defaults from powernap based on name or command name.
		base, ok := configManager.GetServer(name)
		if !ok {
			base, ok = configManager.GetServer(cfg.Command)
			if !ok {
				continue
			}
		}
		if cfg.Options == nil {
			cfg.Options = base.Settings
		}
		if cfg.InitOptions == nil {
			cfg.InitOptions = base.InitOptions
		}
		if len(cfg.FileTypes) == 0 {
			cfg.FileTypes = base.FileTypes
		}
		if len(cfg.RootMarkers) == 0 {
			cfg.RootMarkers = base.RootMarkers
		}
		cfg.Command = cmp.Or(cfg.Command, base.Command)
		if len(cfg.Args) == 0 {
			cfg.Args = base.Args
		}
		if len(cfg.Env) == 0 {
			cfg.Env = base.Environment
		}
		// Update the config in the map
		c.LSP[name] = cfg
	}
}

func (c *Config) defaultModelSelection(knownProviders []catalog.Provider) (largeModel SelectedModel, smallModel SelectedModel, err error) {
	if len(knownProviders) == 0 && c.Providers.Len() == 0 {
		err = fmt.Errorf("no providers configured, please configure at least one provider")
		return largeModel, smallModel, err
	}

	// Use the first provider enabled based on the known providers order
	// if no provider found that is known use the first provider configured
	defaultSelection := func(providerID, modelID, label string) (SelectedModel, error) {
		model := c.GetModel(providerID, modelID)
		if model == nil {
			slog.Warn(fmt.Sprintf("Default %s model %s not found for provider %s", label, modelID, providerID))
			providerConfig, ok := c.Providers.Get(providerID)
			if !ok || len(providerConfig.Models) == 0 {
				return SelectedModel{}, fmt.Errorf("default %s model %s not found for provider %s", label, modelID, providerID)
			}
			model = &providerConfig.Models[0]
		}
		return SelectedModel{
			Provider:        providerID,
			Model:           model.ID,
			MaxTokens:       model.DefaultMaxTokens,
			ReasoningEffort: catalog.DefaultReasoningLevel(model.ReasoningLevels),
		}, nil
	}
	for _, p := range knownProviders {
		providerConfig, ok := c.Providers.Get(string(p.ID))
		if !ok || providerConfig.Disable {
			continue
		}
		if largeModel, err = defaultSelection(string(p.ID), p.DefaultLargeModelID, "large"); err != nil {
			return largeModel, smallModel, err
		}
		if smallModel, err = defaultSelection(string(p.ID), p.DefaultSmallModelID, "small"); err != nil {
			return largeModel, smallModel, err
		}
		return largeModel, smallModel, nil
	}

	enabledProviders := c.EnabledProviders()
	slices.SortFunc(enabledProviders, func(a, b ProviderConfig) int {
		return strings.Compare(a.ID, b.ID)
	})

	if len(enabledProviders) == 0 {
		err = fmt.Errorf("no providers configured, please configure at least one provider")
		return largeModel, smallModel, err
	}

	// A custom provider publishes no defaults, so its first model serves
	// as both.
	providerConfig := enabledProviders[0]
	if len(providerConfig.Models) == 0 {
		err = fmt.Errorf("provider %s has no models configured", providerConfig.ID)
		return largeModel, smallModel, err
	}
	largeModel, err = defaultSelection(providerConfig.ID, providerConfig.Models[0].ID, "large")
	return largeModel, largeModel, err
}

// resolveSelection overlays a user-selected model's provider and model
// ID onto dst (where the selection is silent, dst keeps its current
// values) and returns the catalog model for the overlaid selection, or
// nil when it does not resolve.
func (c *Config) resolveSelection(dst *SelectedModel, sel SelectedModel) *catalog.Model {
	if sel.Model != "" {
		dst.Model = sel.Model
	}
	if sel.Provider != "" {
		dst.Provider = sel.Provider
	}
	return c.GetModel(dst.Provider, dst.Model)
}

// applySelectionParams copies a user-selected model's tuning parameters
// onto dst, using the catalog model's defaults where the selection is
// silent.
func applySelectionParams(dst *SelectedModel, sel SelectedModel, model *catalog.Model) {
	if sel.MaxTokens > 0 {
		dst.MaxTokens = sel.MaxTokens
	} else {
		dst.MaxTokens = model.DefaultMaxTokens
	}
	if sel.ReasoningEffort != "" {
		dst.ReasoningEffort = sel.ReasoningEffort
	} else {
		dst.ReasoningEffort = catalog.DefaultReasoningLevel(model.ReasoningLevels)
	}
	dst.Think = sel.Think
	if sel.Temperature != nil {
		dst.Temperature = sel.Temperature
	}
	if sel.TopP != nil {
		dst.TopP = sel.TopP
	}
	if sel.TopK != nil {
		dst.TopK = sel.TopK
	}
	if sel.FrequencyPenalty != nil {
		dst.FrequencyPenalty = sel.FrequencyPenalty
	}
	if sel.PresencePenalty != nil {
		dst.PresencePenalty = sel.PresencePenalty
	}
	if sel.ProviderOptions != nil {
		dst.ProviderOptions = maps.Clone(sel.ProviderOptions)
	}
}

// resolvedModels holds the result of resolving user-configured model
// selections against the provider catalog.
type resolvedModels struct {
	Large           SelectedModel
	Small           SelectedModel
	LargeConfigured SelectedModel
	LargeFallback   bool // true if Large was corrected to a default
	SmallFallback   bool // true if Small was corrected to a default
}

// applyResolvedModels copies resolution results onto cfg. It does not persist.
func applyResolvedModels(cfg *Config, resolved resolvedModels) {
	if cfg.Models == nil {
		cfg.Models = make(map[SelectedModelType]SelectedModel)
	}
	cfg.Models[SelectedModelTypeLarge] = resolved.Large
	cfg.Models[SelectedModelTypeSmall] = resolved.Small
	cfg.LargeFallback = resolved.LargeFallback
	cfg.LargeConfigured = resolved.LargeConfigured
}

// resolveSelectedModels validates the user's configured model selections
// against the provider catalog, falling back to defaults when a model ID is
// invalid. It is pure resolution logic: it does not mutate the store or
// touch disk. The caller assigns the results to c.Models and persists any
// fallback corrections as appropriate.
func resolveSelectedModels(cfg *Config, knownProviders []catalog.Provider) (resolvedModels, error) {
	var result resolvedModels
	defaultLarge, defaultSmall, err := cfg.defaultModelSelection(knownProviders)
	if err != nil {
		return result, fmt.Errorf("failed to select default models: %w", err)
	}
	large, small := defaultLarge, defaultSmall

	if largeModelSelected, ok := cfg.Models[SelectedModelTypeLarge]; ok {
		result.LargeConfigured = largeModelSelected
		if model := cfg.resolveSelection(&large, largeModelSelected); model == nil {
			large = defaultLarge
			result.LargeFallback = true
		} else {
			applySelectionParams(&large, largeModelSelected, model)
		}
	}
	if smallModelSelected, ok := cfg.Models[SelectedModelTypeSmall]; ok {
		if model := cfg.resolveSelection(&small, smallModelSelected); model == nil {
			small = defaultSmall
			result.SmallFallback = true
		} else {
			applySelectionParams(&small, smallModelSelected, model)
		}
	}

	// When small isn't explicitly configured and the provider isn't a
	// known built-in, use the large model as the small model. This
	// prevents two different models from being requested concurrently
	// for local/openai-compat providers.
	if _, ok := cfg.Models[SelectedModelTypeSmall]; !ok && KnownProviderByID(knownProviders, small.Provider) == nil {
		slog.Warn("Using large model as small model for unknown provider", "provider", large.Provider, "model", large.Model)
		small = large
	}

	result.Large = large
	result.Small = small
	return result, nil
}

// lookupConfigs searches config files starting at cwd and walking up
// through the current project. The upward walk stops at the git
// working tree root when one can be detected, otherwise at cwd itself,
// so an unrelated harness.yaml placed above the project is never picked
// up. Global user-level config locations are always included
// regardless of the boundary.
func lookupConfigs(cwd string) []string {
	// Prepend the system config, the hand-written user config, and the
	// machine-owned state file. Missing files are skipped when loaded.
	configPaths := []string{
		systemConfigPath,
		GlobalConfig(),
		GlobalConfigData(),
	}

	// Ordered high-to-low priority within a directory. LookupBounded returns
	// matches in this order, and the later reverse + merge make the earliest
	// listed name win on conflict: the hidden .harness.yaml beats the visible
	// harness.yaml, and .yaml beats .yml for either spelling.
	configNames := []string{
		"." + appName + yamlExt,
		"." + appName + ymlExt,
		appName + yamlExt,
		appName + ymlExt,
	}

	foundConfigs, err := fsext.LookupBounded(cwd, projectBoundary(cwd), configNames...)
	if err != nil {
		// returns at least default configs
		return configPaths
	}

	// reverse order so last config has more priority
	slices.Reverse(foundConfigs)

	return append(configPaths, foundConfigs...)
}

func loadFromConfigPaths(_ context.Context, configPaths []string) (*Config, []string, error) {
	var configs [][]byte
	var loaded []string

	// Track the directories that hold more than one config file, along with
	// the top-level keys each defines, so overlapping settings can be
	// reported. Two spellings in one directory (harness.yaml next to
	// .harness.yaml) is legal but rarely intended.
	dirKeys := make(map[string]map[string]bool)
	dirFiles := make(map[string][]string)

	for _, path := range configPaths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, fmt.Errorf("failed to open config file %s: %w", path, err)
		}

		jsonBytes, err := decodeConfig(data)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid YAML in config file %s: %w", path, err)
		}
		if len(jsonBytes) == 0 {
			continue
		}

		dir := filepath.Dir(path)
		reportConflicts(dirKeys, dirFiles, dir, path, jsonBytes)
		configs = append(configs, jsonBytes)
		loaded = append(loaded, path)
	}

	cfg, err := loadFromBytes(configs)
	if err != nil {
		return nil, nil, err
	}
	return cfg, loaded, nil
}

// reportConflicts records the top-level keys path contributes and warns
// when an earlier config from the same directory already defined one of
// them. Later paths win the merge, so the warning names what is being
// overridden rather than failing the load.
func reportConflicts(dirKeys map[string]map[string]bool, dirFiles map[string][]string, dir, path string, data []byte) {
	keys := dirKeys[dir]
	if keys == nil {
		keys = make(map[string]bool)
		dirKeys[dir] = keys
	}

	var conflicts []string
	gjson.ParseBytes(data).ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		if keys[name] {
			conflicts = append(conflicts, name)
		}
		keys[name] = true
		return true
	})

	if len(conflicts) > 0 {
		slices.Sort(conflicts)
		slog.Warn("Found more than one config file in the same directory; merging with the later file taking precedence",
			"dir", dir,
			"file", filepath.Base(path),
			"other_files", strings.Join(dirFiles[dir], ", "),
			"conflicting_keys", strings.Join(conflicts, ", "))
	}
	dirFiles[dir] = append(dirFiles[dir], filepath.Base(path))
}

func loadFromBytes(configs [][]byte) (*Config, error) {
	if len(configs) == 0 {
		return &Config{}, nil
	}

	data, err := jsons.Merge(configs)
	if err != nil {
		return nil, err
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func hasAWSCredentials(env env.Env) bool {
	if env.Get("AWS_BEARER_TOKEN_BEDROCK") != "" {
		return true
	}

	if env.Get("AWS_ACCESS_KEY_ID") != "" && env.Get("AWS_SECRET_ACCESS_KEY") != "" {
		return true
	}

	if env.Get("AWS_PROFILE") != "" || env.Get("AWS_DEFAULT_PROFILE") != "" {
		return true
	}

	if env.Get("AWS_REGION") != "" || env.Get("AWS_DEFAULT_REGION") != "" {
		return true
	}

	if env.Get("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
		env.Get("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" {
		return true
	}

	// File-based credential discovery requires filesystem stats, so do it
	// last and skip it under test. Checking testing.Testing() before the
	// os.Stat call (rather than after, in the && tail) ensures the syscall
	// is never issued during tests, where it otherwise ran unconditionally
	// and only had its result discarded.
	if testing.Testing() {
		return false
	}
	if _, err := os.Stat(filepath.Join(home.Dir(), ".aws/credentials")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(home.Dir(), ".aws/login")); err == nil {
		return true
	}

	return false
}

// GlobalConfig returns the hand-written user configuration file path:
// $XDG_CONFIG_HOME/harness/config.yaml. Harness reads this file and never
// writes to it, so comments and layout in it are safe.
func GlobalConfig() string {
	if harnessGlobal := os.Getenv("HARNESS_GLOBAL_CONFIG"); harnessGlobal != "" {
		return filepath.Join(harnessGlobal, userConfigFile)
	}
	return filepath.Join(home.Config(), appName, userConfigFile)
}

// GlobalCacheDir returns the path to the global cache directory for the
// application.
func GlobalCacheDir() string {
	if harnessCache := os.Getenv("HARNESS_CACHE_DIR"); harnessCache != "" {
		return harnessCache
	}
	if xdgCacheHome := os.Getenv("XDG_CACHE_HOME"); xdgCacheHome != "" {
		return filepath.Join(xdgCacheHome, appName)
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(home.AppData(), appName, "cache")
	}
	return filepath.Join(home.Dir(), ".cache", appName)
}

// ProjectConfigs returns list of current project configs paths.
func ProjectConfigs(cwd string) []string {
	return lookupConfigs(cwd)
}

// GlobalConfigData returns the path to the main data directory for the application.
// this config is used when the app overrides configurations instead of updating the global config.
func GlobalConfigData() string {
	return filepath.Join(home.DataDir(), stateConfigFile)
}

// GlobalWorkspaceDir returns the path to the global server workspace
// directory. This directory acts as a meta-workspace for the server
// process, giving it a real workingDir so that config loading, scoped
// writes, and provider resolution behave identically to project
// workspaces.
func GlobalWorkspaceDir() string {
	return filepath.Dir(GlobalConfigData())
}

func assignIfNil[T any](ptr **T, val T) {
	if *ptr == nil {
		*ptr = &val
	}
}

// worktreeRoot returns the absolute path of the git working tree root for
// dir, or the empty string if dir is not inside a working tree (bare
// repositories, missing git binary, plain directories, or any other
// failure mode). Linked worktrees and submodules each report their own
// top-level, which is what callers want when bounding lookups.
// worktreeRootCache memoizes the git worktree root per directory. The root
// is stable for the life of the process, so we avoid re-shelling out to
// "git rev-parse" on every config reload. Keyed by the requested dir; the
// value is the resolved root ("" when dir is not in a git worktree).
var worktreeRootCache sync.Map // map[string]string

func worktreeRoot(dir string) string {
	if cached, ok := worktreeRootCache.Load(dir); ok {
		return cached.(string)
	}
	root := computeWorktreeRoot(dir)
	worktreeRootCache.Store(dir, root)
	return root
}

func computeWorktreeRoot(dir string) string {
	cmd := exec.CommandContext(
		context.Background(),
		"git", "rev-parse", "--show-toplevel",
	)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	return abs
}

// projectBoundary returns the directory at which an upward configuration
// search rooted at dir should stop. It is the git working tree root when
// one can be detected, otherwise dir itself. Returning dir as a
// fallback keeps Harness from silently adopting state files placed above
// the current project.
func projectBoundary(dir string) string {
	if root := worktreeRoot(dir); root != "" {
		return root
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// GlobalSkillsDirs returns the default directories for Agent Skills.
// Skills in these directories are auto-discovered and their files can be read
// without permission prompts.
func GlobalSkillsDirs() []string {
	// Per the Agent Skills spec, also scan ~/.agents/skills.
	return globalDirs("HARNESS_SKILLS_DIR", "skills", ".agents", ".claude")
}

// globalDirs returns the global directories for one kind of definition
// (skills, subagents, extensions). A non-empty envVar overrides the list
// entirely. Otherwise it is the harness and shared agents directories
// under the config home, then name under each of homeDirs in the home
// directory, then on Windows the same pair under app data, kept mostly
// for backwards compatibility.
func globalDirs(envVar, name string, homeDirs ...string) []string {
	if dir := os.Getenv(envVar); dir != "" {
		return []string{dir}
	}

	paths := []string{
		filepath.Join(home.Config(), appName, name),
		filepath.Join(home.Config(), "agents", name),
	}
	for _, dir := range homeDirs {
		paths = append(paths, filepath.Join(home.Dir(), dir, name))
	}
	if runtime.GOOS == "windows" {
		appData := home.AppData()
		paths = append(paths,
			filepath.Join(appData, appName, name),
			filepath.Join(appData, "agents", name),
		)
	}
	return paths
}

// projectDirs returns subdirs under the git working tree root and then
// under workingDir, so monorepo-level definitions are found from inside a
// subdirectory. Discovery keeps the last definition of a name, so listing
// the working directory last lets a local definition override a
// repository-root one with the same name.
func projectDirs(workingDir string, subdirs []string) []string {
	bases := []string{workingDir}
	if root := worktreeRoot(workingDir); root != "" && root != workingDir {
		bases = []string{root, workingDir}
	}
	dirs := make([]string, 0, len(subdirs)*len(bases))
	for _, base := range bases {
		for _, sub := range subdirs {
			dirs = append(dirs, filepath.Join(base, sub))
		}
	}
	return dirs
}

// projectSkillSubdirs lists the conventional subdirectories where
// project-level skills are discovered. Shared across working-dir and
// git-root lookups to prevent drift when a new convention is added.
var projectSkillSubdirs = []string{
	".agents/skills",
	".harness/skills",
	".claude/skills",
	".cursor/skills",
}

// ProjectSkillsDir returns the default project directories in which Harness
// looks for skills: the git working tree root first, then the working
// directory, so a local skill overrides a monorepo-level one with the same
// name (skills.Deduplicate keeps the last occurrence).
func ProjectSkillsDir(workingDir string) []string {
	return projectDirs(workingDir, projectSkillSubdirs)
}

// GlobalSubagentsDirs returns the default global directories for subagent
// definitions. The HARNESS_SUBAGENTS_DIR environment variable, when set to a
// non-empty value, overrides the default list entirely.
func GlobalSubagentsDirs() []string {
	return globalDirs("HARNESS_SUBAGENTS_DIR", "subagents", ".agents")
}

// projectSubagentSubdirs lists the conventional subdirectories where
// project-level subagents are discovered. Shared across working-dir and
// git-root lookups to prevent drift when a new convention is added.
var projectSubagentSubdirs = []string{
	".agents/subagents",
	".harness/subagents",
}

// ProjectSubagentsDir returns the default project directories in which
// Harness looks for subagent definitions, repository root first; see
// projectDirs.
func ProjectSubagentsDir(workingDir string) []string {
	return projectDirs(workingDir, projectSubagentSubdirs)
}

// ValidateHooks normalizes event names and checks that every configured
// hook has a command and a syntactically valid matcher regex. Matcher
// compilation used for matching is owned by hooks.Runner; this function
// only validates up front so the user sees config errors at load time
// rather than on the first tool call.
func (c *Config) ValidateHooks() error {
	// Normalize event name keys. Visit them sorted so that, when both a
	// canonical and a variant spelling are present, their hooks merge in
	// the same order on every load.
	for _, event := range slices.Sorted(maps.Keys(c.Hooks)) {
		canonical, ok := CanonicalHookEvent(event)
		if !ok {
			return fmt.Errorf("hook event %q is not supported", event)
		}
		if canonical != event {
			c.Hooks[canonical] = append(c.Hooks[canonical], c.Hooks[event]...)
			delete(c.Hooks, event)
		}
	}

	for event, eventHooks := range c.Hooks {
		for i, h := range eventHooks {
			if h.Command == "" {
				return fmt.Errorf("hook %s[%d]: command is required", event, i)
			}
			if h.Matcher == "" {
				continue
			}
			if _, err := CompileMatcher(h.Matcher); err != nil {
				return fmt.Errorf("hook %s[%d]: invalid matcher regex %q: %w", event, i, h.Matcher, err)
			}
		}
	}
	return nil
}

// GlobalExtensionsDirs returns the default global directories for Lua
// extensions. The HARNESS_EXTENSIONS_DIR environment variable, when set
// to a non-empty value, overrides the default list entirely.
func GlobalExtensionsDirs() []string {
	return globalDirs("HARNESS_EXTENSIONS_DIR", "extensions")
}

// projectExtensionSubdirs lists the conventional subdirectories where
// project-level extensions are discovered.
var projectExtensionSubdirs = []string{
	".harness/extensions",
	".agents/extensions",
}

// ProjectExtensionsDir returns the default project directories in which
// Harness looks for extensions, repository root first; see projectDirs.
func ProjectExtensionsDir(workingDir string) []string {
	return projectDirs(workingDir, projectExtensionSubdirs)
}
