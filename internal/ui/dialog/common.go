package dialog

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// filterableList is the filtering surface every filterable dialog
// list satisfies; list.FilterableList and the models list both route
// ClampScroll bounds a scroll offset to [0, max(0, totalLines-viewport)].
// Single source for the question dialogs, which re-derive the same
// arithmetic per dialog type before their own keep-visible extras.
func ClampScroll(offset, totalLines, viewport int) int {
	return min(max(0, offset), max(0, totalLines-viewport))
}

// dialogInputTextWidth returns the text-area width for a dialog input so
// that the input frame, its prompt (e.g. "❯ "), the text, and a trailing
// cursor cell all fit within contentWidth. The prompt is rendered outside
// the text area, so it must be subtracted or long values wrap past the
// dialog border.
func dialogInputTextWidth(t *styles.Styles, input textinput.Model, contentWidth int) int {
	const cursorPadding = 1
	return max(0, contentWidth-
		t.Dialog.InputPrompt.GetHorizontalFrameSize()-
		lipgloss.Width(input.Prompt)-
		cursorPadding)
}

// sizer is satisfied by any list type that can report its total content
// height and accept a viewport size. Both *list.List and *list.FilterableList
// (and wrappers embedding them) implement this.
type sizer interface {
	TotalHeight() int
	SetSize(width, height int)
}

// dialogChromeHeight returns the rendered height of an input dialog's
// chrome: the title, the input row, the separator rule between input
// and content, the placement frame, and any extra sections (e.g. the
// help row). The single source for every dialog that sizes its list
// against the available height, so adding a chrome row resizes them
// all together instead of drifting.
func dialogChromeHeight(t *styles.Styles, extra ...lipgloss.Style) int {
	height := dialogTitleHeight(t) + dialogInputHeight(t) +
		ActiveFrame(t).GetVerticalFrameSize()
	for _, section := range extra {
		height += section.GetVerticalFrameSize()
	}
	return height
}

// dialogTitleHeight returns the rendered height of the title row.
func dialogTitleHeight(t *styles.Styles) int {
	return t.Dialog.Title.GetVerticalFrameSize() + titleContentHeight
}

// dialogInputHeight returns the rendered height of the input row and
// the separator rule RenderContext.Render draws beneath (or above) it.
// Input-less dialogs render neither and must not reserve them.
func dialogInputHeight(t *styles.Styles) int {
	return ActiveInput(t).GetVerticalFrameSize() + inputContentHeight +
		separatorContentHeight
}

// sizeDialogList computes the list dimensions within a dialog and calls
// l.SetSize. It accounts for the title, the input row and its rule
// (withInput, exactly what RenderContext renders for dialogs with a
// filter input), and the view frame, so callers don't have to repeat
// the arithmetic. The scrollbar column is reserved only when content
// overflows the viewport.
//
// Returns listHeight, listTotalHeight, and listWidth for callers that need
// them (e.g. to pass to joinScrollbar or applyInfoColumnVisibility).
//
// Parameters:
//   - t: styles for frame/border measurements.
//   - l: the list to size.
//   - innerWidth: dialog content width (total minus View horizontal frame).
//   - dialogHeight: total dialog content height (already clamped).
//   - withInput: whether the dialog renders an input row (and thus the
//     separator rule around it).
func sizeDialogList(t *styles.Styles, l sizer, innerWidth, dialogHeight int, withInput bool) (listHeight, listTotalHeight, listWidth int) {
	chrome := dialogTitleHeight(t) + ActiveFrame(t).GetVerticalFrameSize()
	if withInput {
		chrome += dialogInputHeight(t)
	}
	listHeight = max(0, dialogHeight-chrome)
	listTotalHeight = l.TotalHeight()
	// Hug the content: a short list shrinks its viewport — and with it the
	// panel — instead of padding blank rows out to the height cap.
	listHeight = min(listHeight, listTotalHeight)

	// Reserve one column for the scrollbar only when it will actually
	// show, so the list otherwise spans the full content width.
	scrollbarWidth := 0
	if listTotalHeight > listHeight {
		scrollbarWidth = 1
	}
	listWidth = max(0, innerWidth-scrollbarWidth)
	l.SetSize(listWidth, listHeight)
	return listHeight, listTotalHeight, listWidth
}

// joinScrollbar appends a vertical scrollbar to the right of view when the
// content overflows its viewport, and returns view unchanged otherwise.
// contentSize is the total content height, viewportSize the visible height,
// and offset the current scroll position.
func joinScrollbar(t *styles.Styles, view string, height, contentSize, viewportSize, offset int) string {
	if sb := common.Scrollbar(t, height, contentSize, viewportSize, offset); sb != "" {
		return lipgloss.JoinHorizontal(lipgloss.Top, view, sb)
	}
	return view
}

