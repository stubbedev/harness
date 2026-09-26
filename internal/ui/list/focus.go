package list

// FocusedRenderCallback is a helper function that returns a render callback
// that marks items as focused during rendering, and tells selection-aware
// items whether they are the selected one.
func FocusedRenderCallback(list *List) RenderCallback {
	return func(idx, selectedIdx int, item Item) Item {
		if aware, ok := item.(SelectionAware); ok {
			aware.SetSelected(idx == selectedIdx)
		}
		if focusable, ok := item.(Focusable); ok {
			focusable.SetFocused(list.Focused() && idx == selectedIdx)
		}
		return item
	}
}
