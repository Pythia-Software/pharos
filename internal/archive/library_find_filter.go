package archive

import (
	"context"
	"encoding/json"
	"fmt"
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
// hit in the batch, and how many matching messages, file changes, or URLs.
type libraryFindMatch struct {
	Count int        `json:"count"`
	Hit   libraryHit `json:"hit"`
}

type libraryFindSet struct {
	matches map[string]*libraryFindMatch
	limited bool
	search  textSearchResult
}

// libraryFindSummary tells a page of rows how the keyword search went.
type libraryFindSummary struct {
	Workspaces int               `json:"workspaces"`
	Limited    bool              `json:"limited"`
	Search     *textSearchResult `json:"search,omitempty"`
}

// libraryFindCache keeps up to eight searches until the next commit: a page
// asks for rows, metrics, and column stats concurrently, each with its scope.
type libraryFindCache struct {
	mu       sync.Mutex
	version  int64
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
	var version int64
	var versionErr error
	for {
		version, versionErr = c.catalogVersion(ctx)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		cache.mu.Lock()
		if versionErr == nil && cache.entries[key] != nil && cache.version == version {
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
	set := &libraryFindSet{matches: map[string]*libraryFindMatch{}, limited: limited, search: search}
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
	// A page withheld because its overlap check timed out is not the answer
	// for this catalog version; keep it out so a retry searches again.
	if versionErr == nil && search.StopReason != "consistency_check_timeout" {
		cache.mu.Lock()
		if cache.version != version || len(cache.entries) >= 8 {
			cache.entries = nil
		}
		if cache.entries == nil {
			cache.entries = map[string]*libraryFindSet{}
		}
		cache.entries[key] = set
		cache.version = version
		cache.mu.Unlock()
	}
	return set, nil
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
	search := librarySearch(ctx, values)
	search.evidence = evidence
	rows, err := s.Catalog.searchRows(search, fields)
	if err != nil {
		return nil, nil, err
	}
	options, ok, err := libraryFindRequest(values)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return rows, nil, nil
	}
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
	// Resolve every eligible workspace before search candidates are capped.
	// A nil scope means all; an explicit empty scope means none.
	options.Search.WorkspaceIDs = make([]string, 0, len(rows))
	for _, row := range rows {
		options.Search.WorkspaceIDs = append(options.Search.WorkspaceIDs, firstString(row["id"]))
	}
	slices.Sort(options.Search.WorkspaceIDs)
	set, err := s.Catalog.libraryFindMatches(ctx, options)
	if err != nil {
		return nil, nil, err
	}
	// searchRows returns a slice of its own, so it can be filtered in place.
	return slices.DeleteFunc(rows, func(row map[string]any) bool { return set.matches[firstString(row["id"])] == nil }), set, nil
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
