package common

import (
	"io"
	"strings"
	"sync"

	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/ui/xchroma"
)

// Markdown code blocks are highlighted by harness, not by glamour.
//
// Glamour looks its chroma formatter and theme up by name in chroma's
// global registries, which are plain maps: registering while anything
// renders is a data race, so everything is registered once, here, and
// never again. Glamour's own theme path is not used either - it
// registers the first style config it meets under one fixed name and
// keeps it for the life of the process, so code kept the colors of the
// first theme across every theme switch.
//
// Instead each markdown style config renders its code blocks under a
// fixed placeholder theme name. The one registered formatter takes only
// that name from the style glamour hands it, and highlights with the
// rules harness last derived for the name ([codeBlockStyleConfig]),
// which are rebuilt with the renderers on every theme change.
const (
	formatterName     = "harness"
	markdownCodeTheme = "harness-markdown"
	quietCodeTheme    = "harness-quiet"
)

func init() {
	for _, name := range []string{markdownCodeTheme, quietCodeTheme} {
		chromastyles.Register(chroma.MustNewStyle(name, chroma.StyleEntries{}))
	}
	formatters.Register(formatterName, chroma.FormatterFunc(formatCodeBlock))
}

// codeBlockRules is how one markdown style config highlights its code
// blocks.
type codeBlockRules struct {
	style  *chroma.Style
	format chroma.Formatter
	// margin is written in front of every code line: the block's
	// declared indent and margin, as concealed cells. They look like the
	// blank indent they replace, and terminal-native copies still get
	// spaces from them, but a selection copy leaves concealed cells out
	// (see list.HighlightContent), so copied code carries no indent the
	// source did not have.
	margin string
}

var (
	codeBlockRulesMu      sync.RWMutex
	codeBlockRulesByTheme = map[string]codeBlockRules{}
)

// codeBlockStyleConfig returns cfg as glamour must see it: its code
// blocks name the given placeholder theme and leave the margin to the
// formatter. It records the rules the formatter highlights that theme
// with, derived from the same code block style, so the two cannot
// disagree.
func codeBlockStyleConfig(theme string, cfg ansi.StyleConfig) ansi.StyleConfig {
	rules := cfg.CodeBlock

	width := 0
	if rules.Indent != nil {
		width += int(*rules.Indent)
	}
	if rules.Margin != nil {
		width += int(*rules.Margin)
	}
	var bg xansi.Color
	marginStyle := xansi.Style{}.Conceal(true)
	if c := rules.BackgroundColor; c != nil {
		bg = lipgloss.Color(*c)
		marginStyle = marginStyle.BackgroundColor(bg)
	}
	var margin string
	if width > 0 {
		margin = marginStyle.Styled(strings.Repeat(" ", width))
	}

	codeBlockRulesMu.Lock()
	codeBlockRulesByTheme[theme] = codeBlockRules{
		style:  chroma.MustNewStyle(theme, styles.CodeBlockChromaEntries(rules)),
		format: xchroma.Formatter(bg, nil),
		margin: margin,
	}
	codeBlockRulesMu.Unlock()

	zero := uint(0)
	rules.Chroma = nil
	rules.Theme = theme
	rules.Indent = nil
	rules.Margin = &zero
	cfg.CodeBlock = rules
	return cfg
}

// formatCodeBlock is the registered formatter. The style glamour passes
// is a placeholder that only names the rules to highlight with.
func formatCodeBlock(w io.Writer, style *chroma.Style, it chroma.Iterator) error {
	codeBlockRulesMu.RLock()
	rules, ok := codeBlockRulesByTheme[style.Name]
	codeBlockRulesMu.RUnlock()
	if !ok {
		return xchroma.Formatter(nil, nil).Format(w, style, it)
	}
	pw := &linePrefixWriter{w: w, prefix: rules.margin, atLineStart: true}
	if err := rules.format.Format(pw, rules.style, it); err != nil {
		return err
	}
	return pw.Close()
}

// linePrefixWriter writes prefix in front of the first printable byte
// of every line. The escape sequences that open a line are held back and
// written after the prefix, so the prefix, which carries and resets its
// own styling, cannot cancel the styling the line opened with. Escape
// sequences are not printable, so a line holding only styling - the
// reset after a block's final newline - gets no prefix and cannot grow a
// phantom indented line.
type linePrefixWriter struct {
	w           io.Writer
	prefix      string
	atLineStart bool
	// held is the styling that opened the current line, not yet
	// written.
	held     []byte
	inEscape bool
	// inCSI tracks the parameter bytes of a CSI sequence, which run
	// until its final byte.
	inCSI bool
}

func (p *linePrefixWriter) Write(b []byte) (int, error) {
	if p.prefix == "" {
		return p.w.Write(b)
	}
	var out []byte
	for _, c := range b {
		escape := p.inEscape || p.inCSI || c == 0x1b
		switch {
		case p.inCSI:
			p.inCSI = c < 0x40 || c > 0x7e
		case p.inEscape:
			p.inEscape = false
			p.inCSI = c == '['
		case c == 0x1b:
			p.inEscape = true
		}
		switch {
		case !p.atLineStart:
			out = append(out, c)
			if c == '\n' {
				p.atLineStart = true
			}
		case escape:
			p.held = append(p.held, c)
		case c == '\n':
			out = append(append(out, p.held...), c)
			p.held = p.held[:0]
		default:
			out = append(append(append(out, p.prefix...), p.held...), c)
			p.held = p.held[:0]
			p.atLineStart = false
		}
	}
	if _, err := p.w.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Close writes any styling still held back, which only a stream ending
// in the middle of a line's opening escapes leaves behind.
func (p *linePrefixWriter) Close() error {
	if len(p.held) == 0 {
		return nil
	}
	_, err := p.w.Write(p.held)
	p.held = p.held[:0]
	return err
}
