// Package memory provides durable, agent-maintained notes that persist
// across sessions. Items live in the workspace SQLite database alongside
// sessions, so they survive session (and process) boundaries and are
// scoped to the project automatically.
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
	"unicode/utf8"

	"github.com/stubbedev/harness/internal/db"
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

// ErrNotFound reports a Get or Delete against an unknown memory ID.
var ErrNotFound = errors.New("memory not found")

// Item is a single durable memory.
type Item struct {
	ID         string   `json:"id"`
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
// list/search output, id included so the model can delete or edit by it.
func (i Item) IndexLine() string {
	return fmt.Sprintf("- [%s] %s", i.ID, i.promptLine()[2:])
}

// promptLine is the line the system prompt index carries: category and
// title, no id. The id is derived from the title, so it doubled the
// index for nothing; the tool reads by title.
func (i Item) promptLine() string {
	category := string(i.Category)
	if i.Pinned {
		category += ", pinned"
	}
	return fmt.Sprintf("- (%s) %s", category, i.Title)
}

// SaveInput describes a create-or-update. When ID is set that memory is
// updated; otherwise the ID is derived from the title, so saving again
// with the same title updates the existing memory instead of duplicating
// it. A nil Pinned keeps the existing value on update and means unpinned
// on create.
type SaveInput struct {
	ID       string
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

// Service persists and retrieves memories.
type Service interface {
	Save(ctx context.Context, input SaveInput) (SaveResult, error)
	Get(ctx context.Context, id string) (Item, error)
	// GetByTitle returns the memory whose title matches exactly,
	// case-insensitively, or ErrNotFound.
	GetByTitle(ctx context.Context, title string) (Item, error)
	Search(ctx context.Context, query string) ([]Item, error)
	List(ctx context.Context) ([]Item, error)
	Delete(ctx context.Context, id string) error
	// Index renders the compact one-line-per-memory index that is injected
	// into the system prompt, truncated to budget characters (a budget of
	// zero or less means unlimited).
	Index(ctx context.Context, budget int) (string, error)
}

type service struct {
	q       *db.Queries
	conn    *sql.DB
	reapFn  func() int
	scrubFn func(string) (string, int)
}

// Option customizes a Service.
type Option func(*service)

// WithReapLimit supplies a function returning the current maximum number
// of memories to keep (0 disables reaping). A function rather than a
// value so live config reloads are honored. Defaults to no reaping.
func WithReapLimit(fn func() int) Option {
	return func(s *service) { s.reapFn = fn }
}

// WithScrubber overrides the secret scrubber, primarily for tests.
func WithScrubber(fn func(string) (string, int)) Option {
	return func(s *service) { s.scrubFn = fn }
}

// NewService returns a Service backed by the given queries. The raw
// connection backs the full-text search query, which sqlc cannot
// manage because it cannot parse the FTS5 virtual table.
func NewService(q *db.Queries, conn *sql.DB, opts ...Option) Service {
	if conn == nil {
		panic("memory service requires a database connection")
	}
	s := &service{
		q:       q,
		conn:    conn,
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

func (s *service) Save(ctx context.Context, input SaveInput) (SaveResult, error) {
	input.Title = truncate(strings.TrimSpace(input.Title), MaxTitleLen)
	if input.Title == "" {
		return SaveResult{}, errors.New("memory title is required")
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" {
		return SaveResult{}, errors.New("memory content is required")
	}
	input.Content = truncate(input.Content, MaxContentLen)

	category := input.Category
	if category == "" {
		category = DefaultCategory
	} else if _, err := ParseCategory(string(category)); err != nil {
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
	pinned := input.Pinned != nil && *input.Pinned

	existing, err := s.q.GetMemory(ctx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		row, err := s.q.CreateMemory(ctx, db.CreateMemoryParams{
			ID:        id,
			Category:  string(category),
			Title:     input.Title,
			Content:   content,
			Pinned:    boolToInt(pinned),
			Embedding: embedding,
		})
		if err != nil {
			return SaveResult{}, err
		}
		s.reap(ctx)
		return SaveResult{Item: fromDB(row), Created: true, Redactions: redactions}, nil
	case err != nil:
		return SaveResult{}, err
	}

	newPinned := existing.Pinned != 0
	if input.Pinned != nil {
		newPinned = *input.Pinned
	}
	row, err := s.q.UpdateMemory(ctx, db.UpdateMemoryParams{
		Category:  string(category),
		Title:     input.Title,
		Content:   content,
		Pinned:    boolToInt(newPinned),
		Embedding: embedding,
		ID:        id,
	})
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Item: fromDB(row), Redactions: redactions}, nil
}

// reap deletes unpinned memories beyond the configured limit, evicting
// the least useful first: lowest use count, then least recently used,
// then oldest. Pinned memories are never evicted.
func (s *service) reap(ctx context.Context) {
	max := s.reapFn()
	if max <= 0 {
		return
	}
	if _, err := s.q.ReapMemories(ctx, int64(max)); err != nil {
		slog.Debug("Failed to reap memories", "error", err)
	}
}

func (s *service) Get(ctx context.Context, id string) (Item, error) {
	row, err := s.q.GetMemory(ctx, id)
	if err != nil {
		return Item{}, mapErr(id, err)
	}
	item := fromDB(row)
	s.touch(ctx, id)
	item.UseCount++
	item.LastUsedAt = time.Now().Unix()
	return item, nil
}

func (s *service) GetByTitle(ctx context.Context, title string) (Item, error) {
	row, err := s.q.GetMemoryByTitle(ctx, title)
	if err != nil {
		return Item{}, mapErr(title, err)
	}
	return fromDB(row), nil
}

func (s *service) List(ctx context.Context) ([]Item, error) {
	rows, err := s.q.ListMemories(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, fromDB(row))
	}
	return items, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	rows, err := s.q.DeleteMemory(ctx, id)
	if err != nil {
		return mapErr(id, err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil
}

func (s *service) Index(ctx context.Context, budget int) (string, error) {
	items, err := s.List(ctx)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", nil
	}

	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, item.promptLine())
	}

	if budget <= 0 {
		return strings.Join(lines, "\n"), nil
	}

	var b strings.Builder
	shown := 0
	for _, line := range lines {
		if b.Len()+len(line)+1 > budget {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		shown++
	}
	if remaining := len(items) - shown; remaining > 0 {
		fmt.Fprintf(&b, "\n(+%d more: use the memory tool with action \"list\")", remaining)
	}
	return b.String(), nil
}

func (s *service) touch(ctx context.Context, id string) {
	if err := s.q.TouchMemory(ctx, id); err != nil {
		slog.Debug("Failed to update memory usage", "id", id, "error", err)
	}
}

func mapErr(id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return err
}

func fromDB(row db.Memory) Item {
	return Item{
		ID:         row.ID,
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
		return strings.TrimRight(truncate(slug, MaxIDLen), "-")
	}
	sum := sha256.Sum256([]byte(title))
	suffix := "m-" + hex.EncodeToString(sum[:6])
	if slug == "" {
		return suffix
	}
	slug = strings.TrimRight(truncate(slug, MaxIDLen-len(suffix)-1), "-")
	return slug + "-" + suffix
}

// truncate cuts s to at most max bytes without splitting a rune.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