// Maximum share of a list row width the secondary info column may take
// before it is hidden entirely, so it never crowds out the item name.
// Command shortcuts are small and non-essential, so they yield sooner
// than the larger, more useful session timestamps.
const (
	sessionInfoMaxPercent = 35
	commandInfoMaxPercent = 25
)

// infoColumnItem is a list item with a secondary info column (a session
// timestamp, a command shortcut) that can be hidden when space is tight.
type infoColumnItem interface {
	// InfoText returns the raw info string, or "" when there is none.
	InfoText() string
	// SetHideInfo toggles whether the info column is rendered.
	SetHideInfo(bool)
}

// applyInfoColumnVisibility hides the secondary info column across every
// item uniformly when its widest entry would take more than maxPercent of
// rowWidth, so item names keep their room. It returns once rowWidth grows
// enough for the widest entry to fit within the budget again.
func applyInfoColumnVisibility(items []list.Item, rowWidth, maxPercent int) {
	widest := 0
	for _, it := range items {
		if ic, ok := it.(infoColumnItem); ok {
			if info := ic.InfoText(); info != "" {
				widest = max(widest, lipgloss.Width(" "+info+" "))
			}
		}
	}
	hide := rowWidth > 0 && widest*100 > rowWidth*maxPercent
	for _, it := range items {
		if ic, ok := it.(infoColumnItem); ok {
			ic.SetHideInfo(hide)
		}
	}
}

// InputCursor adjusts the cursor position for an input field within a
// dialog, for an input that sits under the title. The frame arithmetic
// follows the active placement, so the bottom-anchored panel's borderless
// frame does not shift the cursor.
func InputCursor(t *styles.Styles, cur *tea.Cursor) *tea.Cursor {
	return inputCursorIn(cur, t.Dialog.Title, ActiveInput(t), ActiveFrame(t), 0)
}

// inputCursorIn positions a text-input cursor that sits under a title
// and rowsAbove additional rendered rows, inside the given dialog and
// input frames. All dialog inputs place their cursor through here so
// the frame arithmetic lives in one place.
func inputCursorIn(cur *tea.Cursor, titleStyle, inputStyle, dialogStyle lipgloss.Style, rowsAbove int) *tea.Cursor {
	if cur == nil {
		return nil
	}
	return common.OffsetCursor(cur, 0, 0,
		inputStyle.GetBorderLeftSize()+
			inputStyle.GetMarginLeft()+
			inputStyle.GetPaddingLeft()+
			dialogStyle.GetBorderLeftSize()+
			dialogStyle.GetPaddingLeft()+
			dialogStyle.GetMarginLeft(),
		titleStyle.GetVerticalFrameSize()+
			inputStyle.GetVerticalFrameSize()+
			dialogStyle.GetPaddingTop()+
			dialogStyle.GetMarginTop()+
			dialogStyle.GetBorderTopSize()+
			rowsAbove)
}

// adjustOnboardingInputCursor removes the dialog view frame offset from an
// input cursor. Onboarding dialogs render without Dialog.View frame, while
// InputCursor includes that frame offset for regular dialogs.
func adjustOnboardingInputCursor(t *styles.Styles, cur *tea.Cursor) *tea.Cursor {
	if cur == nil {
		return nil
	}
	dialogStyle := t.Dialog.View
	return common.OffsetCursor(cur,
		-(dialogStyle.GetBorderLeftSize() + dialogStyle.GetPaddingLeft() + dialogStyle.GetMarginLeft()),
		-(dialogStyle.GetBorderTopSize() + dialogStyle.GetPaddingTop() + dialogStyle.GetMarginTop()),
		0, 0)
}

// ActiveFrame returns the frame style dialogs wrap their content in for
// the active placement: the rounded floating box, or the full-width
// top-border-only panel of the bottom-anchored (which-key) mode.
func ActiveFrame(t *styles.Styles) lipgloss.Style {
	if placementTop() {
		return t.Dialog.View
	}
	return t.Dialog.ViewBottom
}

// ActiveInput returns the input-row style for the active placement. The
// bottom-anchored variant drops the bottom margin so the input is the
// panel's last line.
func ActiveInput(t *styles.Styles) lipgloss.Style {
	if placementTop() {
		return t.Dialog.InputPrompt
	}
	return t.Dialog.InputBottom
}

// DialogWidth returns a dialog's total width for area under the active
// placement: full width when bottom-anchored, otherwise clamped to
// defaultDialogMaxWidth inside the floating frame's borders.
func DialogWidth(t *styles.Styles, area uv.Rectangle) int {
	if placementTop() {
		return max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	}
	return max(0, area.Dx())
}

// DialogInnerWidth returns the content width inside a dialog of the given
// total width, for the active placement's frame.
func DialogInnerWidth(t *styles.Styles, width int) int {
	return max(0, width-ActiveFrame(t).GetHorizontalFrameSize())
}

