// Package fuzzyrank scores fuzzy queries against candidates made of a
// primary field (a name) and optional secondary text (a description).
//
// Per-term matching uses fzf's FuzzyMatchV2 algorithm. On top of it,
// Match adds the structure fzf itself has no opinion about: a term
// matched in the primary field always outranks one matched only in the
// secondary text, and equal scores break toward the shorter primary
// field.
package fuzzyrank

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/junegunn/fzf/src/algo"
	"github.com/junegunn/fzf/src/util"
)

func init() {
	algo.Init("default")
}

// primaryBonus is added to a term's fzf score when the term matches the
// primary field. It dwarfs any fzf score spread, so a single
// primary-field match outweighs any number of secondary-field matches.
const primaryBonus = 1 << 20

// Fields is one candidate: a name-like primary field and the rest of its
// searchable text.
type Fields struct {
	Primary string
	Rest    string
}

// Result is a successful match: the tiered score and the matched byte
// offsets, per field.
type Result struct {
	Score int
	// Primary holds the matched byte offsets into Primary, empty when
	// every matched term matched only in Rest.
	Primary []int
	// Rest holds the matched byte offsets into Rest, empty when every
	// matched term matched in Primary.
	Rest []int
}

// Match scores query against the fields. The query is split on
// whitespace and every term must match somewhere, the way fzf's extended
// search ANDs its terms; a term matched in the primary field always
// outranks one matched only in the rest. An empty query matches nothing.
func Match(query string, f Fields) (Result, bool) {
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return Result{}, false
	}
	var res Result
	for _, term := range terms {
		score, offsets, ok := matchTerm(term, f.Primary)
		if ok {
			score += primaryBonus
			res.Primary = append(res.Primary, offsets...)
		} else {
			score, offsets, ok = matchTerm(term, f.Rest)
			if !ok {
				return Result{}, false
			}
			res.Rest = append(res.Rest, offsets...)
		}
		res.Score += score
	}
	return res, true
}

// matchTerm runs fzf's FuzzyMatchV2 for one term and returns its score
// plus the matched byte offsets into text.
func matchTerm(term, text string) (int, []int, bool) {
	if text == "" {
		return 0, nil, false
	}
	pattern := []rune(strings.ToLower(term))
	chars := util.ToChars([]byte(text))
	m, pos := algo.FuzzyMatchV2(false, false, true, &chars, pattern, true, nil)
	if m.Start < 0 {
		return 0, nil, false
	}
	return m.Score, byteOffsets(text, pos), true
}

// byteOffsets converts fzf's matched rune indexes into byte offsets into
// the original text. Positions arrive sorted; sort the converted list
// the same way to keep highlight ranges contiguous.
func byteOffsets(text string, runes *[]int) []int {
	if runes == nil || len(*runes) == 0 {
		return nil
	}
	// Rune index to byte offset table, built only when the text is not
	// pure ASCII, where the two coincide.
	var starts []int
	if !isASCII(text) {
		starts = make([]int, utf8.RuneCountInString(text))
		i := 0
		for pos := range text {
			starts[i] = pos
			i++
		}
	}
	out := make([]int, 0, len(*runes))
	for _, r := range *runes {
		if r < 0 || r >= len(starts) && starts != nil {
			continue
		}
		if starts != nil {
			out = append(out, starts[r])
		} else {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out
}

func isASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
