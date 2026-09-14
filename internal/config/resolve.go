package config

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/stubbedev/harness/internal/env"
	"github.com/stubbedev/harness/internal/shell"
)

// resolveTimeout bounds how long a single ResolveValue call may spend
// inside shell expansion (including any command substitution).
const resolveTimeout = 5 * time.Minute

// pureEnvRefRe matches a value that is nothing but one environment
// variable reference: `$VAR` or `${VAR}`. Such values must not go
// through the embedded shell: POSIX parses `$N` (a digit followed by
// more word characters, as in the catalog template `$302AI_API_KEY`)
// as positional parameter `$3` followed by literal text, which both
// fabricates a non-empty value and never reads the intended variable.
// Handle these directly with the environment so the referenced name —
// however spelled — is the variable actually consulted.
var pureEnvRefRe = regexp.MustCompile(`^(?:\$([A-Za-z0-9_]+)|\$\{([A-Za-z0-9_]+)\})$`)

type VariableResolver interface {
	ResolveValue(value string) (string, error)
}

// identityResolver is a no-op resolver that returns values unchanged.
// Used in client mode where variable resolution is handled server-side.
type identityResolver struct{}

func (identityResolver) ResolveValue(value string) (string, error) {
	return value, nil
}

// IdentityResolver returns a VariableResolver that passes values through
// unchanged.
func IdentityResolver() VariableResolver {
	return identityResolver{}
}

// Expander is the single-value shell expansion seam used by
// shellVariableResolver. Production wires it to shell.ExpandValue; tests
// can inject a fake via WithExpander.
type Expander func(ctx context.Context, value string, env []string) (string, error)

// ShellResolverOption customizes shell variable resolver construction.
type ShellResolverOption func(*shellVariableResolver)

// WithExpander overrides the expansion function used by the resolver.
// Primarily intended for tests; production callers should not need this.
func WithExpander(e Expander) ShellResolverOption {
	return func(r *shellVariableResolver) {
		if e != nil {
			r.expand = e
		}
	}
}

type shellVariableResolver struct {
	env    env.Env
	expand Expander
}

