// Package completions is the @-mention source: the value types the
// editor inserts, the async loaders that fill them, and the tiered
// name-priority filter that ranks file matches. The picker surface is
// the shared dialog machinery (see dialog.MentionPicker); this package
// owns what is picked, not how.
package completions

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/fsext"
	"github.com/stubbedev/harness/internal/ui/list"
)

// Name-priority tiers: a match on the exact basename or its stem beats
// a basename prefix, which beats a bare path-segment hit, which beats
// everything else.
const (
	tierExactName = iota
	tierPrefixName
	tierPathSegment
	tierFallback
)

// CompletionItemsLoadedMsg is sent when the mention sources have
// loaded.
type CompletionItemsLoadedMsg struct {
	Files     []FileCompletionValue
	Resources []ResourceCompletionValue
	Subagents []SubagentCompletionValue
}

// LoadItems loads the mention sources off-thread: files from the
// working tree and MCP resources from the connected servers. Subagents
// are already in memory and ride along.
func LoadItems(depth, limit int, subagents []SubagentCompletionValue) tea.Cmd {
	return func() tea.Msg {
		var msg CompletionItemsLoadedMsg
		msg.Subagents = subagents
		var wg sync.WaitGroup
		wg.Go(func() {
			msg.Files = loadFiles(depth, limit)
		})
		wg.Go(func() {
			msg.Resources = loadMCPResources()
		})
		wg.Wait()
		return msg
	}
}

// MentionItems builds the merged mention list: subagents first so they
// sit at the top, then files, then MCP resources.
func MentionItems(
	normalStyle, focusedStyle, matchStyle lipgloss.Style,
	files []FileCompletionValue,
	resources []ResourceCompletionValue,
	subagents []SubagentCompletionValue,
) []list.FilterableItem {
	items := make([]list.FilterableItem, 0, len(subagents)+len(files)+len(resources))

	// Subagents appear first.
	for _, sa := range subagents {
		items = append(items, NewCompletionItem(sa.Name, sa, normalStyle, focusedStyle, matchStyle))
	}

	// Files.
	for _, file := range files {
		items = append(items, NewCompletionItem(file.Path, file, normalStyle, focusedStyle, matchStyle))
	}

	// MCP resources.
	for _, resource := range resources {
		text := resource.MCPName + "/" + cmp.Or(resource.Title, resource.URI)
		items = append(items, NewCompletionItem(text, resource, normalStyle, focusedStyle, matchStyle))
	}

	return items
}

// FilterMentionItems applies the tiered name-priority ranking to a
// mention list: the fuzzy filter runs as usual (so matches highlight),
// then the list re-ranks its own filtered items so basename and stem
// hits render above deeper path matches - render order and selection
// order are the same by construction. all holds the unfiltered items.
func FilterMentionItems(l *list.FilterableList, all []list.FilterableItem, query string) {
	l.SetFilterOrder(MentionOrder(query))
	l.SetItems(all...)
	l.SetFilter(query)
}

// MentionOrder returns the name-priority comparator for a query: a
// stable re-ranking by tier, so equal tiers keep their fuzzy order.
func MentionOrder(query string) func(a, b list.FilterableItem) int {
	queryLower := strings.ToLower(strings.TrimSpace(query))
	return func(a, b list.FilterableItem) int {
		return namePriorityTier(a.Filter(), queryLower) - namePriorityTier(b.Filter(), queryLower)
	}
}

type namePriorityRule struct {
	tier  int
	match func(pathLower, baseLower, stemLower, queryLower string) bool
}

var namePriorityRules = []namePriorityRule{
	{
		tier: tierExactName,
		match: func(_ string, baseLower, stemLower, queryLower string) bool {
			return baseLower == queryLower || stemLower == queryLower
		},
	},
	{
		tier: tierPrefixName,
		match: func(_ string, baseLower, _ string, queryLower string) bool {
			return strings.HasPrefix(baseLower, queryLower)
		},
	},
	{
		tier: tierPathSegment,
		match: func(pathLower, _ string, _ string, queryLower string) bool {
			return hasPathSegment(pathLower, queryLower)
		},
	},
}

func namePriorityTier(path, queryLower string) int {
	if queryLower == "" {
		return tierFallback
	}

	pathLower := strings.ToLower(path)
	baseLower := strings.ToLower(filepath.Base(strings.ReplaceAll(path, `\`, `/`)))
	stemLower := strings.TrimSuffix(baseLower, filepath.Ext(baseLower))
	for _, rule := range namePriorityRules {
		if rule.match(pathLower, baseLower, stemLower, queryLower) {
			return rule.tier
		}
	}
	return tierFallback
}

func hasPathSegment(pathLower, queryLower string) bool {
	return slices.Contains(strings.FieldsFunc(pathLower, func(r rune) bool {
		return r == '/' || r == '\\'
	}), queryLower)
}

func loadFiles(depth, limit int) []FileCompletionValue {
	files, _, _ := fsext.ListDirectory(".", nil, depth, limit)
	slices.Sort(files)
	result := make([]FileCompletionValue, 0, len(files))
	for _, file := range files {
		result = append(result, FileCompletionValue{
			Path: strings.TrimPrefix(file, "./"),
		})
	}
	return result
}

func loadMCPResources() []ResourceCompletionValue {
	var resources []ResourceCompletionValue
	for mcpName, mcpResources := range mcp.Resources() {
		for _, r := range mcpResources {
			resources = append(resources, ResourceCompletionValue{
				MCPName:  mcpName,
				URI:      r.URI,
				Title:    r.Name,
				MIMEType: r.MIMEType,
			})
		}
	}
	return resources
}
