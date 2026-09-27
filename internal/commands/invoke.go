package commands

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stubbedev/harness/internal/skills"
)

// Every kind of command - built-in, custom, skill, extension, MCP prompt -
// takes its arguments the same way: the text typed after its name in the
// editor ("/review 1234 focus on auth"), bound to what the command
// declares by an ArgSpec, and carried to the command as Args. Anything
// that needs to know how a command takes arguments asks its spec, so the
// palette hint, the arguments form, the editor binding and the content
// expansion cannot disagree.

// ArgumentsPlaceholder is replaced by everything typed after a command's
// name. Skills own it: a skill and a command file read it the same way.
const ArgumentsPlaceholder = skills.ArgumentsPlaceholder

// ArgSpec is how a command takes arguments.
type ArgSpec struct {
	// Fields are the named arguments the command declares, in order.
	Fields []Argument
	// FreeText marks a command that accepts free-form text after its
	// name even when it declares no fields.
	FreeText bool
	// Hint overrides the usage hint derived from Fields, e.g. a skill's
	// argument-hint frontmatter.
	Hint string
	// Title and Description head the arguments form that asks for
	// fields the invocation left out.
	Title       string
	Description string
}

// Args are the arguments a command was invoked with.
type Args struct {
	// Raw is the text typed after the command's name.
	Raw string
	// Values holds the named fields, bound from Raw or filled in the
	// arguments form.
	Values map[string]string
}

// TakesArgs reports whether anything may follow the command's name.
func (s ArgSpec) TakesArgs() bool {
	return len(s.Fields) > 0 || s.FreeText
}

// HintText is the usage hint shown after the command's name:
// "<required> [optional]", or the explicit Hint. It is empty for a
// command that asks for nothing, which is then run as soon as it is
// picked.
func (s ArgSpec) HintText() string {
	if s.Hint != "" {
		return s.Hint
	}
	parts := make([]string, 0, len(s.Fields))
	for _, arg := range s.Fields {
		name := strings.ToLower(arg.ID)
		if arg.Required {
			parts = append(parts, "<"+name+">")
		} else {
			parts = append(parts, "["+name+"]")
		}
	}
	return strings.Join(parts, " ")
}

// Bind binds raw text to the spec's fields. A single field takes the
// whole text, so free-form input needs no quoting; with several, words
// fill them in order and the last takes whatever remains. It errors when
// text is given to a command that takes none.
func (s ArgSpec) Bind(raw string) (Args, error) {
	raw = strings.TrimSpace(raw)
	args := Args{Raw: raw, Values: map[string]string{}}
	if raw == "" {
		return args, nil
	}
	if !s.TakesArgs() {
		return Args{}, fmt.Errorf("this command takes no arguments")
	}
	switch len(s.Fields) {
	case 0:
	case 1:
		args.Values[s.Fields[0].ID] = raw
	default:
		words := SplitArgs(raw)
		for i, arg := range s.Fields {
			if i >= len(words) {
				break
			}
			if i == len(s.Fields)-1 {
				args.Values[arg.ID] = strings.Join(words[i:], " ")
				break
			}
			args.Values[arg.ID] = words[i]
		}
	}
	return args, nil
}

// Complete reports whether args hold every required field.
func (s ArgSpec) Complete(args Args) bool {
	for _, arg := range s.Fields {
		if arg.Required && strings.TrimSpace(args.Values[arg.ID]) == "" {
			return false
		}
	}
	return true
}

// Value returns the named field's value.
func (a Args) Value(id string) string {
	return a.Values[id]
}

