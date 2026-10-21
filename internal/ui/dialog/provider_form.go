package dialog

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/ui/util"
)

const ProviderFormID ID = "provider_form"

const providerFormMaxHeight = 20

type providerFormStatus uint8

const (
	providerFormIdle providerFormStatus = iota
	providerFormTesting
	providerFormVerified
	providerFormFailed
	providerFormSaving
)

type providerTestedMsg struct {
	id     catalog.InferenceProvider
	update config.ProviderUpdate
	save   bool
	err    error
}

type ProviderForm struct {
	com     *common.Common
	target  ProviderTarget
	fields  []providerField
	values  []string
	focused int

	list    *list.FilterableList
	input   textinput.Model
	spinner spinner.Model
	status  providerFormStatus
	err     error

	keyMap struct {
		Next     key.Binding
		Previous key.Binding
		Confirm  key.Binding
		Test     key.Binding
		Save     key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*ProviderForm)(nil)

func NewProviderForm(com *common.Common, target ProviderTarget) *ProviderForm {
	f := &ProviderForm{com: com, target: target, fields: target.fields()}
	f.values = make([]string, len(f.fields))
	for i, field := range f.fields {
		f.values[i] = target.display(field)
	}

	f.list = list.NewFilterableList()
	f.list.Focus()

	f.input = textinput.New()
	f.input.SetVirtualCursor(false)
	f.input.SetStyles(com.Styles.TextInput)
	f.input.Focus()

	f.spinner = spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(com.Styles.Dialog.APIKey.Spinner))

	km := dialogKeys()
	f.keyMap.Next = km.Arguments.Next
	f.keyMap.Previous = km.Arguments.Previous
	f.keyMap.Confirm = keys.WithDesc(km.Select, "next / save")
	f.keyMap.Test = km.Providers.Test
	f.keyMap.Save = km.Providers.Save
	f.keyMap.Close = km.Close

	f.focus(0)
	return f
}

func (f *ProviderForm) ID() ID {
	return ProviderFormID
}

func (f *ProviderForm) focus(index int) {
	n := len(f.fields)
	f.focused = ((index % n) + n) % n
	field := f.fields[f.focused]
	f.input.Prompt = field.label + " ❯ "
	f.input.Placeholder = f.target.placeholder(field)
	f.input.SetValue(f.values[f.focused])
	f.input.CursorEnd()
	f.refreshRows()
}

func (f *ProviderForm) refreshRows() {
	items := make([]list.FilterableItem, len(f.fields))
	for i, field := range f.fields {
		items[i] = NewPickerItem(f.com.Styles, i, field.label, f.rowValue(i))
	}
	f.list.SetItems(items...)
	f.list.SetSelected(f.focused)
}

func (f *ProviderForm) rowValue(i int) string {
	field := f.fields[i]
	value := f.values[i]
	switch {
	case value == "" && field.secret && f.target.rawValue(field.key) != "":
		return maskSecret(f.target.rawValue(field.key))
	case value == "":
		return "default"
	case field.secret && !isReference(value):
		return maskSecret(value)
	default:
		return value
	}
}

func (f *ProviderForm) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case providerTestedMsg:
		return f.tested(msg)
	case spinner.TickMsg:
		if f.status != providerFormTesting && f.status != providerFormSaving {
			return nil
		}
		var cmd tea.Cmd
		f.spinner, cmd = f.spinner.Update(msg)
		return ActionCmd{cmd}
	case tea.PasteMsg:
		return f.edit(msg)
	case tea.KeyPressMsg:
		if f.status == providerFormTesting {
			return nil
		}
		switch {
		case key.Matches(msg, f.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, f.keyMap.Next):
			f.focus(f.focused + 1)
		case key.Matches(msg, f.keyMap.Previous):
			f.focus(f.focused - 1)
		case key.Matches(msg, f.keyMap.Test):
			return f.test(false)
		case key.Matches(msg, f.keyMap.Save):
			return f.test(true)
		case key.Matches(msg, f.keyMap.Confirm):
			if f.focused == len(f.fields)-1 {
				return f.test(true)
			}
			f.focus(f.focused + 1)
		default:
			return f.edit(msg)
		}
	}
	return nil
}

func (f *ProviderForm) edit(msg tea.Msg) Action {
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	if v := f.input.Value(); v != f.values[f.focused] {
		f.values[f.focused] = v
		f.status = providerFormIdle
		f.err = nil
		f.refreshRows()
	}
	return ActionCmd{cmd}
}

func (f *ProviderForm) test(save bool) Action {
	id, update, err := f.target.buildUpdate(f.fields, f.values)
	if err != nil {
		f.status = providerFormFailed
		f.err = err
		return nil
	}
	if save && update.Empty() {
		return ActionCmd{util.ReportInfo("Nothing changed")}
	}
	cand, err := f.target.candidate(id, update)
	if err != nil {
		f.status = providerFormFailed
		f.err = err
		return nil
	}
	f.status = providerFormTesting
	f.err = nil
	return ActionCmd{tea.Batch(f.spinner.Tick, testProviderCmd(f.com.Workspace.Resolver(), cand, update, save))}
}

func testProviderCmd(resolver config.VariableResolver, cand config.ProviderConfig, update config.ProviderUpdate, save bool) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		err := cand.TestConnection(context.Background(), resolver)
		if wait := 500*time.Millisecond - time.Since(start); wait > 0 {
			time.Sleep(wait)
		}
		return providerTestedMsg{id: cand.ID, update: update, save: save, err: err}
	}
}

func (f *ProviderForm) tested(msg providerTestedMsg) Action {
	if msg.err != nil {
		f.status = providerFormFailed
		f.err = msg.err
		return nil
	}
	if !msg.save {
		f.status = providerFormVerified
		return nil
	}
	f.status = providerFormSaving
	return ActionSaveProvider{ProviderID: msg.id, Update: msg.update}
}

func (f *ProviderForm) SaveFailed(err error) {
	f.status = providerFormFailed
	f.err = err
}

func (f *ProviderForm) statusView(width int) string {
	t := f.com.Styles
	switch f.status {
	case providerFormTesting:
		return f.spinner.View() + t.Dialog.SecondaryText.Render(" Testing connection...")
	case providerFormSaving:
		return f.spinner.View() + t.Dialog.SecondaryText.Render(" Connection verified, saving...")
	case providerFormVerified:
		return t.Dialog.OAuth.Success.Render(styles.CheckIcon + " Connection verified")
	case providerFormFailed:
		return t.Dialog.OAuth.ErrorText.Width(width).Render(f.err.Error())
	default:
		return t.Dialog.SecondaryText.Width(width).Render(fmt.Sprintf(
			"Values may reference $VAR or $(command). Saved to %s.", config.GlobalConfigData()))
	}
}

func (f *ProviderForm) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := f.com.Styles
	return drawPicker(scr, area, t, &f.input, f.list, pickerLayout{
		title:     f.target.title(),
		header:    f.statusView(DialogInnerWidth(t, DialogWidth(t, area))),
		maxHeight: providerFormMaxHeight,
	})
}

func (f *ProviderForm) ShortHelp() []key.Binding {
	if f.status == providerFormTesting {
		return nil
	}
	return []key.Binding{f.keyMap.Next, f.keyMap.Test, f.keyMap.Save, f.keyMap.Close}
}

func (f *ProviderForm) FullHelp() [][]key.Binding {
	return [][]key.Binding{{f.keyMap.Next, f.keyMap.Previous, f.keyMap.Confirm}, {f.keyMap.Test, f.keyMap.Save, f.keyMap.Close}}
}
