package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/gbdubs/pharos/internal/querytable"
)

// A Library query-table request may carry a keyword search (find=, with
// find_kind=text|file|url and the text options find_fuzzy=0, find_case=1,
// find_separators=1). It narrows the rows to the work LibraryFind hits, so the
// table's filters, metrics, and both result views apply to the matches.

// libraryFindMatch is what a keyword search found in one workspace: its first
// hit, and how many matching messages, file changes, or URLs.
type libraryFindMatch struct {
	Count int        `json:"count"`
	Hit   libraryHit `json:"hit"`
}

type libraryFindSet struct {
	matches      map[string]*libraryFindMatch
	limited      bool
	search       textSearchResult
	cacheVersion string
}

// libraryFindSummary tells a page of rows how the keyword search went.
type libraryFindSummary struct {
	Workspaces int               `json:"workspaces"`
	Limited    bool              `json:"limited"`
	Search     *textSearchResult `json:"search,omitempty"`
}

// libraryFindCache keeps up to eight searches until their inputs change: a page
// asks for rows, metrics, and column stats concurrently, each with its scope.
type libraryFindCache struct {
	mu       sync.Mutex
	inflight map[string]chan struct{}
	entries  map[string]*libraryFindSet
}

func libraryFindRequest(values url.Values) (LibraryFindOptions, bool, error) {
	search, err := textOptionsFromValues(values, "find_")
	options := LibraryFindOptions{
		Kind: values.Get("find_kind"), Query: strings.TrimSpace(values.Get("find")),
		Search: search,
		Fuzzy:  values.Get("find_fuzzy") != "0", CaseSensitive: values.Get("find_case") == "1", Separators: values.Get("find_separators") == "1",
	}
	return options, options.Query != "" || options.Search.Where != nil, err
}

func (c *Catalog) libraryFindMatches(ctx context.Context, o LibraryFindOptions) (*libraryFindSet, error) {
	cache := &c.find
	encoded, _ := json.Marshal(o)
	key := string(encoded)
	var version string
	var versionErr error
	for {
		version, versionErr = c.libraryFindVersion(ctx, o)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		cache.mu.Lock()
		if versionErr == nil && cache.entries[key] != nil && cache.entries[key].cacheVersion == version {
			set := cache.entries[key]
			cache.mu.Unlock()
			return set, nil
		}
		if pending := cache.inflight[key]; pending != nil {
			cache.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-pending:
			}
			continue
		}
		if cache.inflight == nil {
			cache.inflight = map[string]chan struct{}{}
		}
		cache.inflight[key] = make(chan struct{})
		cache.mu.Unlock()
		break
	}
	defer func() { cache.mu.Lock(); close(cache.inflight[key]); delete(cache.inflight, key); cache.mu.Unlock() }()
	var search textSearchResult
	o.searchResult = &search
	hits, limited, err := c.libraryFindHits(ctx, o)
	if err != nil {
		return nil, err
	}
	set := &libraryFindSet{matches: map[string]*libraryFindMatch{}, limited: limited, search: search, cacheVersion: version}
	for _, hit := range hits {
		match := set.matches[hit.WorkspaceID]
		if match == nil {
			match = &libraryFindMatch{Hit: hit}
			set.matches[hit.WorkspaceID] = match
		}
		// Text hits are grouped by conversation and count its matching
		// messages; file and URL hits count themselves.
		if o.Kind == "" || o.Kind == "text" {
			match.Count += hit.Count
		} else {
			match.Count++
		}
	}
	// A withheld page or a deadline without progress is not a reusable batch.
	// A retry after transient load must be able to search again.
	if versionErr == nil && (!o.WorkspaceRows || !limited) && search.StopReason != "consistency_check_timeout" && search.StopReason != "time_budget_no_progress" {
		cache.mu.Lock()
		if len(cache.entries) >= 8 {
			cache.entries = nil
		}
		if cache.entries == nil {
			cache.entries = map[string]*libraryFindSet{}
		}
		cache.entries[key] = set
		cache.mu.Unlock()
	}
	return set, nil
}

