package dialog

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// ModelGroup represents a group of model items.
type ModelGroup struct {
	*list.Versioned
	Title      string
	Items      []*ModelItem
	configured bool
	t          *styles.Styles
}

// NewModelGroup creates a new ModelGroup.
func NewModelGroup(t *styles.Styles, title string, configured bool, items ...*ModelItem) ModelGroup {
	return ModelGroup{
		Versioned:  list.NewVersioned(),
		Title:      title,
		Items:      items,
		configured: configured,
		t:          t,
	}
}

// Finished implements list.Item. Model groups are immutable headers.
func (m *ModelGroup) Finished() bool {
	return true
}

// AppendItems appends [ModelItem]s to the group.
func (m *ModelGroup) AppendItems(items ...*ModelItem) {
	m.Items = append(m.Items, items...)
}

// Render implements [list.Item].
func (m *ModelGroup) Render(width int) string {
	var configured string
	if m.configured {
		configuredIcon := m.t.ToolCallSuccess.Render()
		configuredText := m.t.Dialog.Models.ConfiguredText.Render("Configured")
		configured = configuredIcon + " " + configuredText
	}

	title := " " + m.Title + " "
	// Keep the "Configured" badge only when the full title fits beside it
	// (plus a separator). Otherwise drop it and let the title use the whole
	// width, rather than truncating the title to reserve room for a badge
	// that common.Section would then drop anyway, leaving dead space.
	if configured != "" && lipgloss.Width(title)+lipgloss.Width(configured)+3 > width {
		configured = ""
	}
	if configured == "" {
		title = ansi.Truncate(title, max(0, width-1), "…")
	}

	return common.Section(m.t, title, width, configured)
}

// ModelItem represents a list item for a model type.
type ModelItem struct {
	*list.Versioned

	prov      catalog.Provider
	model     catalog.Model
	modelType ModelType

	cache        map[int]string
	t            *styles.Styles
	m            fuzzy.Match
	focused      bool
	showProvider bool
}

// Finished implements list.Item. Model items are render-stable
// outside of explicit SetFocused / SetMatch.
func (m *ModelItem) Finished() bool {
	return true
}

// SelectedModel returns this model item as a [config.SelectedModel] instance.
func (m *ModelItem) SelectedModel() config.SelectedModel {
	return config.SelectedModel{
		Model:           m.model.ID,
		Provider:        string(m.prov.ID),
		ReasoningEffort: catalog.DefaultReasoningLevel(m.model.ReasoningLevels),
		MaxTokens:       m.model.DefaultMaxTokens,
	}
}

// SelectedModelType returns the type of model represented by this item.
func (m *ModelItem) SelectedModelType() config.SelectedModelType {
	return m.modelType.Config()
}

var _ ListItem = &ModelItem{}

// NewModelItem creates a new ModelItem.
func NewModelItem(t *styles.Styles, prov catalog.Provider, model catalog.Model, typ ModelType, showProvider bool) *ModelItem {
	return &ModelItem{
		Versioned:    list.NewVersioned(),
		prov:         prov,
		model:        model,
		modelType:    typ,
		t:            t,
		cache:        make(map[int]string),
		showProvider: showProvider,
	}
}

// Filter implements ListItem.
func (m *ModelItem) Filter() string {
	return m.model.Name
}

// Value implements PickerItem: the selected model as a
// config.SelectedModel.
func (m *ModelItem) Value() any {
	return m.SelectedModel()
}

// Label implements PickerItem.
func (m *ModelItem) Label() string {
	return m.model.Name
}

// RightLabel implements PickerItem: the provider column.
func (m *ModelItem) RightLabel() string {
	if !m.showProvider {
		return ""
	}
	return string(m.prov.Name)
}

// ID implements ListItem.
func (m *ModelItem) ID() string {
	return modelKey(string(m.prov.ID), m.model.ID)
}

// Render implements ListItem, flowing through the one shared row
// path.
func (m *ModelItem) Render(width int) string {
	return renderItem(pickerItemStyles(m.t), m.Label(), m.RightLabel(), m.focused, width, m.cache, &m.m)
}

// SetFocused implements ListItem.
func (m *ModelItem) SetFocused(focused bool) {
	if m.focused == focused {
		return
	}
	m.cache = nil
	m.focused = focused
	if m.Versioned != nil {
		m.Bump()
	}
}

// SetMatch implements ListItem.
func (m *ModelItem) SetMatch(fm fuzzy.Match) {
	if sameFuzzyMatch(m.m, fm) {
		return
	}
	m.cache = nil
	m.m = fm
	if m.Versioned != nil {
		m.Bump()
	}
}
