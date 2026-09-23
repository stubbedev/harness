package dialog

import (
	"cmp"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
)

const (
	// ConnectID is the identifier for the provider connection dialog.
	ConnectID              ID = "connect"
	connectDialogMinHeight    = 8
	connectDialogMaxHeight    = 20
)

// Connect lists the catalog providers that have no credentials yet. The
// models dialog only offers providers that can actually serve a request,
// so adding a new one is a separate step: pick a provider here and the
// authentication dialog takes over.
type Connect struct {
	com   *common.Common
	list  *list.FilterableList
	input textinput.Model

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*Connect)(nil)

// NewConnect creates the provider connection dialog.
func NewConnect(com *common.Common) (*Connect, error) {
	c := &Connect{com: com}

	c.list = list.NewFilterableList()
	c.list.Focus()

	c.input = textinput.New()
	c.input.SetVirtualCursor(false)
	c.input.Prompt = "❯ "
	c.input.Placeholder = "Find a provider to connect"
	c.input.SetStyles(com.Styles.TextInput)
	c.input.Focus()

	km := dialogKeys()
	c.keyMap.Select = keys.WithDesc(km.Select, "connect")
	c.keyMap.Next = km.Next
	c.keyMap.Previous = km.Previous
	c.keyMap.UpDown = km.UpDown
	c.keyMap.Close = km.Close

	// A stale catalog must not keep the dialog from opening: whatever
	// providers were known last still let the user connect one.
	providers, err := config.Providers(com.Config())
	if err != nil {
		if len(providers) == 0 {
			return nil, fmt.Errorf("failed to get providers: %w", err)
		}
		slog.Warn("Listing the previously known providers", "error", err)
	}

	c.setItems(providers)
	return c, nil
}

// ID implements Dialog.
func (c *Connect) ID() ID {
	return ConnectID
}

// HandleMsg implements Dialog.
func (c *Connect) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, c.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, c.keyMap.Previous):
			c.list.Focus()
			if c.list.IsSelectedFirst() {
				c.list.SelectLast()
				c.list.ScrollToBottom()
			} else {
				c.list.SelectPrev()
				c.list.ScrollToSelected()
			}
		case key.Matches(msg, c.keyMap.Next):
			c.list.Focus()
			if c.list.IsSelectedLast() {
				c.list.SelectFirst()
				c.list.ScrollToTop()
			} else {
				c.list.SelectNext()
				c.list.ScrollToSelected()
			}
		case key.Matches(msg, c.keyMap.Select):
			provider, ok := c.selectedItem()
			if !ok {
				break
			}
			// The provider has no credentials, so this selection lands
			// in the authentication dialog rather than switching the
			// model outright. It carries the provider's default large
			// model, which is what gets selected once the key verifies.
			return ActionSelectModel{
				Provider:  provider,
				Model:     defaultSelectedModel(provider),
				ModelType: config.SelectedModelTypeLarge,
			}
		default:
			cmd, _ := filterInput(&c.input, msg, applyListFilter(c.list))
			return ActionCmd{cmd}
		}
	}
	return nil
}

// Cursor returns the cursor position relative to the dialog.
func (c *Connect) Cursor() *tea.Cursor {
	return InputCursor(c.com.Styles, c.input.Cursor())
}

// Draw implements [Dialog].
func (c *Connect) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	st := c.com.Styles
	width := DialogWidth(st, area)
	innerWidth := DialogInnerWidth(st, width)

	c.input.SetWidth(dialogInputTextWidth(st, c.input, innerWidth))

	heightOffset := dialogChromeHeight(st, st.Dialog.HelpView)
	desiredHeight := heightOffset + c.list.TotalHeight()
	maxAvailable := DialogHeightCeiling(st, area, connectDialogMaxHeight)
	height := max(connectDialogMinHeight, min(connectDialogMaxHeight, desiredHeight, maxAvailable))

	listHeight, listTotalHeight, _ := sizeDialogList(st, c.list, innerWidth, height, true)

	rc := NewRenderContext(st, width)
	rc.Title = "Connect Provider"
	rc.AddInput(c.input.View())

	listView := st.Dialog.List.Height(c.list.Height()).Render(c.list.Render())
	listView = joinScrollbar(st, listView, listHeight, listTotalHeight, listHeight, c.list.Offset())
	rc.AddPart(listView)

	view := rc.Render()

	cur := DialogCursor(st, view, c.input.Cursor())
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (c *Connect) ShortHelp() []key.Binding {
	return []key.Binding{
		c.keyMap.UpDown,
		c.keyMap.Select,
		c.keyMap.Close,
	}
}

// FullHelp implements [help.KeyMap].
func (c *Connect) FullHelp() [][]key.Binding {
	return [][]key.Binding{{
		c.keyMap.Select,
		c.keyMap.Next,
		c.keyMap.Previous,
		c.keyMap.Close,
	}}
}

// selectedItem resolves the selected row to its provider through the
// shared picker item, or reports false when the list is empty.
func (c *Connect) selectedItem() (catalog.Provider, bool) {
	item, ok := c.list.SelectedItem().(PickerItem)
	if !ok || item == nil {
		return catalog.Provider{}, false
	}
	provider, ok := item.Value().(catalog.Provider)
	return provider, ok
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
	_, configured := cfg.Providers.Get(string(provider.ID))
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
		Provider:        string(provider.ID),
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