// DialogHeightCeiling clamps a dialog's height to maxHeight within area
// for the active placement's frame.
func DialogHeightCeiling(t *styles.Styles, area uv.Rectangle, maxHeight int) int {
	if placementTop() {
		return max(0, min(maxHeight, area.Dy()-t.Dialog.View.GetVerticalBorderSize()))
	}
	return max(0, min(maxHeight, area.Dy()-t.Dialog.ViewBottom.GetVerticalBorderSize()))
}

// closesWithRule reports whether a rendered dialog view closes with a
// rule line beneath the input row: bottom-anchored dialogs frame the
// input between two rules; floating dialogs close with the rounded
// border instead. Render draws it and DialogCursor counts it, so the
// cursor row can never drift off the input.
func closesWithRule() bool {
	return anchoredAtBottom()
}

// DialogCursor positions a text-input cursor for the active placement.
// Floating: the input sits under the title, and InputCursor's frame
// arithmetic applies. Bottom-anchored: the input row is self-spaced in
// the hand-assembled panel - it carries its own gutter, the frame's
// padding does not wrap it - so the offset reads the input style alone,
// and the input sits on the row above the closing rule.
func DialogCursor(t *styles.Styles, view string, cur *tea.Cursor) *tea.Cursor {
	if cur == nil {
		return nil
	}
	if placementTop() {
		return InputCursor(t, cur)
	}
	input := t.Dialog.InputBottom
	cur.Y = lipgloss.Height(view) - 1
	if closesWithRule() {
		cur.Y--
	}
	return common.OffsetCursor(cur, 0, 0,
		input.GetMarginLeft()+input.GetPaddingLeft()+input.GetBorderLeftSize(),
		0)
}

// RenderContext is a dialog rendering context that can be used to render
// common dialog layouts.
type RenderContext struct {
	// Styles is the styles to use for rendering.
	Styles *styles.Styles
	// TitleStyle is the style of the dialog title by default it uses Styles.Dialog.Title
	TitleStyle lipgloss.Style
	// ViewStyle is the style of the dialog title by default it uses Styles.Dialog.View
	ViewStyle lipgloss.Style
	// TitleGradientFromColor is the color the title gradient starts by default
	// its Styles.Dialog.TitleGradFromColor
	TitleGradientFromColor color.Color
	// TitleGradientToColor is the color the title gradient ends by default its
	// Styles.Dialog.TitleGradToColor
	TitleGradientToColor color.Color
	// Width is the total width of the dialog including any margins, borders,
	// and paddings.
	Width int
	// Gap is the gap between content parts. Zero means no gap.
	Gap int
	// Title is the title of the dialog. This will be styled using the default
	// dialog title style and prepended to the content parts slice.
	Title string
	// TitleInfo is additional information to display next to the title. This
	// part is displayed as is, any styling must be applied before setting this
	// field.
	TitleInfo string
	// Parts are the rendered parts of the dialog.
	Parts []string
	// Input is the raw input-row view. Render styles it with the active
	// input style and places it first among the content (floating) or as
	// the panel's last line (bottom-anchored). Set it with AddInput.
	Input string
	// IsOnboarding indicates whether to render the dialog as part of the
	// onboarding flow. This means that the content will be rendered at the
	// bottom left of the screen.
	IsOnboarding bool
}

// NewRenderContext creates a new RenderContext with the provided styles and width.
func NewRenderContext(t *styles.Styles, width int) *RenderContext {
	return &RenderContext{
		Styles:                 t,
		TitleStyle:             t.Dialog.Title,
		ViewStyle:              ActiveFrame(t),
		TitleGradientFromColor: t.Dialog.TitleGradFromColor,
		TitleGradientToColor:   t.Dialog.TitleGradToColor,
		Width:                  width,
		Parts:                  []string{},
	}
}

// AddInput sets the dialog's input row from the raw input view (e.g.
// input.View()). Render applies the placement-appropriate prompt style.
func (rc *RenderContext) AddInput(inputView string) {
	rc.Input = inputView
}

// AddPart adds a rendered part to the dialog.
func (rc *RenderContext) AddPart(part string) {
	if len(part) > 0 {
		rc.Parts = append(rc.Parts, part)
	}
}

