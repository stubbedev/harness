package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sahilm/fuzzy"
)

// searchCandidate is one entry a defer-loading search tool can return: the
// name the model loads it by, and the text that describes what it does.
type searchCandidate struct {
	name string
	desc string
}

// rankCandidates scores candidates with the fzf algorithm and returns the
// top limit names plus how many matched in total. The query is split on
// whitespace and every term must match somewhere, the way fzf's extended
// search ANDs its terms. Each term scores against the name and the
// description -- a name match counts double -- and a candidate's score is
// the sum over terms; ties break in the order given.
func rankCandidates(query string, candidates []searchCandidate, limit int) ([]string, int) {
	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		name  string
		score int
	}
	var matches []scored
	for _, c := range candidates {
		name := strings.ToLower(c.name)
		desc := strings.ToLower(c.desc)
		total := 0
		allTerms := true
		for _, term := range terms {
			best, found := 0, false
			for _, m := range fuzzy.Find(term, []string{name}) {
				if !found || m.Score*2 > best {
					best, found = m.Score*2, true
				}
			}
			for _, m := range fuzzy.Find(term, []string{desc}) {
				if !found || m.Score > best {
					best, found = m.Score, true
				}
			}
			if !found {
				allTerms = false
				break
			}
			total += best
		}
		if allTerms {
			matches = append(matches, scored{name: c.name, score: total})
		}
	}
	slices.SortStableFunc(matches, func(a, b scored) int { return b.score - a.score })
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
