package memory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/stubbedev/harness/internal/fuzzyrank"
)

const (
	// searchLimit caps how many memories a search returns.
	searchLimit = 8
	// ftsCandidates is how many FTS5 hits feed the fusion stage; the
	// deeper the candidate list, the more the embeddings can rerank.
	ftsCandidates = 25
	// semWeight scales the embedding cosine added to the lexical
	// score, which is normalized to (0, 1]. At parity a top lexical
	// hit with a weak cosine still wins; a strong paraphrase can
	// outrank a weak lexical one.
	semWeight = 1.0
	// minFillCosine is the cosine below which an embedding-only match
	// is considered noise and dropped.
	minFillCosine = 0.35
)

// search returns the most relevant memories for the query. Lexical
// matching runs in FTS5 (BM25 over stemmed tokens, title weighted
// above content), a hashed-subword embedding reranks and fills in
// paraphrases, and fzf's subsequence matcher is the last-resort net
// for typos no other layer catches. Results are relevance-ordered and
// carry a content snippet; each returned memory is marked used.
func (s *service) Search(ctx context.Context, query string) ([]Item, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	items, vectors, err := s.listWithVectors(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}

	byID := make(map[string]Item, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}

	candidates, err := s.searchFTSLadder(ctx, query)
	if err != nil {
		return nil, err
	}

	type result struct {
		item  Item
		score float64
	}
	results := make(map[string]result, len(candidates))
	qvec := Embed(query)
	for rank, hit := range candidates {
		item, ok := byID[hit.id]
		if !ok {
			continue
		}
		item.Snippet = hit.snippet
		byID[hit.id] = item
		lexical := float64(len(candidates)-rank) / float64(len(candidates))
		results[hit.id] = result{item: item, score: lexical + semWeight*cosine(qvec, vectors[hit.id])}
	}
	// Embedding fill: memories the lexical layer never saw but the
	// semantics clearly match. This is the paraphrase and typo path.
	if len(results) == 0 || len(results) < searchLimit {
		filler := make([]string, 0, len(items))
		for _, item := range items {
			if _, seen := results[item.ID]; seen {
				continue
			}
			if cosine(qvec, vectors[item.ID]) >= minFillCosine {
				filler = append(filler, item.ID)
			}
		}
		slices.SortFunc(filler, func(a, b string) int {
			return cmp.Compare(cosine(qvec, vectors[b]), cosine(qvec, vectors[a]))
		})
		for _, id := range filler {
			if len(results) >= searchLimit {
				break
			}
			results[id] = result{item: byID[id], score: semWeight * cosine(qvec, vectors[id])}
		}
	}

	if len(results) == 0 {
		for _, item := range items {
			res, ok := fuzzyrank.Match(query, fuzzyrank.Fields{Primary: item.Title, Rest: item.Content})
			if !ok {
				continue
			}
			results[item.ID] = result{item: item, score: float64(res.Score)}
		}
	}

	ordered := make([]result, 0, len(results))
	for _, r := range results {
		ordered = append(ordered, r)
	}
	slices.SortFunc(ordered, func(a, b result) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return cmp.Compare(a.item.ID, b.item.ID)
	})
	if len(ordered) > searchLimit {
		ordered = ordered[:searchLimit]
	}

	items = make([]Item, 0, len(ordered))
	now := time.Now().Unix()
	for _, r := range ordered {
		r.item.UseCount++
		r.item.LastUsedAt = now
		items = append(items, r.item)
		s.touch(ctx, r.item.ID)
	}
	return items, nil
}

// listWithVectors loads every memory with its embedding, computing
// embeddings on the fly for rows saved before they existed. Returned
// vectors are always unit vectors (zero vectors for empty text).
func (s *service) listWithVectors(ctx context.Context) ([]Item, map[string][]float32, error) {
	rows, err := s.q.ListMemories(ctx)
	if err != nil {
		return nil, nil, err
	}
	items := make([]Item, 0, len(rows))
	vectors := make(map[string][]float32, len(rows))
	for _, row := range rows {
		vec := decodeEmbedding(row.Embedding)
		if vec == nil {
			vec = Embed(row.Title + "\n" + row.Content)
		}
		item := fromDB(row)
		items = append(items, item)
		vectors[item.ID] = vec
	}
	return items, vectors, nil
}

// ftsHit is one FTS5 candidate before fusion.
type ftsHit struct {
	id      string
	snippet string
}

// ftsMode is one rung of the full-text search ladder.
type ftsMode struct {
	prefix bool
	op     string
}

// searchFTSLadder runs the FTS5 ladder from precise to loose, stopping
// at the first rung that returns candidates: exact terms ANDed, exact
// terms ORed (bm25 ranks matches with more rare terms first), then the
// same two with term prefixes. The OR rungs keep one stopword miss or
// one typo from emptying the result set.
func (s *service) searchFTSLadder(ctx context.Context, query string) ([]ftsHit, error) {
	for _, mode := range []ftsMode{{op: ""}, {op: " OR "}, {prefix: true, op: ""}, {prefix: true, op: " OR "}} {
		hits, err := s.searchFTS(ctx, ftsMatch(query, mode))
		if err != nil {
			return nil, err
		}
		if len(hits) > 0 {
			return hits, nil
		}
	}
	return nil, nil
}

// searchFTS runs one FTS5 MATCH query and returns its candidates in
// bm25 order. The query is hand-written rather than sqlc-managed
// because sqlc cannot parse the FTS5 virtual table's MATCH syntax,
// implicit rank column, or snippet() helper.
func (s *service) searchFTS(ctx context.Context, match string) ([]ftsHit, error) {
	match = strings.TrimSpace(match)
	if match == "" {
		return nil, nil
	}
	const q = `SELECT m.id,
       snippet(memories_fts, 1, '', '', ' ... ', 12) AS snippet
FROM memories m
JOIN memories_fts ON memories_fts.rowid = m.rowid
WHERE memories_fts MATCH ?
ORDER BY bm25(memories_fts, 3.0, 1.0), m.id
LIMIT ?`
	rows, err := s.conn.QueryContext(ctx, q, match, ftsCandidates)
	if err != nil {
		return nil, fmt.Errorf("searching memories: %w", err)
	}
	defer rows.Close()
	hits := make([]ftsHit, 0, ftsCandidates)
	for rows.Next() {
		var hit ftsHit
		if err := rows.Scan(&hit.id, &hit.snippet); err != nil {
			return nil, err
		}
		hit.snippet = strings.TrimSpace(hit.snippet)
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

// ftsMatch renders the query as an FTS5 MATCH expression: each
// whitespace-separated term becomes a quoted token joined by the
// mode's operator (empty means FTS5's implicit AND). FTS5 syntax
// characters are stripped so model input cannot change the query's
// structure. With prefix set, every term gains a trailing * so "diag"
// matches "diagnostics".
func ftsMatch(query string, mode ftsMode) string {
	quoted := make([]string, 0, strings.Count(query, " ")+1)
	for term := range strings.FieldsSeq(query) {
		term = strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return r
			}
			return -1
		}, term)
		if term == "" {
			continue
		}
		if mode.prefix {
			term += "*"
		}
		quoted = append(quoted, `"`+term+`"`)
	}
	return strings.Join(quoted, mode.op)
}