// Expand puts the arguments into a command's content: $ARGUMENTS is
// replaced by the raw text and every $NAME by its field. Raw text given
// to content with no $ARGUMENTS and no field to take it follows the
// content, so a command written without a placeholder still sees it.
func (a Args) Expand(content string) string {
	raw := strings.TrimSpace(a.Raw)
	hasPlaceholder := strings.Contains(content, ArgumentsPlaceholder)
	content = strings.ReplaceAll(content, ArgumentsPlaceholder, raw)
	// Longer names first, so $TARGET cannot eat the front of $TARGET_DIR.
	names := slices.SortedFunc(maps.Keys(a.Values), func(x, y string) int {
		return cmp.Or(len(y)-len(x), strings.Compare(x, y))
	})
	for _, name := range names {
		content = strings.ReplaceAll(content, "$"+name, a.Values[name])
	}
	if raw != "" && !hasPlaceholder && len(a.Values) == 0 {
		content = strings.TrimRight(content, "\n") + "\n\nARGUMENTS: " + raw
	}
	return content
}

// Spec is how a custom command takes arguments. A command file accepts
// free text ($ARGUMENTS) besides its named fields; an extension command
// takes exactly the arguments it registered; a skill takes free text.
func (c CustomCommand) Spec() ArgSpec {
	switch {
	case c.Skill != nil:
		return ArgSpec{FreeText: true, Hint: c.ArgumentHint, Title: c.Name}
	case c.ExtensionID != "":
		return ArgSpec{Fields: c.Arguments, Hint: c.ArgumentHint, Title: c.Name, Description: c.Description}
	}
	hint := c.ArgumentHint
	if hint == "" && len(c.Arguments) == 0 && strings.Contains(c.Content, ArgumentsPlaceholder) {
		hint = "[arguments]"
	}
	return ArgSpec{Fields: c.Arguments, FreeText: true, Hint: hint, Title: c.Name, Description: c.Description}
}

// Spec is how an MCP prompt takes arguments: exactly the ones the server
// declared.
func (p MCPPrompt) Spec() ArgSpec {
	return ArgSpec{
		Fields:      p.Arguments,
		Title:       cmp.Or(p.Title, p.ClientID+":"+p.PromptID),
		Description: p.Description,
	}
}

// ParseInvocation splits an editor line that invokes a command into the
// command's name and the raw text after it. The line must open with one
// of prefixes, directly followed by the name; the name ends at the first
// whitespace. ok is false when text is not shaped like an invocation, in
// which case it is an ordinary message.
func ParseInvocation(text string, prefixes []string) (name, args string, ok bool) {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	var rest string
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(text, prefix) {
			rest, ok = text[len(prefix):], true
			break
		}
	}
	if !ok {
		return "", "", false
	}
	end := strings.IndexFunc(rest, unicode.IsSpace)
	if end < 0 {
		end = len(rest)
	}
	name = rest[:end]
	if !IsCommandName(name) {
		return "", "", false
	}
	return name, strings.TrimSpace(rest[end:]), true
}

// IsCommandName reports whether s can name a command: letters, digits
// and the separators command IDs use. A path such as "/etc/hosts" has a
// slash in it and is not one.
func IsCommandName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("_-.:", r) {
			return false
		}
	}
	return true
}

// SplitArgs splits raw arguments into words the way a shell would for
// the simple cases: whitespace separates, and single or double quotes
// group words (the quotes themselves are dropped).
func SplitArgs(raw string) []string {
	var (
		words   []string
		current strings.Builder
		quote   rune
		inWord  bool
	)
	for len(raw) > 0 {
		r, size := utf8.DecodeRuneInString(raw)
		raw = raw[size:]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '"' || r == '\'':
			quote, inWord = r, true
		case unicode.IsSpace(r):
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, current.String())
	}
	return words
}

// InvocationNames returns the names a loaded command or skill answers to
// from the editor, given the label it was loaded under: the bare name
// first, then the full label, which settles a clash between a user and
// a project command of the same name.
func InvocationNames(label string) []string {
	for _, source := range []skills.SourceType{skills.SourceUser, skills.SourceProject, skills.SourceSystem} {
		if bare, ok := strings.CutPrefix(label, source.Prefix()); ok && bare != "" {
			return []string{bare, label}
		}
	}
	return []string{label}
}