// Text matches do not depend on analysis projections, rollups, or an ingest
// observing the same inputs again. Keep rows/metrics requests on the same
// batch during that background work. Initial searches also watch appends,
// even though continuation cursors intentionally tolerate newer messages.
func (c *Catalog) libraryFindVersion(ctx context.Context, o LibraryFindOptions) (string, error) {
	if o.Kind != "" && o.Kind != "text" {
		version, err := c.catalogVersion(ctx)
		return fmt.Sprint(version), err
	}
	watch := o.Search
	watch.Fuzzy = true
	version, err := c.textSearchGeneration(ctx, watch)
	if err != nil {
		return "", err
	}
	var readiness string
	err = c.reader().QueryRowContext(ctx, `SELECT
        COALESCE((SELECT value FROM meta WHERE key='text_search_documents_version'),'') || ':' ||
        COALESCE((SELECT value FROM meta WHERE key='message_trigram_version'),'') || ':' ||
        CASE WHEN ? THEN COALESCE((SELECT value FROM meta WHERE key='reasoning_extraction_generation'),'0') ELSE '-' END`,
		o.Search.Surface == "reasoning").Scan(&readiness)
	return version + ":" + readiness, err
}

func boolFlag(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// libraryRows returns the Library rows a request selects: its semantic
// ?search=, then its keyword find=. The set is nil without a keyword search.
func (s *Server) libraryRows(ctx context.Context, values url.Values, fields libraryFields, evidence *map[string]*searchEvidence, where ...[]querytable.WhereTerm) ([]map[string]any, *libraryFindSet, error) {
	options, ok, err := libraryFindRequest(values)
	if err != nil {
		return nil, nil, err
	}
	// Pricing the whole Library before keyword restriction costs a ledger
	// scan. Cost predicates need prices to resolve the eligible population;
	// sorting and aggregation can price just the matching workspaces.
	costsAfterFind := ok && fields.hasAny(libraryCostFields)
	for _, terms := range where {
		for _, term := range terms {
			for _, predicate := range term.Predicates() {
				if slices.Contains(libraryCostFields, predicate.Field) {
					costsAfterFind = false
				}
			}
		}
	}
	readFields := fields
	if costsAfterFind {
		readFields = maps.Clone(fields)
		if readFields == nil {
			readFields = libraryKnownFields()
		}
		for _, field := range libraryCostFields {
			delete(readFields, field)
		}
	}
	search := librarySearch(ctx, values)
	search.evidence = evidence
	rows, err := s.Catalog.searchRows(search, readFields)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return rows, nil, nil
	}
	options.WorkspaceRows = true
	if len(where) > 0 && len(where[0]) > 0 {
		schema, e := s.queryTableSchema("library")
		if e != nil {
			return nil, nil, e
		}
		rows, e = querytable.Filter(rows, where[0], schema)
		if e != nil {
			return nil, nil, e
		}
	}
	// Resolve every eligible workspace before establishing keyword membership.
	// A nil scope means all; an explicit empty scope means none.
	options.Search.WorkspaceIDs = make([]string, 0, len(rows))
	for _, row := range rows {
		options.Search.WorkspaceIDs = append(options.Search.WorkspaceIDs, firstString(row["id"]))
	}
	slices.Sort(options.Search.WorkspaceIDs)
	set, err := s.Catalog.libraryFindMatches(ctx, options)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, fmt.Errorf("workspace search timed out before the complete population was established; narrow the search or retry: %w", err)
		}
		return nil, nil, err
	}
	if set.limited || set.search.Strategy != "" && !set.search.Complete {
		return nil, nil, fmt.Errorf("search is incomplete (%s); workspace rows, counts and metrics are withheld because Limit applies after filtering and sorting. Narrow the search or retry", set.search.StopReason)
	}
	// searchRows returns a slice of its own, so it can be filtered in place.
	rows = slices.DeleteFunc(rows, func(row map[string]any) bool { return set.matches[firstString(row["id"])] == nil })
	if costsAfterFind {
		// Cached row maps are shared across requests.
		for i, row := range rows {
			rows[i] = maps.Clone(row)
		}
		if err := s.Catalog.attachWorkspaceCosts(ctx, rows); err != nil {
			return nil, nil, err
		}
	}
	return rows, set, nil
}

