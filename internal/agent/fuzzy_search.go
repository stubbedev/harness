package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/stubbedev/harness/internal/fuzzyrank"
)

// searchCandidate is one entry a defer-loading search tool can return: the
// name the model loads it by, and the text that describes what it does.
type searchCandidate struct {
	name string
	desc string
}

// rankCandidates scores candidates with fzf's FuzzyMatchV2 algorithm and
// returns the top limit names plus how many matched in total. The query is
// split on whitespace and every term must match somewhere, the way fzf's
// extended search ANDs its terms. A term matched in the name always
// outranks one matched only in the description, and equal scores break
// toward the shorter name.
func rankCandidates(query string, candidates []searchCandidate, limit int) ([]string, int) {
	type scored struct {
		name  string
		score int
	}
	var matches []scored
	for _, c := range candidates {
		res, ok := fuzzyrank.Match(query, fuzzyrank.Fields{Primary: c.name, Rest: c.desc})
		if !ok {
			continue
		}
		matches = append(matches, scored{name: c.name, score: res.Score})
	}
	slices.SortStableFunc(matches, func(a, b scored) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return len(a.name) - len(b.name)
	})
	total := len(matches)
	if total > limit {
		matches = matches[:limit]
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.name
	}
	return names, total
}

// nameList renders names for a search tool's own description, stopping at
// budget characters and saying how many it left out so a truncated list
// never reads as the whole set.
func nameList(names []string, budget int) string {
	if len(names) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, name := range names {
		if i > 0 && b.Len()+len(name)+2 > budget {
			fmt.Fprintf(&b, ", … and %d more (use query to find them)", len(names)-i)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name)
	}
	return b.String()
}
