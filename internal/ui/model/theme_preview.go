package model

import "github.com/stubbedev/harness/internal/ui/styles"

// themePreview tracks the theme picker's live preview. The picker applies
// every highlighted theme to the whole UI so the user sees the real
// colors, which means the UI has to remember what to put back when the
// dialog closes without a confirmed selection.
//
// The zero value is the inactive state: nothing previewed, nothing to
// restore.
type themePreview struct {
	// restore is the styles that were active before the first preview,
	// and restoreKey the theme key that went with them.
	restore    *styles.Styles
	restoreKey string
	// name is the theme currently previewed, so moving back onto the same
	// entry doesn't pay for another style rebuild.
	name string
}

// begin snapshots the theme to fall back to. Only the first call after a
// take or clear records anything; later previews keep the original
// snapshot, since that is what the user had before the picker opened.
func (p *themePreview) begin(current styles.Styles, key string) {
	if p.restore != nil {
		return
	}
	p.restore = &current
	p.restoreKey = key
}

// set records name as the previewed theme and reports whether it changed,
// i.e. whether the caller still has to apply it.
func (p *themePreview) set(name string) bool {
	if p.name == name {
		return false
	}
	p.name = name
	return true
}

// take returns the styles and theme key to restore and resets the state.
// The final return is false when nothing was previewed, in which case
// there is nothing to put back.
func (p *themePreview) take() (styles.Styles, string, bool) {
	if p.restore == nil {
		return styles.Styles{}, "", false
	}
	restore, key := *p.restore, p.restoreKey
	p.clear()
	return restore, key, true
}

// clear drops the state, keeping whatever theme is currently applied.
func (p *themePreview) clear() {
	p.restore = nil
	p.restoreKey = ""
	p.name = ""
}
