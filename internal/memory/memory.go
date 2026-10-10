// Package memory provides durable, agent-maintained notes that persist
// across sessions. Items live in one SQLite store shared by every
// workspace and every harness process on the machine (see [Store]).
// A memory is global, seen from every workspace, or belongs to one
// repository, seen from every clone and worktree of it (see [RepoKey]).
package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/stubbedev/harness/internal/memory/memdb"
	"github.com/stubbedev/harness/internal/stringext"
)

// Category buckets memories so the index stays scannable and so the
// prompt guidance can tell the agent what belongs where.
type Category string

const (
	// CategoryUser holds stable facts about the user: preferences,
	// environment, workflow habits.
	CategoryUser Category = "user"
	// CategoryFeedback holds corrections and guidance the user gave that
	// should shape future behavior.
	CategoryFeedback Category = "feedback"
	// CategoryProject holds non-obvious facts about the codebase that are
	// not already in context files or easily rediscovered.
	CategoryProject Category = "project"
	// CategoryReference holds pointers to external material: docs, issues,
	// discussions, related repos.
	CategoryReference Category = "reference"
)

// DefaultCategory is used when a save does not specify one.
const DefaultCategory = CategoryProject

// ValidCategories lists the categories a memory may have.
func ValidCategories() []string {
	return []string{
		string(CategoryUser),
		string(CategoryFeedback),
		string(CategoryProject),
		string(CategoryReference),
	}
}

// ParseCategory validates a category string, falling back to
// DefaultCategory when empty.
func ParseCategory(s string) (Category, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return DefaultCategory, nil
	}
	switch Category(s) {
	case CategoryUser, CategoryFeedback, CategoryProject, CategoryReference:
		return Category(s), nil
	}
	return "", fmt.Errorf("invalid category %q: must be one of %s", s, strings.Join(ValidCategories(), ", "))
}

// Scope says which workspaces see a memory.
type Scope string

const (
	// ScopeGlobal memories are seen from every workspace.
	ScopeGlobal Scope = "global"
	// ScopeRepo memories are seen from every clone, worktree and
	// subdirectory of one repository.
	ScopeRepo Scope = "repo"
)

// ParseScope validates a scope string. Empty means unspecified and is
// returned as is: the caller decides what that defaults to.
func ParseScope(s string) (Scope, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch Scope(s) {
	case "", ScopeGlobal, ScopeRepo:
		return Scope(s), nil
	}
	return "", fmt.Errorf("invalid scope %q: must be %s or %s", s, ScopeGlobal, ScopeRepo)
}

// DefaultScope is the scope a new memory of the category gets when the
// save names none: facts about the user and their feedback hold in every
// repository, facts about a codebase and pointers for it in that one.
func DefaultScope(c Category) Scope {
	switch c {
	case CategoryUser, CategoryFeedback:
		return ScopeGlobal
	case CategoryProject, CategoryReference:
		return ScopeRepo
	}
	return ScopeRepo
}

// ErrNotFound reports a Get or Delete against an unknown memory ID.
var ErrNotFound = errors.New("memory not found")

// Item is a single durable memory.
type Item struct {
	ID         string   `json:"id"`
	Scope      Scope    `json:"scope"`
	Category   Category `json:"category"`
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Pinned     bool     `json:"pinned"`
	UseCount   int64    `json:"use_count"`
	LastUsedAt int64    `json:"last_used_at"`
	CreatedAt  int64    `json:"created_at"`
	UpdatedAt  int64    `json:"updated_at"`
	// Snippet carries a matched-content excerpt on search results; it
	// is empty everywhere else.
	Snippet string `json:"snippet,omitempty"`
}

// IndexLine renders the compact one-line form used in the tool's
// list/search output, id and scope included so the model can delete or
// edit by them.
func (i Item) IndexLine() string {
	return fmt.Sprintf("- [%s] (%s, %s) %s", i.ID, i.Scope, i.labels(), i.Title)
}

// promptLine is the line the system prompt index carries: category and
// title, no id, under a heading per scope. The id is derived from the
// title, so it doubled the index for nothing; the tool reads by title.
func (i Item) promptLine() string {
	return fmt.Sprintf("- (%s) %s", i.labels(), i.Title)
}

func (i Item) labels() string {
	if i.Pinned {
		return string(i.Category) + ", pinned"
	}
	return string(i.Category)
}

// SaveInput describes a create-or-update. When ID is set that memory is
// updated; otherwise the ID is derived from the title, so saving again
// with the same title updates the existing memory instead of duplicating
// it.
//
// An empty Scope updates the memory with that ID the workspace already
// sees (its repository's first, then the global one) and creates a new
// one in [DefaultScope] of the category. An empty Category keeps the
// existing value on update and means [DefaultCategory] on create, and a
// nil Pinned keeps the existing value on update and means unpinned on
// create.
type SaveInput struct {
	ID       string
	Scope    Scope
	Title    string
	Content  string
	Category Category
	Pinned   *bool
}