// Render renders the dialog using the provided context.
//
// Rows come in two kinds: gutter rows (title, content parts), which
// the active frame wraps with the content gutter, and self-spaced rows
// (the separator rules and the input row, whose style carries its own
// margins). In the bottom-anchored panel the rules run edge to edge
// and the panel opens with the border row rendered by the frame style
// itself, so the hand assembly cannot drift from it; the floating box
// wraps everything in its rounded border instead.
func (rc *RenderContext) Render() string {
	titleStyle := rc.TitleStyle
	dialogStyle := rc.ViewStyle.Width(rc.Width)

	type row struct {
		text   string
		gutter bool
	}
	var rows []row
	addGutter := func(texts ...string) {
		for _, text := range texts {
			rows = append(rows, row{text: text, gutter: true})
		}
	}
	addSpaced := func(text string) {
		if text != "" {
			rows = append(rows, row{text: text})
		}
	}

	if len(rc.Title) > 0 {
		contentWidth := rc.Width - dialogStyle.GetHorizontalFrameSize() -
			titleStyle.GetHorizontalFrameSize()
		titleInfo := rc.TitleInfo
		titleInfoWidth := lipgloss.Width(titleInfo)
		// Drop the title info entirely when it can't sit beside the title
		// text with at least a one-cell gap. Title info is often styled
		// (e.g. radio toggles with backgrounds and padding); truncating it
		// mid-segment leaves broken colored fragments, so hide it instead.
		if titleInfoWidth > 0 && lipgloss.Width(rc.Title)+1+titleInfoWidth > contentWidth {
			titleInfo = ""
			titleInfoWidth = 0
		}
		title := common.DialogTitle(rc.Styles, rc.Title,
			max(0, contentWidth-titleInfoWidth), rc.TitleGradientFromColor, rc.TitleGradientToColor)
		if len(titleInfo) > 0 {
			title += titleInfo
		}
		addGutter(titleStyle.Render(title))
		if rc.Gap > 0 {
			addGutter(make([]string, rc.Gap)...)
		}
	}

	// Input placement follows the anchoring: floating dialogs put the
	// input directly under the title; the bottom-anchored panel puts it on
	// the last line, below the options and help, framed by the rules.
	// Onboarding keeps the classic order and margins wherever it is
	// drawn.
	inputStyle := ActiveInput(rc.Styles)
	inputLast := anchoredAtBottom() && !rc.IsOnboarding
	if rc.IsOnboarding {
		inputStyle = rc.Styles.Dialog.InputPrompt
	}
	// The separator rule sits between the input row and the content,
	// whichever side of the panel the placement puts them on. When it
	// renders it also replaces the input's breathing-room margin, so the
	// input is framed flush against the rules. Panel rules run edge to
	// edge; the floating box insets them to its content width.
	rule := ""
	if rc.Input != "" && len(rc.Parts) > 0 {
		ruleWidth := max(0, rc.Width-dialogStyle.GetHorizontalFrameSize())
		if inputLast {
			ruleWidth = rc.Width
		}
		rule = rc.Styles.Dialog.Rule.Render(strings.Repeat("─", ruleWidth))
		inputStyle = inputStyle.MarginBottom(0)
	}
	inputRow := ""
	if rc.Input != "" {
		inputRow = inputStyle.Render(rc.Input)
	}

	if inputRow != "" && !inputLast {
		addSpaced(inputRow)
		addSpaced(rule)
	}

	if rc.Gap <= 0 {
		addGutter(rc.Parts...)
	} else {
		for i, p := range rc.Parts {
			if len(p) > 0 {
				addGutter(p)
			}
			if i < len(rc.Parts)-1 {
				addGutter(make([]string, rc.Gap)...)
			}
		}
	}

	if inputRow != "" && inputLast {
		addSpaced(rule)
		addSpaced(inputRow)
		// The bottom-anchored panel closes with a rule beneath the
		// input; the floating box closes with its rounded border.
		if closesWithRule() {
			addSpaced(rule)
		}
	}

	// Flatten: gutter runs are wrapped by the frame style in one go;
	// self-spaced rows pass through untouched.
	var lines []string
	var run []string
	flush := func() {
		if len(run) == 0 {
			return
		}
		if rc.IsOnboarding || placementTop() {
			lines = append(lines, run...)
		} else {
			padded := dialogStyle.Border(lipgloss.Border{}, false, false, false, false)
			lines = append(lines, strings.Split(padded.Render(strings.Join(run, "\n")), "\n")...)
		}
		run = nil
	}
	for _, r := range rows {
		if r.gutter {
			run = append(run, r.text)
			continue
		}
		flush()
		lines = append(lines, r.text)
	}
	flush()

	switch {
	case rc.IsOnboarding:
		return strings.Join(lines, "\n")
	case placementTop():
		return dialogStyle.Render(strings.Join(lines, "\n"))
	default:
		return topBorderRow(rc.Styles, rc.Width) + "\n" + strings.Join(lines, "\n")
	}
}

// topBorderRow renders the bottom-anchored panel's top border row from
// the frame style itself, so hand-assembled panels cannot drift from
// the style that defines the border.
func topBorderRow(t *styles.Styles, width int) string {
	line, _, _ := strings.Cut(t.Dialog.ViewBottom.Width(width).Render(" "), "\n")
	return line
}
