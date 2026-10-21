package dialog

import (
	"cmp"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
)

// ConnectID is the identifier for the provider connection dialog.
const ConnectID ID = "connect"

const (
	connectDialogMinHeight = 8
	connectDialogMaxHeight = 20
)

// Connect lists the catalog providers that have no credentials yet. The
// models dialog only offers providers that can actually serve a request,
// so adding a new one is a separate step: pick a provider here and the
// authentication dialog takes over.
type Connect struct {
	*simplePicker[catalog.Provider]
}

var _ Dialog = (*Connect)(nil)

// NewConnect creates the provider connection dialog.
func NewConnect(com *common.Common) (*Connect, error) {
	// A stale catalog must not keep the dialog from opening: whatever
	// providers were known last still let the user connect one.
	providers, err := config.Providers(com.Config())
	if err != nil {
		if len(providers) == 0 {
			return nil, fmt.Errorf("failed to get providers: %w", err)
		}
		slog.Warn("Listing the previously known providers", "error", err)
	}
	return newConnect(com, providers), nil
}

func newConnect(com *common.Common, providers []catalog.Provider) *Connect {
	c := &Connect{simplePicker: newSimplePicker(com, ConnectID, "Connect Provider", connectDialogMinHeight, connectDialogMaxHeight,
		selectConnectProvider, carryCmd)}
	c.input.Prompt = "❯ "
	c.input.Placeholder = "Find a provider to connect"
	c.keyMap.Select = keys.WithDesc(c.keyMap.Select, "connect")
	c.setItems(providers)
	return c
}

func selectConnectProvider(provider catalog.Provider) Action {
	return ActionSelectModel{
		Provider:  provider,
		Model:     defaultSelectedModel(provider),
		ModelType: config.SelectedModelTypeLarge,
	}
}

// setItems fills the list with the providers that are still unconnected.
func (c *Connect) setItems(providers []catalog.Provider) {
	cfg := c.com.Config()
	items := make([]list.FilterableItem, 0, len(providers))
	for _, provider := range providers {
		if !connectable(cfg, provider) {
			continue
		}
		name := connectItemName(provider)
		// Both the display name and the provider ID match: the ID is
		// what ends up in the config file.
		items = append(items, NewPickerItem(
			c.com.Styles,
			provider,
			name,
			connectItemInfo(provider),
			name+" "+string(provider.ID),
		))
	}
	c.list.SetItems(items...)
	c.list.SetSelected(0)
	c.list.ScrollToTop()
}

// isConfigOnlyProvider reports whether a catalog provider can only be
// configured outside the TUI: the API-key dialog's connection test has no
// verification path for these provider types, so offering them would
// dead-end. Mirrors the type switch in config.TestConnection.
func isConfigOnlyProvider(provider catalog.Provider) bool {
	return provider.Type == catalog.TypeAzure || provider.Type == catalog.TypeVertexAI
}

// connectable reports whether a catalog provider belongs in this dialog: it
// must have models to offer, credentials the TUI can actually collect, and
// no configuration already (those live in the models dialog).
func connectable(cfg *config.Config, provider catalog.Provider) bool {
	if len(provider.Models) == 0 {
		return false
	}
	if isConfigOnlyProvider(provider) {
		return false
	}
	if cfg == nil {
		return true
	}
	_, configured := cfg.Providers.Get(provider.ID)
	return !configured
}

// defaultSelectedModel returns the model the provider leads with, used as
// the selection to apply once the provider is authenticated.
func defaultSelectedModel(provider catalog.Provider) config.SelectedModel {
	model := provider.Models[0]
	if idx := slices.IndexFunc(provider.Models, func(m catalog.Model) bool {
		return m.ID == provider.DefaultLargeModelID
	}); idx >= 0 {
		model = provider.Models[idx]
	}
	return config.SelectedModel{
		Model:           model.ID,
		Provider:        provider.ID,
		ReasoningEffort: catalog.DefaultReasoningLevel(model.ReasoningLevels),
		MaxTokens:       model.DefaultMaxTokens,
	}
}

// connectItemName is the provider's display name, falling back to its
// ID when the catalog carries none.
func connectItemName(provider catalog.Provider) string {
	return cmp.Or(provider.Name, string(provider.ID))
}

// connectItemInfo says what connecting costs the user: an OAuth
// handshake for the providers that have one, an API key for everyone
// else.
func connectItemInfo(provider catalog.Provider) string {
	if provider.ID == catalog.InferenceProviderCopilot {
		return "sign in"
	}
	plural := "s"
	if len(provider.Models) == 1 {
		plural = ""
	}
	return strings.TrimSpace(fmt.Sprintf("api key · %d model%s", len(provider.Models), plural))
}
