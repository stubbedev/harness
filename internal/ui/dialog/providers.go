package dialog

import (
	"cmp"
	"fmt"
	"log/slog"
	"slices"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
)

const ProvidersID ID = "providers"

const (
	providersDialogMinHeight = 8
	providersDialogMaxHeight = 20
)

type providerEntry struct {
	provider   catalog.Provider
	configured bool
	rejected   bool
	addCustom  bool
}

type Providers struct {
	*simplePicker[providerEntry]

	removing *providerEntry
	remove   struct {
		Remove  key.Binding
		Confirm key.Binding
		Cancel  key.Binding
	}
}

var _ Dialog = (*Providers)(nil)

func NewProviders(com *common.Common, rejected map[catalog.InferenceProvider]bool) (*Providers, error) {
	providers, err := config.Providers(com.Config())
	if err != nil {
		if len(providers) == 0 {
			return nil, fmt.Errorf("failed to get providers: %w", err)
		}
		slog.Warn("Listing the previously known providers", "error", err)
	}
	return newProviders(com, providers, rejected), nil
}

func newProviders(com *common.Common, catalogProviders []catalog.Provider, rejected map[catalog.InferenceProvider]bool) *Providers {
	p := &Providers{simplePicker: newSimplePicker(com, ProvidersID, "Configure Provider", providersDialogMinHeight, providersDialogMaxHeight,
		selectProviderEntry, carryCmd)}
	p.input.Prompt = "❯ "
	p.input.Placeholder = "Find a provider to configure"
	p.keyMap.Select = keys.WithDesc(p.keyMap.Select, "configure")
	km := dialogKeys()
	p.remove.Remove = km.Providers.Remove
	p.remove.Confirm = km.Providers.ConfirmRemove
	p.remove.Cancel = km.Providers.CancelRemove
	p.setItems(providerEntries(com.Config(), catalogProviders, rejected))
	return p
}

func selectProviderEntry(entry providerEntry) Action {
	return ActionConfigureProvider{ProviderID: entry.provider.ID}
}

func providerEntries(cfg *config.Config, catalogProviders []catalog.Provider, rejected map[catalog.InferenceProvider]bool) []providerEntry {
	var configured, unconfigured []providerEntry
	known := make(map[catalog.InferenceProvider]bool, len(catalogProviders))
	for _, provider := range catalogProviders {
		known[provider.ID] = true
		if len(provider.Models) == 0 {
			continue
		}
		entry := providerEntry{provider: provider, rejected: rejected[provider.ID]}
		if cfg != nil {
			_, entry.configured = cfg.Providers.Get(provider.ID)
		}
		if entry.configured {
			configured = append(configured, entry)
		} else {
			unconfigured = append(unconfigured, entry)
		}
	}
	if cfg != nil {
		var custom []providerEntry
		for id, pc := range cfg.Providers.Seq2() {
			if known[id] {
				continue
			}
			provider := pc.ToProvider()
			provider.ID = id
			provider.Type = pc.Type
			custom = append(custom, providerEntry{provider: provider, configured: true, rejected: rejected[id]})
		}
		slices.SortFunc(custom, func(a, b providerEntry) int {
			return cmp.Compare(providerEntryName(a), providerEntryName(b))
		})
		configured = append(configured, custom...)
	}
	entries := []providerEntry{{addCustom: true}}
	entries = append(entries, configured...)
	return append(entries, unconfigured...)
}

func (p *Providers) setItems(entries []providerEntry) {
	items := make([]list.FilterableItem, 0, len(entries))
	for _, entry := range entries {
		name := providerEntryName(entry)
		items = append(items, NewPickerItem(p.com.Styles, entry, name, providerEntryInfo(entry), name+" "+string(entry.provider.ID)))
	}
	p.list.SetItems(items...)
	p.list.SetSelected(0)
	p.list.ScrollToTop()
}

func providerEntryName(entry providerEntry) string {
	if entry.addCustom {
		return "Add custom provider"
	}
	return cmp.Or(entry.provider.Name, string(entry.provider.ID))
}

func providerEntryInfo(entry providerEntry) string {
	switch {
	case entry.addCustom:
		return "openai or anthropic compatible"
	case entry.rejected:
		return "credentials rejected"
	case entry.configured:
		return "connected"
	}
	plural := "s"
	if len(entry.provider.Models) == 1 {
		plural = ""
	}
	return fmt.Sprintf("%s · %d model%s", providerCredentialKind(entry.provider), len(entry.provider.Models), plural)
}

func providerCredentialKind(provider catalog.Provider) string {
	switch {
	case provider.ID == catalog.InferenceProviderCopilot:
		return "sign in"
	case provider.Type == catalog.TypeVertexAI:
		return "google cloud"
	case provider.Type == catalog.TypeBedrock:
		return "aws"
	case provider.Type == catalog.TypeAzure:
		return "endpoint & key"
	default:
		return "api key"
	}
}

func (p *Providers) selectedConfigured() (providerEntry, bool) {
	entry, ok := p.selected()
	return entry, ok && entry.configured
}

func (p *Providers) HandleMsg(msg tea.Msg) Action {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return p.simplePicker.HandleMsg(msg)
	}
	if p.removing != nil {
		switch {
		case key.Matches(press, p.remove.Confirm):
			id := p.removing.provider.ID
			p.removing = nil
			return ActionRemoveProvider{ProviderID: id}
		case key.Matches(press, p.remove.Cancel):
			p.removing = nil
		}
		return nil
	}
	if key.Matches(press, p.remove.Remove) {
		if entry, ok := p.selectedConfigured(); ok {
			p.removing = &entry
		}
		return nil
	}
	return p.simplePicker.HandleMsg(msg)
}

func (p *Providers) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	header := ""
	if p.removing != nil {
		t := p.com.Styles
		header = t.Dialog.SecondaryText.Width(DialogInnerWidth(t, DialogWidth(t, area))).Render(fmt.Sprintf(
			"Disconnect %s? Its settings are removed from %s.",
			providerEntryName(*p.removing), config.GlobalConfigData()))
	}
	return drawPicker(scr, area, p.com.Styles, &p.input, p.list, pickerLayout{
		title:     p.title,
		header:    header,
		minHeight: p.minHeight,
		maxHeight: p.maxHeight,
	})
}

func (p *Providers) ShortHelp() []key.Binding {
	if p.removing != nil {
		return []key.Binding{p.remove.Confirm, p.remove.Cancel}
	}
	help := []key.Binding{p.keyMap.UpDown, p.keyMap.Select}
	if _, ok := p.selectedConfigured(); ok {
		help = append(help, p.remove.Remove)
	}
	return append(help, p.keyMap.Close)
}

func (p *Providers) FullHelp() [][]key.Binding {
	return [][]key.Binding{p.ShortHelp()}
}
