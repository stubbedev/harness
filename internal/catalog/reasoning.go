package catalog

import "slices"

// effortRank orders the known reasoning effort names from weakest to
// strongest. Values are comparable so an unknown custom level ranks
// below every known one without breaking the pick.
var effortRank = map[string]int{
	"none":    0,
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

// rankedLevels returns the levels weakest first. Known names are ranked;
// a list with a name the table does not know is taken in the order it
// came, which is ascending in practice.
func rankedLevels(levels []string) []string {
	ordered := slices.Clone(levels)
	for _, level := range ordered {
		if _, ok := effortRank[level]; !ok {
			return ordered
		}
	}
	slices.SortStableFunc(ordered, func(a, b string) int {
		return effortRank[a] - effortRank[b]
	})
	return ordered
}

// DefaultReasoningLevel returns the effort a model runs at until the
// user picks one: the middle of the levels it supports, and the upper
// of the two middle ones when there is an even number. Models arrive
// from the catalog rather than by hand, and a model's strongest level
// is tuned for its hardest problems, not for the tool-call steps that
// make up most of a coding session - GLM-5.3 at "max" thinks for tens
// of seconds before every step.
func DefaultReasoningLevel(levels []string) string {
	if len(levels) == 0 {
		return ""
	}
	ordered := rankedLevels(levels)
	return ordered[len(ordered)/2]
}

// LowestReasoningLevel returns the weakest reasoning effort the model
// supports. Titles and summaries are written with it: they are short,
// mechanical, and on the critical path of a turn or the start of the
// next one, so they should not think for as long as the coding model.
func LowestReasoningLevel(levels []string) string {
	if len(levels) == 0 {
		return ""
	}
	return rankedLevels(levels)[0]
}
