package tools

import (
	"cmp"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/stubbedev/harness/internal/toolname"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/memory"
	"github.com/stubbedev/harness/internal/toolaction"
)

//go:embed memory.md
var memoryDescription string

const MemoryToolName = toolname.Memory

const (
	// minTitleMatch is the title similarity above which a single
	// search hit is accepted as the edit target of a slightly-off
	// title instead of erroring.
	minTitleMatch = 0.6
	// minTitleMatchMargin is how much a candidate's title similarity
	// must lead the runner-up by before a fuzzy title is auto-resolved
	// instead of asking; embedding hash noise alone must not decide.
	minTitleMatchMargin = 0.1
	// nearDupSimilarity is the title similarity above which saving a
	// new memory warns that it probably duplicates an existing one.
	nearDupSimilarity = 0.6
)

type MemoryParams struct {
	Action   MemoryAction `json:"action"`
	ID       string       `json:"id,omitempty" description:"Memory id (required for delete; optional for save, edit and read)"`
	Title    string       `json:"title,omitempty" description:"Short title (required for save; for edit identifies the memory when id is omitted)"`
	Content  string       `json:"content,omitempty" description:"Full note content (required for save and edit; replaces existing content on update)"`
	Category string       `json:"category,omitempty" enum:"user,feedback,project,reference" description:"user: stable facts about them; feedback: corrections that shape how you work; project: non-obvious codebase facts; reference: pointers to external material. Default project"`
	Scope    string       `json:"scope,omitempty" enum:"global,repo" description:"global: every repository; repo: this repository, in every clone. A new memory defaults to global for user and feedback, repo otherwise. Elsewhere it picks between same-titled memories in both (default repo) or narrows list and search"`
	Pinned   *bool        `json:"pinned,omitempty" description:"Pin to protect from reaping (save and edit; omit to keep the current value)"`
	Query    string       `json:"query,omitempty" description:"Search query (for search; read falls back to it without an id)"`
}

type MemoryAction string

const (
	MemoryActionSave   MemoryAction = "save"
	MemoryActionEdit   MemoryAction = "edit"
	MemoryActionRead   MemoryAction = "read"
	MemoryActionSearch MemoryAction = "search"
	MemoryActionList   MemoryAction = "list"
	MemoryActionDelete MemoryAction = "delete"
)

type memoryActionFunc func(context.Context, memory.Service, MemoryParams, memory.Scope) (fantasy.ToolResponse, error)

var memoryActions = toolaction.New("action", map[MemoryAction]memoryActionFunc{
	MemoryActionSave:   memorySave,
	MemoryActionEdit:   memoryEdit,
	MemoryActionRead:   memoryRead,
	MemoryActionSearch: memorySearch,
	MemoryActionList:   memoryList,
	MemoryActionDelete: memoryDelete,
})

// NewMemoryTool builds the tool that lets the agent maintain durable
// notes across sessions. Memories are global or scoped to the
// workspace's repository and survive session boundaries.
//
// The tool is parallel. Reads, searches and listings are plain queries;
// saves serialise in the memory service, which is where the
// read-then-create of an upsert has to be atomic anyway (the store is
// shared by every session, sub-agent and harness process, not just this
// tool's calls).
func NewMemoryTool(svc memory.Service) fantasy.AgentTool {
	return toolaction.Advertise(fantasy.NewParallelAgentTool(
		MemoryToolName,
		memoryDescription,
		func(ctx context.Context, params MemoryParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			scope, err := memory.ParseScope(params.Scope)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error() + "; omit scope or pass global or repo"), nil
			}
			action, err := memoryActions.Lookup(params.Action)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return action(ctx, svc, params, scope)
		},
	), memoryActions.Enum())
}

// parseCategory validates an optional category, keeping it empty when
// omitted so the service can tell "keep the stored one" from a choice.
func parseCategory(s string) (memory.Category, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	return memory.ParseCategory(s)
}

func memorySave(ctx context.Context, svc memory.Service, params MemoryParams, scope memory.Scope) (fantasy.ToolResponse, error) {
	if strings.TrimSpace(params.Title) == "" {
		return fantasy.NewTextErrorResponse("title is required for save; pass a short title for the note"), nil
	}
	if strings.TrimSpace(params.Content) == "" {
		return fantasy.NewTextErrorResponse("content is required for save; pass the full note as content"), nil
	}
	category, err := parseCategory(params.Category)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	result, err := svc.Save(ctx, memory.SaveInput{
		ID:       params.ID,
		Scope:    scope,
		Title:    params.Title,
		Content:  params.Content,
		Category: category,
		Pinned:   params.Pinned,
	})
	if err != nil {
		return fantasy.ToolResponse{}, err
	}

	verb := "Updated"
	if result.Created {
		verb = "Saved"
	}
	response := fmt.Sprintf("%s memory %s\n\n%s", verb, result.Item.IndexLine(), result.Item.Content)
	if result.Redactions > 0 {
		response += fmt.Sprintf("\n\nNote: %d likely secret(s) were redacted before saving.", result.Redactions)
	}
	if result.Created {
		if dup, ok := nearDuplicate(ctx, svc, result.Item); ok {
			response += fmt.Sprintf("\n\nWarning: near-duplicate memory exists: %s. If this is the same note, edit that memory instead of keeping both.", dup.IndexLine())
		}
	}
	return fantasy.NewTextResponse(response), nil
}