// NewShellVariableResolver returns a VariableResolver that delegates to
// the embedded shell (the same interpreter used by the bash tool and
// hooks). Supported constructs match shell.ExpandValue: $VAR, ${VAR},
// ${VAR:-default}, $(command), quoting, and escapes. Unset variables
// expand to the empty string by default, matching bash; use
// ${VAR:?message} to require a value and fail loudly when it is missing.
// The stricter "unset is always an error" mode is gated globally by
// shell.NoUnset.
func NewShellVariableResolver(e env.Env, opts ...ShellResolverOption) VariableResolver {
	r := &shellVariableResolver{
		env:    e,
		expand: shell.ExpandValue,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// ResolveValue resolves shell-style substitution anywhere in the string:
//
//   - $(command) for command substitution, with full quoting and nesting.
//   - $VAR and ${VAR} for environment variables.
//   - ${VAR:-default} / ${VAR:+alt} / ${VAR:?msg} for defaulting.
//
// Unset variables expand to the empty string by default, matching bash.
// Command-substitution failures are always a hard error. Required
// credentials should use ${VAR:?message} so a missing variable fails
// loudly at load time instead of quietly resolving to empty. Global
// strict mode is available via shell.NoUnset for callers that want the
// old nounset-on behaviour back.
func (r *shellVariableResolver) ResolveValue(value string) (string, error) {
	// Preserve the historical backward-compat contract: a lone "$" is a
	// malformed config value, not a legal literal. The underlying shell
	// parser would accept it as a literal; we reject it here so existing
	// configs that relied on this validation still fail early.
	if value == "$" {
		return "", fmt.Errorf("invalid value format: %s", value)
	}

	if m := pureEnvRefRe.FindStringSubmatch(value); m != nil {
		name := cmp.Or(m[1], m[2])
		return r.env.Get(name), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()

	// Digit-leading names survive only as aliases: the shell would read
	// them as a positional parameter glued to literal text.
	environ := r.env.Env()
	expanded, extra := aliasDigitVars(value, environ)
	if len(extra) > 0 {
		environ = append(slices.Clip(environ), extra...)
	}

	out, err := r.expand(ctx, expanded, environ)
	if err != nil {
		// The user-written template, not the aliased rewrite, is what
		// belongs in the error.
		return "", sanitizeResolveError(value, err)
	}
	return out, nil
}

// maxResolveErrBytes bounds the size of the inner error message surfaced
// from a resolution failure. Defense-in-depth on top of shell.ExpandValue's
// own stderr budget: a custom Expander injected via WithExpander, or any
// future non-shell error path, must still produce a user-safe message.
const maxResolveErrBytes = 512

// sanitizeResolveError wraps an expansion error with the user-written
// template (the pre-expansion string — it is what they typed, safe to
// surface) and a bounded, scrubbed rendering of the inner error message.
// Contract:
//
//   - Never includes the resolved (post-expansion) value. This helper
//     only receives the template and err, so a successful expansion
//     result cannot reach it.
//   - May include the template verbatim.
//   - Truncates the inner error's message to maxResolveErrBytes and
//     replaces embedded NULs and other non-printables (except tab and
//     newline) with '?'.
//
// The returned error still unwraps to the original for errors.Is/As so
// callers can inspect typed sentinels; only the rendered message is
// scrubbed.
func sanitizeResolveError(template string, err error) error {
	if err == nil {
		return nil
	}
	return &resolveError{
		template: template,
		msg:      scrubErrorMessage(err.Error()),
		inner:    err,
	}
}

// resolveError is the concrete type returned by sanitizeResolveError.
// Its Error() method returns the template + scrubbed inner message;
// Unwrap exposes the original error so errors.Is/As continue to work.
type resolveError struct {
	template string
	msg      string
	inner    error
}

func (e *resolveError) Error() string {
	return fmt.Sprintf("resolving %q: %s", e.template, e.msg)
}

func (e *resolveError) Unwrap() error { return e.inner }

// scrubErrorMessage bounds the message to maxResolveErrBytes bytes and
// replaces non-printable bytes (anything outside ASCII printable, tab, or
// newline) with '?'. Mirrors shell.sanitizeStderr but operates on a
// string rather than raw command stderr and runs at the config layer,
// so arbitrary Expander error text is also sanitized.
func scrubErrorMessage(s string) string {
	if len(s) > maxResolveErrBytes {
		s = s[:maxResolveErrBytes]
	}
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\t' || c == '\n' || (c >= 0x20 && c < 0x7f) {
			out[i] = c
			continue
		}
		out[i] = '?'
	}
	return string(out)
}

// digitVarAliasPrefix names the placeholders aliasDigitVars substitutes
// for digit-leading variable names. Underscore-prefixed and uppercase so
// it cannot collide with a real name a config would reference.
const digitVarAliasPrefix = "_HARNESS_DIGIT_VAR_"

// aliasDigitVars rewrites every `$NAME` / `${NAME...}` reference whose
// name starts with a digit but is not a bare number into a legal shell
// name, and returns the environment entries binding those aliases to
// the real variables' values.
//
// POSIX has no variable name starting with a digit: the shell reads
// `$302AI_API_KEY` as positional parameter `$3` followed by the literal
// text `02AI_API_KEY`, so an unset key silently resolves to a non-empty
// string of garbage, and `${302AI_API_KEY:-x}` fails to parse at all.
// Config values are not scripts and never have positional parameters,
// so a digit-leading name can only have been meant as an environment
// variable — the models.dev catalog ships exactly that in 302.AI's
// `api_key` template. A bare `$3` is left alone: there is nothing else
// it could have meant, and it already expands to the empty string.
//
// A name that is not present in env is left unbound rather than bound
// to "", so `${NAME-default}` and `${NAME:?msg}` keep their
// unset-versus-empty distinction.
//
// Quoting is deliberately not tracked: a config value is expanded as a
// here-document word, where quotes are ordinary characters and do not
// suppress a reference. A backslash escape does suppress one, so an
// escaped pair is copied through unrewritten.
func aliasDigitVars(value string, environ []string) (string, []string) {
	if !strings.Contains(value, "$") {
		return value, nil
	}
	var (
		b       strings.Builder
		extra   []string
		aliases map[string]string
	)
	for i := 0; i < len(value); {
		c := value[i]
		switch {
		case c == '\\' && i+1 < len(value):
			// An escaped character is literal; neither byte can open a
			// reference, so copy the pair through untouched.
			b.WriteString(value[i : i+2])
			i += 2
			continue
		case c == '$':
			nameStart := i + 1
			if nameStart < len(value) && value[nameStart] == '{' {
				nameStart++
			}
			nameEnd := nameStart
			for nameEnd < len(value) && isShellNameByte(value[nameEnd]) {
				nameEnd++
			}
			name := value[nameStart:nameEnd]
			if !isDigitLeadingName(name) {
				break
			}
			alias, ok := aliases[name]
			if !ok {
				alias = fmt.Sprintf("%s%d", digitVarAliasPrefix, len(aliases))
				if aliases == nil {
					aliases = make(map[string]string, 1)
				}
				aliases[name] = alias
				if v, found := lookupEnv(environ, name); found {
					extra = append(extra, alias+"="+v)
				}
			}
			b.WriteString(value[i:nameStart])
			b.WriteString(alias)
			i = nameEnd
			continue
		}
		b.WriteByte(c)
		i++
	}
	if aliases == nil {
		return value, nil
	}
	return b.String(), extra
}

// isShellNameByte reports whether b may appear in a variable name.
func isShellNameByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// isDigitLeadingName reports whether name is a variable name the shell
// cannot express: it starts with a digit and is not a bare number (which
// is a positional parameter and means what it says).
func isDigitLeadingName(name string) bool {
	if name == "" || name[0] < '0' || name[0] > '9' {
		return false
	}
	return strings.IndexFunc(name, func(r rune) bool {
		return r < '0' || r > '9'
	}) >= 0
}

// lookupEnv finds name in a KEY=VALUE environment slice. Later entries
// win, matching the shell's own last-one-wins reading of environ.
func lookupEnv(environ []string, name string) (string, bool) {
	prefix := name + "="
	for i := len(environ) - 1; i >= 0; i-- {
		if strings.HasPrefix(environ[i], prefix) {
			return environ[i][len(prefix):], true
		}
	}
	return "", false
}
