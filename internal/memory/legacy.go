package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/memory/memdb"
)

// LegacySource is a per-workspace database from before the shared
// store, whose memories table is imported into it once.
type LegacySource struct {
	// Name records the import: the workspace data directory's name for
	// one under the workspaces root, else the data directory's path.
	Name string
	// DBPath is the workspace's harness.db.
	DBPath string
	// ProjectPath is the workspace's project directory, empty when no
	// record of it survives.
	ProjectPath string
}

// workspaceDBFile is the database inside a workspace data directory.
const workspaceDBFile = "harness.db"

// LegacySources lists the per-workspace databases to import: one for
// every data directory under workspacesDir, and one for every data
// directory elsewhere that projects names (options.data_directory, the
// old in-repo .harness). projects maps a data directory to the project
// directory it served, as projects.json records them; a directory under
// a "workspaces" root is matched by name, so the mapping survives the
// global data root moving.
func LegacySources(workspacesDir string, projects map[string]string) []LegacySource {
	byName := make(map[string]string, len(projects))
	var outside []string
	for dataDir, project := range projects {
		dataDir = filepath.Clean(dataDir)
		if filepath.Base(filepath.Dir(dataDir)) == filepath.Base(workspacesDir) {
			byName[filepath.Base(dataDir)] = project
			continue
		}
		outside = append(outside, dataDir)
	}

	var sources []LegacySource
	entries, err := os.ReadDir(workspacesDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("Failed to list workspace data directories for the memory import", "dir", workspacesDir, "error", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sources = append(sources, LegacySource{
			Name:        entry.Name(),
			DBPath:      filepath.Join(workspacesDir, entry.Name(), workspaceDBFile),
			ProjectPath: byName[entry.Name()],
		})
	}
	slices.Sort(outside)
	for _, dataDir := range outside {
		sources = append(sources, LegacySource{
			Name:        dataDir,
			DBPath:      filepath.Join(dataDir, workspaceDBFile),
			ProjectPath: projects[dataDir],
		})
	}
	return sources
}

// ImportReport counts what an [Store.ImportLegacy] did.
type ImportReport struct {
	// Sources is how many workspaces were imported this time.
	Sources int
	// Memories is how many memories they carried.
	Memories int
	// Failed is how many could not be read; they are retried on the
	// next open.
	Failed int
}

// legacyRow is a memory as a per-workspace database held it.
type legacyRow struct {
	id, category, title, content          string
	pinned, useCount, lastUsed, createdAt int64
	updatedAt                             int64
}

// ImportLegacy copies the memories of every source not imported before
// into the store, recording each so it happens once. Memories about the
// user and their feedback become global; the rest belong to the
// repository of the workspace's project directory (see [RepoKey]),
// falling back to "path:" and the recorded directory when it is gone,
// and to "workspace:" and the source's name when no directory is
// recorded, so nothing is dropped. Memories that meet under one scope,
// repository and id merge: the most recently updated content wins,
// pinned is kept if either was, use counts add up, and the last use and
// creation times are the latest and earliest.
//
// Old databases are opened read-only and left as they are. One that is
// missing a memories table counts as imported with none; one that
// cannot be read (corrupt, locked) is logged and retried next time.
func (s *Store) ImportLegacy(ctx context.Context, sources []LegacySource) (ImportReport, error) {
	release := lockStore(ctx, filepath.Dir(s.path))
	defer release()

	done, err := s.writer.ListLegacyImports(ctx)
	if err != nil {
		return ImportReport{}, err
	}
	var report ImportReport
	for _, src := range sources {
		if slices.Contains(done, src.Name) {
			continue
		}
		rows, err := readLegacy(ctx, src.DBPath)
		if err != nil {
			slog.Warn("Failed to read memories from a workspace database; retrying on the next start", "db", src.DBPath, "error", err)
			report.Failed++
			continue
		}
		if err := s.importRows(ctx, src, rows); err != nil {
			return report, fmt.Errorf("importing memories of %s: %w", src.Name, err)
		}
		report.Sources++
		report.Memories += len(rows)
		if len(rows) > 0 {
			slog.Info("Imported memories from a workspace database", "workspace", src.Name, "memories", len(rows))
		}
	}
	return report, nil
}

// readLegacy loads the memories table of a per-workspace database. A
// missing database or table holds no memories.
func readLegacy(ctx context.Context, path string) ([]legacyRow, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	conn, err := db.ConnectReadOnly(ctx, path)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var tables int
	if err := conn.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'memories'").Scan(&tables); err != nil {
		return nil, err
	}
	if tables == 0 {
		return nil, nil
	}
	rows, err := conn.QueryContext(ctx, `SELECT id, category, title, content, pinned, use_count,
       last_used_at, created_at, updated_at
FROM memories ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []legacyRow
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.id, &r.category, &r.title, &r.content, &r.pinned, &r.useCount,
			&r.lastUsed, &r.createdAt, &r.updatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// importRows merges one source's memories into the store and records
// the source, in one transaction: a crash leaves the source either
// wholly imported and recorded, or neither.
func (s *Store) importRows(ctx context.Context, src LegacySource, rows []legacyRow) error {
	repoKey := ""
	if slices.ContainsFunc(rows, func(r legacyRow) bool { return legacyScope(r.category) == ScopeRepo }) {
		repoKey = legacyRepoKey(ctx, src)
	}

	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // A no-op after Commit.
	q := s.writer.WithTx(tx)

	// Another process may have imported the source since the list was
	// read, when the open lock could not be had.
	done, err := q.ListLegacyImports(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(done, src.Name) {
		return nil
	}

	for _, r := range rows {
		scope := legacyScope(r.category)
		key := ""
		if scope == ScopeRepo {
			key = repoKey
		}
		merged := memdb.ImportMemoryParams{
			Scope:      string(scope),
			RepoKey:    key,
			ID:         r.id,
			Category:   r.category,
			Title:      r.title,
			Content:    r.content,
			Pinned:     boolToInt(r.pinned != 0),
			UseCount:   r.useCount,
			LastUsedAt: r.lastUsed,
			CreatedAt:  r.createdAt,
			UpdatedAt:  r.updatedAt,
		}
		existing, err := q.GetMemory(ctx, memdb.GetMemoryParams{Scope: string(scope), RepoKey: key, ID: r.id})
		switch {
		case err == nil:
			merged = mergeImported(existing, merged)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		merged.Embedding = encodeEmbedding(Embed(merged.Title + "\n" + merged.Content))
		if err := q.ImportMemory(ctx, merged); err != nil {
			return err
		}
	}
	if err := q.RecordLegacyImport(ctx, memdb.RecordLegacyImportParams{Source: src.Name, Memories: int64(len(rows))}); err != nil {
		return err
	}
	return tx.Commit()
}

// mergeImported folds an imported memory into the one the store already
// holds under the same scope, repository and id.
func mergeImported(existing memdb.Memory, in memdb.ImportMemoryParams) memdb.ImportMemoryParams {
	out := in
	if existing.UpdatedAt >= in.UpdatedAt {
		out.Category = existing.Category
		out.Title = existing.Title
		out.Content = existing.Content
	}
	out.Pinned = boolToInt(existing.Pinned != 0 || in.Pinned != 0)
	out.UseCount = existing.UseCount + in.UseCount
	out.LastUsedAt = max(existing.LastUsedAt, in.LastUsedAt)
	out.CreatedAt = min(existing.CreatedAt, in.CreatedAt)
	out.UpdatedAt = max(existing.UpdatedAt, in.UpdatedAt)
	return out
}

// legacyScope is the scope an imported memory of the category gets.
func legacyScope(category string) Scope {
	return DefaultScope(Category(strings.ToLower(category)))
}

// legacyRepoKey is the repository an imported workspace's memories
// belong to.
func legacyRepoKey(ctx context.Context, src LegacySource) string {
	if src.ProjectPath == "" {
		return "workspace:" + src.Name
	}
	if _, err := os.Stat(src.ProjectPath); err != nil {
		return "path:" + filepath.Clean(src.ProjectPath)
	}
	return RepoKey(ctx, src.ProjectPath)
}