// attach adds each page row's keyword match, for the result views
// to show and open. Page rows are the request's own copies.
func (set *libraryFindSet) attach(rows []map[string]any) {
	for _, row := range rows {
		if match := set.matches[firstString(row["id"])]; match != nil {
			row["find_match_count"] = match.Count
			row["find_hit"] = match.Hit
		}
	}
}

// The word index establishes membership without loading matching bodies.
// Fetch evidence only for the final page, after the table's filters/sort/Limit.
func (c *Catalog) attachLibraryFind(ctx context.Context, set *libraryFindSet, rows []map[string]any) error {
	ids := []string{}
	for _, row := range rows {
		if match := set.matches[firstString(row["id"])]; match != nil && match.Hit.deferredEvidence {
			ids = append(ids, match.Hit.MessageID)
		}
	}
	evidence := map[string]libraryHit{}
	if len(ids) > 0 {
		messages, err := queryMapsContext(ctx, c.reader(), `SELECT m.id,m.role,m.kind,c.provider,c.started_at,w.title,
 CASE WHEN COALESCE(d.byte_length,length(CAST(m.text AS BLOB)))<=1048576 THEN m.text ELSE '' END body
 FROM messages m JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id
 LEFT JOIN text_search_documents d ON d.message_rowid=m.rowid WHERE m.id IN (SELECT value FROM json_each(?))`, jsonText(ids))
		if err != nil {
			return err
		}
		query := set.search.Query.verificationCopy()
		for _, message := range messages {
			body := firstString(message["body"])
			lo, hi := lexicalExcerpt(body, query)
			evidence[firstString(message["id"])] = libraryHit{Title: firstString(message["title"]), Provider: firstString(message["provider"]), StartedAt: firstString(message["started_at"]), Role: firstString(message["role"]), MessageKind: firstString(message["kind"]), Snippet: searchSnippet(body, lo, hi)}
		}
	}
	set.attach(rows)
	for _, row := range rows {
		if hit, ok := row["find_hit"].(libraryHit); ok {
			if found, exists := evidence[hit.MessageID]; exists {
				hit.Title, hit.Provider, hit.StartedAt = found.Title, found.Provider, found.StartedAt
				hit.Role, hit.MessageKind, hit.Snippet = found.Role, found.MessageKind, found.Snippet
				row["find_hit"] = hit
			}
		}
	}
	return nil
}

func (set *libraryFindSet) summary() libraryFindSummary {
	summary := libraryFindSummary{Workspaces: len(set.matches), Limited: set.limited}
	if set.search.Strategy != "" {
		copy := set.search
		copy.Items = nil
		summary.Search = &copy
	}
	return summary
}

// Human Words searches intersect actual message IDs, never workspace IDs.
// Table filters run after this stable search batch; metadata requests use the
// same batch and cursor. No query-table filter changes the cursor fingerprint.
func (c *Catalog) messageFindDataset(ctx context.Context, values url.Values, table sqlDataset) (sqlDataset, *libraryFindSet, error) {
	options, active, err := libraryFindRequest(values)
	if err != nil || !active {
		return table, nil, err
	}
	surface := options.Search.Surface
	if surface != "user" && !slices.Contains([]string{"typed", "pasted", "quoted", "resent", "template", "harness", "automated", "attachment"}, surface) {
		return table, nil, fmt.Errorf("Human Words searches require a people-message or classified-span target")
	}
	if options.Kind != "" && options.Kind != "text" {
		return table, nil, fmt.Errorf("Human Words searches require a text query")
	}
	set, err := c.libraryFindMatches(ctx, options)
	if err != nil {
		return table, nil, err
	}
	ids := make([]string, 0, len(set.search.Items))
	for _, item := range set.search.Items {
		// Literals here are data, not SQL identifiers. Escape every quote.
		ids = append(ids, "'"+strings.ReplaceAll(item.MessageID, "'", "''")+"'")
	}
	predicate := "0=1"
	if len(ids) > 0 {
		predicate = "ma.message_id IN (" + strings.Join(ids, ",") + ")"
	}
	if table.base != "" {
		table.base = "(" + table.base + ") AND " + predicate
	} else {
		table.base = predicate
	}
	return table, set, nil
}