// SaveResult reports what a Save did.
type SaveResult struct {
	Item       Item
	Created    bool
	Redactions int
}

// Service persists and retrieves the memories one workspace sees: the
// global ones and those of its repository.
type Service interface {
	Save(ctx context.Context, input SaveInput) (SaveResult, error)
	// Get returns the memory with id in scope and marks it used.
	Get(ctx context.Context, scope Scope, id string) (Item, error)
	// Lookup returns the memories with id, its repository's first and
	// then the global one, without marking them used. Empty when there
	// is none.
	Lookup(ctx context.Context, id string) ([]Item, error)
	// LookupTitle is Lookup by exact, case-insensitive title.
	LookupTitle(ctx context.Context, title string) ([]Item, error)
	Search(ctx context.Context, query string) ([]Item, error)
	// List returns every memory the workspace sees, global first.
	List(ctx context.Context) ([]Item, error)
	// Delete removes the memory with id in scope.
	Delete(ctx context.Context, scope Scope, id string) error
	// Index renders the compact one-line-per-memory index that is injected
	// into the system prompt, truncated to budget characters (a budget of
	// zero or less means unlimited).
	Index(ctx context.Context, budget int) (string, error)
}

type service struct {
	store   *Store
	repoKey string
	reapFn  func() int
	scrubFn func(string) (string, int)
}

// Option customizes a Service.
type Option func(*service)

// WithReapLimit supplies a function returning the current maximum number
// of memories each scope keeps (0 disables reaping). A function rather
// than a value so live config reloads are honored. Defaults to no
// reaping.
func WithReapLimit(fn func() int) Option {
	return func(s *service) { s.reapFn = fn }
}

// WithScrubber overrides the secret scrubber, primarily for tests.
func WithScrubber(fn func(string) (string, int)) Option {
	return func(s *service) { s.scrubFn = fn }
}

