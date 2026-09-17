package catalog

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

// HighestReasoningLevel returns the strongest reasoning effort the
// model supports. Reasoning defaults to the maximum a model supports;
// anything lower is a deliberate manual choice. Levels list ascending
// in practice, so with unknown level names the last entry wins.
func HighestReasoningLevel(levels []string) string {
	if len(levels) == 0 {
		return ""
	}
	best := levels[0]
	bestRank, known := effortRank[best]
	if !known {
		bestRank = -1
	}
	for _, level := range levels[1:] {
		rank, ok := effortRank[level]
		if !ok {
			// Unknown name: keep it only when nothing known has been
			// seen yet; a later unknown still replaces an earlier one
			// so ascending lists work.
			if bestRank < 0 {
				best, bestRank = level, -1
			}
			continue
		}
		if !known || rank >= bestRank {
			best, bestRank, known = level, rank, true
		}
	}
	return best
}
