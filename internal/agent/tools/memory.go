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

type MemoryParams struct {
	Action   string `json:"action" description:"One of: save, edit, read, search, list, delete"`
	ID       string `json:"id,omitempty" description:"Memory id (required for read and delete; optional for save and edit)"`
	Title    string `json:"title,omitempty" description:"Short title (required for save; for edit identifies the memory when id is omitted)"`
	Content  string `json:"content,omitempty" description:"Full note content (required for save and edit; replaces existing content on update)"`
	Category string `json:"category,omitempty" description:"user, feedback, project, or reference (default project; edit keeps the existing category when omitted)"`
	Pinned   *bool  `json:"pinned,omitempty" description:"Pin to protect from reaping (save and edit; omit to keep the current value)"`
	Query    string `json:"query,omitempty" description:"Search query (required for search)"`
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
	return fantasy.NewTextResponse(response), nil
}

// memoryEdit updates an existing memory identified by id or exact
// title. Unlike save it never creates: when the target does not exist
// it errors and points at save instead, so a typo'd title cannot
// silently duplicate a memory. Omitted category and pinned keep the
// stored values.
func memoryEdit(ctx context.Context, svc memory.Service, params MemoryParams) (fantasy.ToolResponse, error) {
	if params.Content == "" {
		return fantasy.ToolResponse{}, errors.New("content is required for edit")
	}

	var existing memory.Item
	switch {
	case params.ID != "":
		item, err := svc.Get(ctx, params.ID)
		if err != nil {
			return fantasy.ToolResponse{}, fmt.Errorf("no memory with id %q: %w", params.ID, err)
		}
		existing = item
	case params.Title != "":
		matches, err := svc.Search(ctx, params.Title)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		found := false
		for _, m := range matches {
			if strings.EqualFold(strings.TrimSpace(m.Title), strings.TrimSpace(params.Title)) {
				existing = m
				found = true
				break
			}
		}
		if !found {
			return fantasy.ToolResponse{}, fmt.Errorf("no memory titled %q; use save to create it", params.Title)
		}
	default:
		return fantasy.ToolResponse{}, errors.New("id or title is required for edit")
	}

	title := cmp.Or(params.Title, existing.Title)
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
	if result.Redactions > 0 {
		response += fmt.Sprintf("\n\nNote: %d likely secret(s) were redacted before saving.", result.Redactions)
	}
	return fantasy.NewTextResponse(response), nil
}

func memoryRead(ctx context.Context, svc memory.Service, params MemoryParams) (fantasy.ToolResponse, error) {
	if params.ID == "" {
		return fantasy.ToolResponse{}, errors.New("id is required for read")
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
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, item.IndexLine())
	}
	return fmt.Sprintf("%d memories:\n%s", len(items), strings.Join(lines, "\n"))
}