// nearDuplicate finds an existing memory whose title is so close to a
// newly created one that both are probably the same note. Slug
// equality already upserts silently; this catches paraphrased titles
// that would otherwise mint a near-copy, and the same title saved into
// the other scope.
func nearDuplicate(ctx context.Context, svc memory.Service, saved memory.Item) (memory.Item, bool) {
	matches, err := svc.Search(ctx, saved.Title)
	if err != nil {
		return memory.Item{}, false
	}
	for _, m := range matches {
		if (m.ID != saved.ID || m.Scope != saved.Scope) && memory.Similarity(saved.Title, m.Title) >= nearDupSimilarity {
			return m, true
		}
	}
	return memory.Item{}, false
}

// pick chooses the memory an id or exact title names among the
// workspace's matches, which come repository first: the one in scope
// when it is set, else the repository's, the more specific of the two.
// The note it returns tells the model a same-named memory in the other
// scope was passed over, and how to reach it.
func pick(matches []memory.Item, scope memory.Scope) (memory.Item, string, bool) {
	if scope != "" {
		matches = slices.DeleteFunc(matches, func(m memory.Item) bool { return m.Scope != scope })
	}
	if len(matches) == 0 {
		return memory.Item{}, "", false
	}
	note := ""
	if len(matches) > 1 {
		other := matches[1]
		note = fmt.Sprintf("\n\nNote: a %s memory of the same name also exists; pass scope %q to target it.", other.Scope, other.Scope)
	}
	return matches[0], note, true
}

// memoryEdit updates an existing memory identified by id or title.
// A title that is not exact falls back to the ranked search: one
// clearly matching memory is edited (keeping its original title),
// otherwise the closest candidates are returned for the next call.
// Unlike save it never creates: when the target does not exist it
// errors and points at save, so a typo'd title cannot silently
// duplicate a memory. Omitted category and pinned keep the stored
// values, and the memory stays in its scope.
func memoryEdit(ctx context.Context, svc memory.Service, params MemoryParams, scope memory.Scope) (fantasy.ToolResponse, error) {
	if strings.TrimSpace(params.Content) == "" {
		return fantasy.NewTextErrorResponse("content is required for edit; pass the full replacement note as content"), nil
	}

	var existing memory.Item
	var note string
	fuzzy := false
	switch {
	case params.ID != "":
		matches, err := svc.Lookup(ctx, params.ID)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		item, shadowed, ok := pick(matches, scope)
		if !ok {
			return memoryNotFound(params.ID)
		}
		existing, note = item, shadowed
	case strings.TrimSpace(params.Title) != "":
		item, shadowed, failure, err := resolveByTitle(ctx, svc, params.Title, scope)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		if failure != nil {
			return *failure, nil
		}
		existing, note = item, shadowed
		// A fuzzy match keeps the stored title: the caller's spelling
		// is not a rename request.
		fuzzy = existing.Title != params.Title
	default:
		return fantasy.NewTextErrorResponse("id or title is required for edit; name the memory to change"), nil
	}

	title := existing.Title
	if !fuzzy {
		title = cmp.Or(params.Title, existing.Title)
	}
	category, err := parseCategory(params.Category)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	result, err := svc.Save(ctx, memory.SaveInput{
		ID:       existing.ID,
		Scope:    existing.Scope,
		Title:    title,
		Content:  params.Content,
		Category: category,
		Pinned:   params.Pinned,
	})
	if err != nil {
		return fantasy.ToolResponse{}, err
	}

	response := fmt.Sprintf("Edited memory %s\n\n%s", result.Item.IndexLine(), result.Item.Content)
	if fuzzy {
		response = "Matched by fuzzy title.\n\n" + response
	}
	if result.Redactions > 0 {
		response += fmt.Sprintf("\n\nNote: %d likely secret(s) were redacted before saving.", result.Redactions)
	}
	return fantasy.NewTextResponse(response + note), nil
}

// resolveByTitle finds the memory an edit targets: exact title first,
// then the ranked search. A single clearly-similar hit is returned as
// the target; otherwise the failure names the closest candidates so
// the caller can retry with an id. It never guesses between several
// memories. The failure is for the model; err is a store failure.
func resolveByTitle(ctx context.Context, svc memory.Service, title string, scope memory.Scope) (memory.Item, string, *fantasy.ToolResponse, error) {
	exact, err := svc.LookupTitle(ctx, title)
	if err != nil {
		return memory.Item{}, "", nil, err
	}
	if item, note, ok := pick(exact, scope); ok {
		return item, note, nil, nil
	}

	matches, err := searchIn(ctx, svc, title, scope)
	if err != nil {
		return memory.Item{}, "", nil, err
	}
	if len(matches) == 0 {
		resp := fantasy.NewTextErrorResponse(fmt.Sprintf("no memory titled %q; use save to create it", title))
		return memory.Item{}, "", &resp, nil
	}

	best := matches[0]
	bestSim := memory.Similarity(title, best.Title)
	if bestSim >= minTitleMatch && (len(matches) == 1 || memory.Similarity(title, matches[1].Title) < bestSim-minTitleMatchMargin) {
		return best, "", nil, nil
	}
	if len(matches) > 3 {
		matches = matches[:3]
	}
	resp := fantasy.NewTextErrorResponse(fmt.Sprintf("no memory titled %q; closest matches (edit by id):\n%s", title, renderIndex(matches)))
	return memory.Item{}, "", &resp, nil
}