// NewService returns the Service of the workspace whose repository has
// repoKey (see [RepoKey]), backed by the machine-wide store.
func NewService(store *Store, repoKey string, opts ...Option) Service {
	if store == nil {
		panic("memory service requires a store")
	}
	if repoKey == "" {
		panic("memory service requires a repo key")
	}
	s := &service{
		store:   store,
		repoKey: repoKey,
		reapFn:  func() int { return 0 },
		scrubFn: Scrub,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// MaxTitleLen bounds the length of a title; longer titles are truncated.
const MaxTitleLen = 120

// MaxContentLen bounds the length of a memory's content; longer content
// is truncated. Memories are meant to be short notes, not documents.
const MaxContentLen = 16 * 1024

// MaxIDLen bounds a derived memory ID.
const MaxIDLen = 64

// keyOf returns the repo_key column value of a scope as this workspace
// sees it.
func (s *service) keyOf(scope Scope) string {
	if scope == ScopeGlobal {
		return ""
	}
	return s.repoKey
}

// visibleScopes are the scopes a lookup without one searches, most
// specific first.
var visibleScopes = []Scope{ScopeRepo, ScopeGlobal}

func (s *service) Save(ctx context.Context, input SaveInput) (SaveResult, error) {
	input.Title = stringext.TruncateBytes(strings.TrimSpace(input.Title), MaxTitleLen)
	if input.Title == "" {
		return SaveResult{}, errors.New("memory title is required")
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" {
		return SaveResult{}, errors.New("memory content is required")
	}
	input.Content = stringext.TruncateBytes(input.Content, MaxContentLen)
	if input.Category != "" {
		if _, err := ParseCategory(string(input.Category)); err != nil {
			return SaveResult{}, err
		}
	}
	if _, err := ParseScope(string(input.Scope)); err != nil {
		return SaveResult{}, err
	}

	content, redactions := s.scrubFn(input.Content)
	if redactions > 0 {
		slog.Warn("Redacted likely secrets from saved memory", "title", input.Title, "count", redactions)
	}
	embedding := encodeEmbedding(Embed(input.Title + "\n" + content))

	id := input.ID
	if id == "" {
		id = Slug(input.Title)
	}

	// The look-up and the write are one immediate transaction: other
	// sessions, sub-agents and harness processes save into the same
	// store, and two saves of one title racing past the look-up would
	// both try to create it, the second failing on the primary key.
	tx, err := s.store.conn.BeginTx(ctx, nil)
	if err != nil {
		return SaveResult{}, err
	}
	defer tx.Rollback() //nolint:errcheck // A no-op after Commit.
	q := s.store.writer.WithTx(tx)

	existing, found, err := s.findForSave(ctx, q, input.Scope, id)
	if err != nil {
		return SaveResult{}, err
	}

	var row memdb.Memory
	created := !found
	if created {
		category := cmpCategory(input.Category, DefaultCategory)
		scope := input.Scope
		if scope == "" {
			scope = DefaultScope(category)
		}
		row, err = q.CreateMemory(ctx, memdb.CreateMemoryParams{
			Scope:     string(scope),
			RepoKey:   s.keyOf(scope),
			ID:        id,
			Category:  string(category),
			Title:     input.Title,
			Content:   content,
			Pinned:    boolToInt(input.Pinned != nil && *input.Pinned),
			Embedding: embedding,
		})
		if err != nil {
			return SaveResult{}, err
		}
		s.reap(ctx, q, scope)
	} else {
		pinned := existing.Pinned != 0
		if input.Pinned != nil {
			pinned = *input.Pinned
		}
		row, err = q.UpdateMemory(ctx, memdb.UpdateMemoryParams{
			Category:  string(cmpCategory(input.Category, Category(existing.Category))),
			Title:     input.Title,
			Content:   content,
			Pinned:    boolToInt(pinned),
			Embedding: embedding,
			Scope:     existing.Scope,
			RepoKey:   existing.RepoKey,
			ID:        id,
		})
		if err != nil {
			return SaveResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Item: fromDB(row), Created: created, Redactions: redactions}, nil
}

// findForSave finds the memory a save updates: the one with id in the
// named scope, or without one in the first visible scope that has it.
func (s *service) findForSave(ctx context.Context, q *memdb.Queries, scope Scope, id string) (memdb.Memory, bool, error) {
	scopes := visibleScopes
	if scope != "" {
		scopes = []Scope{scope}
	}
	for _, sc := range scopes {
		row, err := q.GetMemory(ctx, memdb.GetMemoryParams{Scope: string(sc), RepoKey: s.keyOf(sc), ID: id})
		switch {
		case err == nil:
			return row, true, nil
		case !errors.Is(err, sql.ErrNoRows):
			return memdb.Memory{}, false, err
		}
	}
	return memdb.Memory{}, false, nil
}

func cmpCategory(c, fallback Category) Category {
	if c == "" {
		return fallback
	}
	return c
}

// reap deletes the unpinned memories of a scope beyond the configured
// limit, evicting the least useful first: lowest use count, then least
// recently used, then oldest. Pinned memories are never evicted. The
// limit holds per scope, global and each repository apart, so a busy
// repository cannot push out the user's global memories, nor another
// repository's.
func (s *service) reap(ctx context.Context, q *memdb.Queries, scope Scope) {
	limit := s.reapFn()
	if limit <= 0 {
		return
	}
	if _, err := q.ReapMemories(ctx, memdb.ReapMemoriesParams{
		Scope:   string(scope),
		RepoKey: s.keyOf(scope),
		Keep:    int64(limit),
	}); err != nil {
		slog.Debug("Failed to reap memories", "error", err)
	}
}

func (s *service) Get(ctx context.Context, scope Scope, id string) (Item, error) {
	row, err := s.store.queries.GetMemory(ctx, memdb.GetMemoryParams{Scope: string(scope), RepoKey: s.keyOf(scope), ID: id})
	if err != nil {
		return Item{}, mapErr(id, err)
	}
	item := fromDB(row)
	s.touch(ctx, item.Scope, item.ID)
	item.UseCount++
	item.LastUsedAt = time.Now().Unix()
	return item, nil
}

func (s *service) Lookup(ctx context.Context, id string) ([]Item, error) {
	return s.lookup(func(scope Scope) (memdb.Memory, error) {
		return s.store.queries.GetMemory(ctx, memdb.GetMemoryParams{Scope: string(scope), RepoKey: s.keyOf(scope), ID: id})
	})
}

func (s *service) LookupTitle(ctx context.Context, title string) ([]Item, error) {
	return s.lookup(func(scope Scope) (memdb.Memory, error) {
		return s.store.queries.GetMemoryByTitle(ctx, memdb.GetMemoryByTitleParams{Scope: string(scope), RepoKey: s.keyOf(scope), Title: title})
	})
}

func (s *service) lookup(get func(Scope) (memdb.Memory, error)) ([]Item, error) {
	var items []Item
	for _, scope := range visibleScopes {
		row, err := get(scope)
		switch {
		case err == nil:
			items = append(items, fromDB(row))
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	return items, nil
}

func (s *service) List(ctx context.Context) ([]Item, error) {
	rows, err := s.store.queries.ListVisibleMemories(ctx, s.repoKey)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, fromDB(row))
	}
	return items, nil
}

func (s *service) Delete(ctx context.Context, scope Scope, id string) error {
	rows, err := s.store.writer.DeleteMemory(ctx, memdb.DeleteMemoryParams{Scope: string(scope), RepoKey: s.keyOf(scope), ID: id})
	if err != nil {
		return mapErr(id, err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil
}

// indexHeadings label the index's sections, one per scope.
var indexHeadings = map[Scope]string{
	ScopeGlobal: "Global:",
	ScopeRepo:   "This repository:",
}

func (s *service) Index(ctx context.Context, budget int) (string, error) {
	items, err := s.List(ctx)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", nil
	}

	var global, repo []string
	for _, item := range items {
		if item.Scope == ScopeGlobal {
			global = append(global, item.promptLine())
		} else {
			repo = append(repo, item.promptLine())
		}
	}

	// Each scope is guaranteed half the budget, and the share one does
	// not use goes to the other: neither many global memories nor many
	// repository ones can push the other scope out of the prompt.
	globalBudget, repoBudget := 0, 0
	if budget > 0 {
		globalNeed := sectionSize(ScopeGlobal, global)
		repoNeed := sectionSize(ScopeRepo, repo)
		globalBudget = min(globalNeed, max(budget/2, budget-repoNeed))
		repoBudget = budget - globalBudget
		if globalBudget <= 0 {
			globalBudget = -1
		}
	}

	var b strings.Builder
	writeSection(&b, ScopeGlobal, global, globalBudget)
	writeSection(&b, ScopeRepo, repo, repoBudget)
	return b.String(), nil
}

// sectionSize is the length an index section takes in full: its heading
// and each line, newline-separated.
func sectionSize(scope Scope, lines []string) int {
	if len(lines) == 0 {
		return 0
	}
	n := len(indexHeadings[scope])
	for _, line := range lines {
		n += 1 + len(line)
	}
	return n
}

// writeSection appends one scope's section to the index: its heading
// and as many lines as fit budget characters, or all of them when the
// budget is zero, and a pointer at the list action for the rest. A
// negative budget leaves no room at all.
func writeSection(b *strings.Builder, scope Scope, lines []string, budget int) {
	if len(lines) == 0 {
		return
	}
	heading := indexHeadings[scope]
	used := len(heading)
	shown := 0
	for _, line := range lines {
		if budget != 0 && used+1+len(line) > budget {
			break
		}
		used += 1 + len(line)
		shown++
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(heading)
	for _, line := range lines[:shown] {
		b.WriteByte('\n')
		b.WriteString(line)
	}
	if remaining := len(lines) - shown; remaining > 0 {
		fmt.Fprintf(b, "\n(+%d more: use the memory tool with action \"list\")", remaining)
	}
}

func (s *service) touch(ctx context.Context, scope Scope, id string) {
	if err := s.store.writer.TouchMemory(ctx, memdb.TouchMemoryParams{Scope: string(scope), RepoKey: s.keyOf(scope), ID: id}); err != nil {
		slog.Debug("Failed to update memory usage", "id", id, "error", err)
	}
}

func mapErr(id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return err
}

func fromDB(row memdb.Memory) Item {
	return Item{
		ID:         row.ID,
		Scope:      Scope(row.Scope),
		Category:   Category(row.Category),
		Title:      row.Title,
		Content:    row.Content,
		Pinned:     row.Pinned != 0,
		UseCount:   row.UseCount,
		LastUsedAt: row.LastUsedAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

// Slug derives a stable, readable memory ID from a title: lowercase
// ASCII letters and digits, everything else collapsed to single hyphens,
// trimmed and truncated. Saving without an ID slugs the title, so
// re-saving under the same title updates the same memory.
//
// A title whose letters or digits are not all ASCII would lose them in
// the slug, so two different titles ("日本 notes", "中国 notes") could
// share an ID and overwrite each other. Such a slug carries a short hash
// of the whole title instead, keeping it stable and distinct.
func Slug(title string) string {
	var b strings.Builder
	lastHyphen := false
	dropped := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			dropped = true
		}
		if !lastHyphen && b.Len() > 0 {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if !dropped {
		return strings.TrimRight(stringext.TruncateBytes(slug, MaxIDLen), "-")
	}
	sum := sha256.Sum256([]byte(title))
	suffix := "m-" + hex.EncodeToString(sum[:6])
	if slug == "" {
		return suffix
	}
	slug = strings.TrimRight(stringext.TruncateBytes(slug, MaxIDLen-len(suffix)-1), "-")
	return slug + "-" + suffix
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
