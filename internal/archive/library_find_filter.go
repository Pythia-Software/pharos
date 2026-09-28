package archive

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// A Library query-table request may carry a keyword search (find=, with
// find_kind=text|file|url and the text options find_fuzzy=0, find_case=1,
// find_separators=1). It narrows the rows to the work LibraryFind hits, so the
// table's filters, metrics, and both result views apply to the matches.

// libraryFindMatch is what a keyword search found in one workspace: its most
// relevant hit, and how many matching messages, file changes, or URLs.
type libraryFindMatch struct {
	Count int        `json:"count"`
	Hit   libraryHit `json:"hit"`
}

type libraryFindSet struct {
	matches map[string]*libraryFindMatch
	limited bool
}

// libraryFindSummary tells a page of rows how the keyword search went.
type libraryFindSummary struct {
	Workspaces int  `json:"workspaces"`
	Limited    bool `json:"limited"`
}

// libraryFindCache keeps the last search's matches until the next commit:
// a page asks for rows, metrics, and column stats at once, each with it.
type libraryFindCache struct {
	mu      sync.Mutex
	key     string
	version int64
	set     *libraryFindSet
}

func libraryFindRequest(values url.Values) (LibraryFindOptions, bool) {
	options := LibraryFindOptions{
		Kind: values.Get("find_kind"), Query: strings.TrimSpace(values.Get("find")),
		Fuzzy: values.Get("find_fuzzy") != "0", CaseSensitive: values.Get("find_case") == "1", Separators: values.Get("find_separators") == "1",
	}
	return options, options.Query != ""
}

func (c *Catalog) libraryFindMatches(ctx context.Context, o LibraryFindOptions) (*libraryFindSet, error) {
	cache := &c.find
	cache.mu.Lock()
	defer cache.mu.Unlock()
	version, versionErr := c.catalogVersion(ctx)
	key := strings.Join([]string{o.Kind, o.Query, boolFlag(o.Fuzzy), boolFlag(o.CaseSensitive), boolFlag(o.Separators)}, "\x00")
	if versionErr == nil && cache.set != nil && cache.key == key && cache.version == version {
		return cache.set, nil
	}
	hits, limited, err := c.libraryFindHits(ctx, o)
	if err != nil {
		return nil, err
	}
	set := &libraryFindSet{matches: map[string]*libraryFindMatch{}, limited: limited}
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
	if versionErr == nil {
		cache.key, cache.version, cache.set = key, version, set
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
func (s *Server) libraryRows(ctx context.Context, values url.Values, fields libraryFields, evidence *map[string]*searchEvidence) ([]map[string]any, *libraryFindSet, error) {
	search := librarySearch(ctx, values)
	search.evidence = evidence
	rows, err := s.Catalog.searchRows(search, fields)
	if err != nil {
		return nil, nil, err
	}
	options, ok := libraryFindRequest(values)
	if !ok {
		return rows, nil, nil
	}
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