// memoryNotFound is the result for an id no memory in reach carries.
func memoryNotFound(id string) (fantasy.ToolResponse, error) {
	return fantasy.NewTextErrorResponse(fmt.Sprintf("memory %q not found; list or search memories for the right id", id)), nil
}

// memoryStoreError turns a failed store call into the tool's result: a
// memory that went missing between lookup and use is reported to the
// model like any unknown id, anything else is a failure of the store.
func memoryStoreError(err error, id string) (fantasy.ToolResponse, error) {
	if errors.Is(err, memory.ErrNotFound) {
		return memoryNotFound(id)
	}
	return fantasy.ToolResponse{}, err
}

// searchIn runs a search, narrowed to scope when it is set.
func searchIn(ctx context.Context, svc memory.Service, query string, scope memory.Scope) ([]memory.Item, error) {
	items, err := svc.Search(ctx, query)
	if err != nil || scope == "" {
		return items, err
	}
	return slices.DeleteFunc(items, func(m memory.Item) bool { return m.Scope != scope }), nil
}

// memoryRead returns a memory's full content. Agents ask to read by
// subject far more often than by id, so a missing id falls back to a
// search: one match is read outright, several come back as the index
// so the follow-up call can name the id.
func memoryRead(ctx context.Context, svc memory.Service, params MemoryParams, scope memory.Scope) (fantasy.ToolResponse, error) {
	if params.ID == "" {
		query := cmp.Or(strings.TrimSpace(params.Query), strings.TrimSpace(params.Title))
		if query == "" {
			return fantasy.NewTextErrorResponse("id or query is required for read; pass the memory's id, or a query to find it"), nil
		}
		matches, err := searchIn(ctx, svc, query, scope)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		switch len(matches) {
		case 0:
			return fantasy.NewTextErrorResponse(fmt.Sprintf("no memory matching %q", query)), nil
		case 1:
			return fantasy.NewTextResponse(renderItem(matches[0])), nil
		}
		return fantasy.NewTextResponse(renderIndex(matches)), nil
	}
	matches, err := svc.Lookup(ctx, params.ID)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	target, note, ok := pick(matches, scope)
	if !ok {
		return memoryNotFound(params.ID)
	}
	item, err := svc.Get(ctx, target.Scope, target.ID)
	if err != nil {
		return memoryStoreError(err, params.ID)
	}
	return fantasy.NewTextResponse(renderItem(item) + note), nil
}

func memorySearch(ctx context.Context, svc memory.Service, params MemoryParams, scope memory.Scope) (fantasy.ToolResponse, error) {
	if strings.TrimSpace(params.Query) == "" {
		return fantasy.NewTextErrorResponse("query is required for search; pass the words to search for, or use list to see every memory"), nil
	}
	items, err := searchIn(ctx, svc, params.Query, scope)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	return fantasy.NewTextResponse(renderIndex(items)), nil
}

func memoryList(ctx context.Context, svc memory.Service, _ MemoryParams, scope memory.Scope) (fantasy.ToolResponse, error) {
	items, err := svc.List(ctx)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if scope != "" {
		items = slices.DeleteFunc(items, func(m memory.Item) bool { return m.Scope != scope })
	}
	return fantasy.NewTextResponse(renderIndex(items)), nil
}

func memoryDelete(ctx context.Context, svc memory.Service, params MemoryParams, scope memory.Scope) (fantasy.ToolResponse, error) {
	if params.ID == "" {
		return fantasy.NewTextErrorResponse("id is required for delete; list or search memories for the id"), nil
	}
	matches, err := svc.Lookup(ctx, params.ID)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	item, note, ok := pick(matches, scope)
	if !ok {
		return memoryNotFound(params.ID)
	}
	if err := svc.Delete(ctx, item.Scope, item.ID); err != nil {
		return memoryStoreError(err, params.ID)
	}
	return fantasy.NewTextResponse(fmt.Sprintf("Deleted memory %s", item.IndexLine()) + note), nil
}

func renderItem(item memory.Item) string {
	var b strings.Builder
	b.WriteString(item.IndexLine())
	b.WriteString("\n\n")
	b.WriteString(item.Content)
	return b.String()
}

func renderIndex(items []memory.Item) string {
	if len(items) == 0 {
		return "No memories found."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d memories:", len(items))
	for _, item := range items {
		b.WriteString("\n")
		b.WriteString(item.IndexLine())
		if item.Snippet != "" {
			b.WriteString("\n    ")
			b.WriteString(item.Snippet)
		}
	}
	return b.String()
}
