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
	"unicode/utf16"
	"unicode/utf8"

	qt "github.com/Pythia-Software/query-table/backends/go"

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
	// The search's spelling options, to mark its matches in each snippet.
	caseSensitive, separators bool
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
	set := &libraryFindSet{matches: map[string]*libraryFindMatch{}, limited: limited, search: search, cacheVersion: version, caseSensitive: o.CaseSensitive, separators: o.Separators}
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
	// A withheld page or a deadline without progress is not a reusable batch,
	// nor are incomplete workspace rows, which Find reports as an error. A
	// retry after transient load must be able to search again.
	incomplete := o.WorkspaceRows && (limited || search.Strategy != "" && !search.Complete)
	if versionErr == nil && !incomplete && search.StopReason != "consistency_check_timeout" && search.StopReason != "time_budget_no_progress" {
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
		return nil, nil, &qt.PlanDiagnostic{Code: qt.ErrorCodeResourceLimit, Message: fmt.Sprintf("search is incomplete (%s); workspace rows, counts and metrics are withheld because Limit applies after filtering and sorting. Narrow the search or retry", set.search.StopReason)}
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

// highlight marks the query's matches in a hit's snippet. The snippet is an
// excerpt, so this locates the query's positive terms within it, rather than
// re-verifying the whole query: an excluded or distant term is not marked.
func (set *libraryFindSet) highlight(hit libraryHit) libraryHit {
	hit.Highlights = snippetHighlights(set.search.Query, hit.Snippet, set.caseSensitive, set.separators)
	return hit
}

func snippetHighlights(query *textQuery, snippet string, caseSensitive, separators bool) [][2]int {
	if snippet == "" || query == nil {
		return nil
	}
	query = query.verificationCopy()
	var spans [][2]int
	if query.Op == "literal" || query.Op == "regex" {
		if pattern, _, err := compileTextPattern(query, caseSensitive); err == nil && pattern != nil {
			for _, at := range pattern.FindAllStringIndex(snippet, 64) {
				if at[1] > at[0] {
					spans = append(spans, [2]int{at[0], at[1]})
				}
			}
		}
	} else {
		spans = query.positiveLocations(snippet, caseSensitive, separators)
	}
	return utf16Spans(snippet, mergeTextSpans(spans))
}

func (n *textQuery) positiveLocations(body string, caseSensitive, separators bool) [][2]int {
	switch n.Op {
	case "not":
		return n.Children[0].positiveLocations(body, caseSensitive, separators)
	case "and", "or", "near", "pre":
		spans := [][2]int{}
		for _, child := range n.Children {
			spans = append(spans, child.positiveLocations(body, caseSensitive, separators)...)
		}
		return spans
	default:
		return n.strictLocations(body, caseSensitive, separators, 64)
	}
}

// mergeTextSpans sorts byte spans and joins overlapping ones.
func mergeTextSpans(spans [][2]int) [][2]int {
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	merged := [][2]int{}
	for _, span := range spans {
		if last := len(merged) - 1; last >= 0 && span[0] <= merged[last][1] {
			merged[last][1] = max(merged[last][1], span[1])
		} else {
			merged = append(merged, span)
		}
	}
	return merged
}

// utf16Spans converts byte offsets in text to UTF-16 offsets.
func utf16Spans(text string, spans [][2]int) [][2]int {
	if len(spans) == 0 {
		return nil
	}
	result := make([][2]int, len(spans))
	units, at := 0, 0
	advance := func(to int) int {
		for at < to && at < len(text) {
			r, size := utf8.DecodeRuneInString(text[at:])
			at += size
			units += utf16.RuneLen(r)
		}
		return units
	}
	for i, span := range spans {
		result[i] = [2]int{advance(span[0]), advance(span[1])}
	}
	return result
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
			projection := projectMessageLexicalText(firstString(message["kind"]), body)
			lo, hi := lexicalExcerpt(projection.text, query)
			lo, hi = projection.sourceOffset(lo), projection.sourceOffset(hi)
			evidence[firstString(message["id"])] = libraryHit{Title: firstString(message["title"]), Provider: firstString(message["provider"]), StartedAt: firstString(message["started_at"]), Role: firstString(message["role"]), MessageKind: firstString(message["kind"]), Snippet: searchSnippet(body, lo, hi)}
		}
	}
	set.attach(rows)
	for _, row := range rows {
		if hit, ok := row["find_hit"].(libraryHit); ok {
			if found, exists := evidence[hit.MessageID]; exists {
				hit.Title, hit.Provider, hit.StartedAt = found.Title, found.Provider, found.StartedAt
				hit.Role, hit.MessageKind, hit.Snippet = found.Role, found.MessageKind, found.Snippet
			}
			row["find_hit"] = set.highlight(hit)
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
