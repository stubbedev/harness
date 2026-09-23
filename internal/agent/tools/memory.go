package tools

import (
	"cmp"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/memory"
)

//go:embed memory.md
var memoryDescription string

const MemoryToolName = "memory"

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
	Action   string `json:"action" enum:"save,edit,read,search,list,delete"`
	ID       string `json:"id,omitempty" description:"Memory id (required for delete; optional for save, edit and read)"`
	Title    string `json:"title,omitempty" description:"Short title (required for save; for edit identifies the memory when id is omitted)"`
	Content  string `json:"content,omitempty" description:"Full note content (required for save and edit; replaces existing content on update)"`
	Category string `json:"category,omitempty" enum:"user,feedback,project,reference" description:"user: stable facts about them; feedback: corrections that shape how you work; project: non-obvious codebase facts; reference: pointers to external material. Default project"`
	Pinned   *bool  `json:"pinned,omitempty" description:"Pin to protect from reaping (save and edit; omit to keep the current value)"`
	Query    string `json:"query,omitempty" description:"Search query (for search; read falls back to it without an id)"`
}

// NewMemoryTool builds the tool that lets the agent maintain durable
// notes across sessions. Memories are workspace-scoped and survive
// session boundaries.
func NewMemoryTool(svc memory.Service) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MemoryToolName,
		memoryDescription,
		func(ctx context.Context, params MemoryParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			switch params.Action {
			case "save":
				return memorySave(ctx, svc, params)
			case "edit":
				return memoryEdit(ctx, svc, params)
			case "read":
				return memoryRead(ctx, svc, params)
			case "search":
				return memorySearch(ctx, svc, params)
			case "list":
				return memoryList(ctx, svc)
			case "delete":
				return memoryDelete(ctx, svc, params)
			default:
				return fantasy.ToolResponse{}, fmt.Errorf("invalid action %q: must be one of save, edit, read, search, list, delete", params.Action)
			}
		},
	)
}

func memorySave(ctx context.Context, svc memory.Service, params MemoryParams) (fantasy.ToolResponse, error) {
	if params.Title == "" {
		return fantasy.ToolResponse{}, errors.New("title is required for save")
	}
	if params.Content == "" {
		return fantasy.ToolResponse{}, errors.New("content is required for save")
	}
	category, err := memory.ParseCategory(params.Category)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}

	result, err := svc.Save(ctx, memory.SaveInput{
		ID:       params.ID,
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
// that would otherwise mint a near-copy.
func nearDuplicate(ctx context.Context, svc memory.Service, saved memory.Item) (memory.Item, bool) {
	matches, err := svc.Search(ctx, saved.Title)
	if err != nil {
		return memory.Item{}, false
	}
	for _, m := range matches {
		if m.ID != saved.ID && memory.Similarity(saved.Title, m.Title) >= nearDupSimilarity {
			return m, true
		}
	}
	return memory.Item{}, false
}

// memoryEdit updates an existing memory identified by id or title.
// A title that is not exact falls back to the ranked search: one
// clearly matching memory is edited (keeping its original title),
// otherwise the closest candidates are returned for the next call.
// Unlike save it never creates: when the target does not exist it
// errors and points at save, so a typo'd title cannot silently
// duplicate a memory. Omitted category and pinned keep the stored
// values.
func memoryEdit(ctx context.Context, svc memory.Service, params MemoryParams) (fantasy.ToolResponse, error) {
	if params.Content == "" {
		return fantasy.ToolResponse{}, errors.New("content is required for edit")
	}

	var existing memory.Item
	fuzzy := false
	switch {
	case params.ID != "":
		item, err := svc.Get(ctx, params.ID)
		if err != nil {
			return fantasy.ToolResponse{}, fmt.Errorf("no memory with id %q: %w", params.ID, err)
		}
		existing = item
	case params.Title != "":
		item, err := resolveByTitle(ctx, svc, params.Title)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		existing = item
		// A fuzzy match keeps the stored title: the caller's spelling
		// is not a rename request.
		fuzzy = existing.Title != params.Title
	default:
		return fantasy.ToolResponse{}, errors.New("id or title is required for edit")
	}

	title := existing.Title
	if !fuzzy {
		title = cmp.Or(params.Title, existing.Title)
	}
	category := existing.Category
	if params.Category != "" {
		parsed, err := memory.ParseCategory(params.Category)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		category = parsed
	}

	result, err := svc.Save(ctx, memory.SaveInput{
		ID:       existing.ID,
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
	return fantasy.NewTextResponse(response), nil
}

// resolveByTitle finds the memory an edit targets: exact title first,
// then the ranked search. A single clearly-similar hit is returned as
// the target; otherwise the closest candidates come back in the error
// so the caller can retry with an id. It never guesses between
// several memories.
func resolveByTitle(ctx context.Context, svc memory.Service, title string) (memory.Item, error) {
	item, err := svc.GetByTitle(ctx, title)
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, memory.ErrNotFound) {
		return memory.Item{}, err
	}

	matches, err := svc.Search(ctx, title)
	if err != nil {
		return memory.Item{}, err
	}
	if len(matches) == 0 {
		return memory.Item{}, fmt.Errorf("no memory titled %q; use save to create it", title)
	}

	best := matches[0]
	bestSim := memory.Similarity(title, best.Title)
	if bestSim >= minTitleMatch && (len(matches) == 1 || memory.Similarity(title, matches[1].Title) < bestSim-minTitleMatchMargin) {
		return best, nil
	}
	if len(matches) > 3 {
		matches = matches[:3]
	}
	return memory.Item{}, fmt.Errorf("no memory titled %q; closest matches (edit by id):\n%s", title, renderIndex(matches))
}

// memoryRead returns a memory's full content. Agents ask to read by
// subject far more often than by id, so a missing id falls back to a
// search: one match is read outright, several come back as the index
// so the follow-up call can name the id.
func memoryRead(ctx context.Context, svc memory.Service, params MemoryParams) (fantasy.ToolResponse, error) {
	if params.ID == "" {
		query := cmp.Or(params.Query, params.Title)
		if query == "" {
			return fantasy.ToolResponse{}, errors.New("id or query is required for read")
		}
		matches, err := svc.Search(ctx, query)
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
	item, err := svc.Get(ctx, params.ID)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	return fantasy.NewTextResponse(renderItem(item)), nil
}

func memorySearch(ctx context.Context, svc memory.Service, params MemoryParams) (fantasy.ToolResponse, error) {
	if params.Query == "" {
		return fantasy.ToolResponse{}, errors.New("query is required for search")
	}
	items, err := svc.Search(ctx, params.Query)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	return fantasy.NewTextResponse(renderIndex(items)), nil
}

func memoryList(ctx context.Context, svc memory.Service) (fantasy.ToolResponse, error) {
	items, err := svc.List(ctx)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	return fantasy.NewTextResponse(renderIndex(items)), nil
}

func memoryDelete(ctx context.Context, svc memory.Service, params MemoryParams) (fantasy.ToolResponse, error) {
	if params.ID == "" {
		return fantasy.ToolResponse{}, errors.New("id is required for delete")
	}
	item, err := svc.Get(ctx, params.ID)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if err := svc.Delete(ctx, params.ID); err != nil {
		return fantasy.ToolResponse{}, err
	}
	return fantasy.NewTextResponse(fmt.Sprintf("Deleted memory %s", item.IndexLine())), nil
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
