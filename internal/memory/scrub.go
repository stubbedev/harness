package memory

import "regexp"

// Redacted is the placeholder substituted for anything that looks like a
// secret.
const Redacted = "[REDACTED]"

// scrubRule replaces everything a pattern matches with a replacement that
// may capture a label to keep (e.g. the key name in "api_key: ...").
type scrubRule struct {
	re      *regexp.Regexp
	replace string
}

// scrubRules matches common secret shapes: provider API keys with known
// prefixes, PEM private key blocks, bearer tokens, and key/value
// assignments whose name says the value is sensitive. It is heuristic:
// better to redact a look-alike than persist a credential, and the agent
// is told in the tool response when redaction happened.
var scrubRules = []scrubRule{
	// PEM private key blocks, header through footer.
	{
		re:      regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
		replace: "[REDACTED PRIVATE KEY]",
	},
	// Anthropic keys: sk-ant-...
	{
		re:      regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{16,}`),
		replace: Redacted,
	},
	// OpenAI keys: sk-... and sk-proj-...
	{
		re:      regexp.MustCompile(`sk-(?:proj-)?[A-Za-z0-9_\-]{20,}`),
		replace: Redacted,
	},
	// GitHub tokens: ghp_, gho_, ghu_, ghs_, ghr_, github_pat_...
	{
		re:      regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`),
		replace: Redacted,
	},
	// Slack tokens: xox[abprs]-...
	{
		re:      regexp.MustCompile(`xox[abprs]-[A-Za-z0-9\-]{10,}`),
		replace: Redacted,
	},
	// AWS access key IDs.
	{
		re:      regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		replace: Redacted,
	},
	// Google API keys.
	{
		re:      regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`),
		replace: Redacted,
	},
	// Bearer authorization headers.
	{
		re:      regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._+\-/=]{16,}`),
		replace: "Bearer " + Redacted,
	},
	// Sensitive key/value assignments, e.g. "password: hunter2" or
	// "db_password=hunter2", keeping the key name so the note stays
	// useful.
	{
		re:      regexp.MustCompile(`(?i)\b([\w.\-]*(?:api[_\-]?key|apikey|secret|token|passwd|passphrase|password|pwd)\b["']?\s*[:=]\s*["']?)[^\s"',;]{8,}`),
		replace: `${1}` + Redacted,
	},
}

// Scrub redacts likely secrets from s and reports how many replacements
// it made. It runs before anything is persisted.
func Scrub(s string) (string, int) {
	count := 0
	for _, rule := range scrubRules {
		s = rule.re.ReplaceAllStringFunc(s, func(match string) string {
			count++
			return rule.re.ReplaceAllString(match, rule.replace)
		})
	}
	return s, count
}
