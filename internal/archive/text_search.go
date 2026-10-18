package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// TextSearchOptions is shared by HTTP, Library Find, and MCP search_text.
// Searches enumerate messages in stable reverse index order, not relevance.
// A cursor resumes work, including pages with zero matches. It is invalidated
// by relevant edits, deletes, or index reordering. Ordinary appends are safe;
// fuzzy searches also watch vocabulary changes and spans watch classification.
type TextSearchOptions struct {
	Query              string     `json:"query"`
	Mode               string     `json:"mode,omitempty"`
	Where              *textQuery `json:"where,omitempty"`
	Surface            string     `json:"surface,omitempty"`
	Repository         string     `json:"repository,omitempty"`
	ConversationID     string     `json:"conversation_id,omitempty"`
	WorkspaceID        string     `json:"workspace_id,omitempty"`
	WorkspaceIDs       []string   `json:"workspace_ids,omitempty"`
	Source             string     `json:"source,omitempty"`
	Provider           string     `json:"provider,omitempty"`
	Roles              []string   `json:"roles,omitempty"`
	Kinds              []string   `json:"kinds,omitempty"`
	From               string     `json:"from,omitempty"`
	To                 string     `json:"to,omitempty"`
	CaseSensitive      bool       `json:"case_sensitive,omitempty"`
	Separators         bool       `json:"separators,omitempty"`
	FuzzyPrefixLength  int        `json:"fuzzy_prefix_length,omitempty"`
	FuzzyMaxEdits      int        `json:"fuzzy_max_edits,omitempty"`
	FuzzyMaxExpansions int        `json:"fuzzy_max_expansions,omitempty"`
	Fuzzy              bool       `json:"fuzzy,omitempty"`
	SelectedOnly       bool       `json:"selected_only,omitempty"`
	Cursor             string     `json:"cursor,omitempty"`
	Limit              int        `json:"limit,omitempty"`
	MaxCandidates      int        `json:"max_candidates,omitempty"`
	MaxBytes           int        `json:"max_bytes,omitempty"`
	TimeoutMS          int        `json:"timeout_ms,omitempty"`
}

type textCapture struct {
	Group     int    `json:"group"`
	Name      string `json:"name,omitempty"`
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
}
type textSearchItem struct {
	RangeKind string `json:"range_kind"`
	libraryHit
	CreatedAt       string        `json:"created_at,omitempty"`
	ContentHash     string        `json:"content_hash"`
	EvidenceLocator string        `json:"evidence_locator,omitempty"`
	StartByte       int           `json:"start_byte"`
	EndByte         int           `json:"end_byte"`
	SpanCategory    string        `json:"span_category,omitempty"`
	Captures        []textCapture `json:"captures,omitempty"`
	CapturesClipped bool          `json:"captures_clipped,omitempty"`
	// Cursor after this result, before any later verification work. Used when
	// an MCP output budget trims a page, so no matches are silently skipped.
	cursor string
}
type textSearchResult struct {
	Items           []textSearchItem    `json:"items"`
	NextCursor      string              `json:"next_cursor"`
	Complete        bool                `json:"complete"`
	StopReason      string              `json:"stop_reason"`
	StopMessage     string              `json:"stop_message,omitempty"`
	Count           int                 `json:"count"`
	CountRelation   string              `json:"count_relation"`
	Strategy        string              `json:"strategy"`
	Candidates      int                 `json:"candidates"`
	VerifiedBytes   int                 `json:"verified_bytes"`
	SkippedOversize int                 `json:"skipped_oversize"`
	ElapsedMS       int64               `json:"elapsed_ms"`
	Query           *textQuery          `json:"interpreted_query"`
	Expansions      map[string][]string `json:"expansions,omitempty"`
	Surface         string              `json:"surface"`
	Coverage        string              `json:"coverage"`
	Generation      string              `json:"generation"`
}
type textSearchCursor struct {
	Version     int    `json:"v"`
	Fingerprint string `json:"q"`
	Generation  string `json:"g"`
	Before      int64  `json:"b"`
	Count       int    `json:"n"`
	Skipped     int    `json:"s"`
}

var textSearchEpoch = fmt.Sprint(time.Now().UnixNano())

func (c *Catalog) textSearchGeneration(ctx context.Context, o TextSearchOptions) (string, error) {
	var generation string
	err := c.reader().QueryRowContext(ctx, `SELECT
 CASE WHEN EXISTS(SELECT 1 FROM meta WHERE key='text_search_cursor_version' AND value='2') THEN
 '2:' || value || ':' || CASE WHEN ? THEN COALESCE((SELECT value FROM meta WHERE key='text_search_span_generation'),'0') ELSE '-' END ||
 ':' || CASE WHEN ? THEN COALESCE((SELECT value FROM meta WHERE key='text_search_append_generation'),'0') ELSE '-' END
 ELSE '1:' || value END FROM meta WHERE key='text_search_generation'`, textSpanSurface(o.Surface), o.Fuzzy).Scan(&generation)
	if err == sql.ErrNoRows {
		v, e := c.catalogVersion(ctx)
		return fmt.Sprintf("%s:%d", textSearchEpoch, v), e
	}
	return generation, err
}
func encodeTextCursor(cursor textSearchCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// A timeout without a consumed position cannot be resumed safely. In
// particular, SQL may spend the entire budget rejecting candidates before it
// yields a row. Do not give callers an identical cursor to retry indefinitely.
func (r *textSearchResult) stopPage(cursor textSearchCursor, before int64) {
	if cursor.Before < before {
		r.NextCursor = encodeTextCursor(cursor)
		return
	}
	r.NextCursor = ""
	r.StopReason = "time_budget_no_progress"
	r.StopMessage = "The time budget expired before a resumable position was reached. Narrow the search or increase the time budget and retry; this is not a complete result."
}
func validateTextTree(n *textQuery, depth int, count *int) error {
	if n == nil {
		return fmt.Errorf("empty query node")
	}
	*count++
	if depth > 16 || *count > 128 {
		return fmt.Errorf("query tree exceeds depth 16 or 128 nodes")
	}
	arity := 0
	switch n.Op {
	case "term", "phrase", "prefix", "literal", "regex":
		if n.Text == "" || len(n.Text) > 2000 {
			return fmt.Errorf("query text needs 1–2,000 bytes")
		}
		if n.Op != "literal" && n.Op != "regex" && len(auditQueryWords(n.Text)) == 0 {
			return fmt.Errorf("token clause needs a word")
		}
	case "and", "or":
		arity = 2
	case "not", "near", "pre":
		arity = 2
		if len(n.Children) != 2 {
			return fmt.Errorf("%s needs two children", n.Op)
		}
	default:
		return fmt.Errorf("unknown query operation %q", n.Op)
	}
	if len(n.Children) < arity || (arity == 0 && len(n.Children) != 0) {
		return fmt.Errorf("invalid children for %s", n.Op)
	}
	for _, child := range n.Children {
		if child != nil && (child.Op == "regex" || child.Op == "literal") {
			return fmt.Errorf("regex and literal must be the root query; combine scope with filters")
		}
		if err := validateTextTree(child, depth+1, count); err != nil {
			return err
		}
	}
	if n.Op == "near" || n.Op == "pre" {
		if n.Distance < 0 || n.Distance > 100 {
			return fmt.Errorf("NEAR distance must be 0–100")
		}
		for _, child := range n.Children {
			if child.Op != "term" && child.Op != "phrase" && child.Op != "prefix" {
				return fmt.Errorf("NEAR needs word or phrase children")
			}
		}
	}
	return nil
}
func normalizeTextOptions(o *TextSearchOptions) error {
	if o.FuzzyPrefixLength == 0 {
		o.FuzzyPrefixLength = 3
	}
	o.FuzzyPrefixLength = clamp(o.FuzzyPrefixLength, 2, 8)
	if o.FuzzyMaxExpansions == 0 {
		o.FuzzyMaxExpansions = 24
	}
	o.FuzzyMaxExpansions = clamp(o.FuzzyMaxExpansions, 1, 64)
	o.FuzzyMaxEdits = clamp(o.FuzzyMaxEdits, 0, 2)
	if o.Mode == "" {
		o.Mode = "advanced"
	}
	if o.Surface == "" {
		o.Surface = "prose"
	}
	if o.Limit == 0 {
		o.Limit = 50
	}
	o.Limit = clamp(o.Limit, 1, 5000)
	if o.MaxCandidates == 0 {
		o.MaxCandidates = 10000
	}
	o.MaxCandidates = clamp(o.MaxCandidates, 1, 50000)
	if o.MaxBytes == 0 {
		o.MaxBytes = 16 << 20
	}
	o.MaxBytes = clamp(o.MaxBytes, 1, 64<<20)
	if o.TimeoutMS == 0 {
		o.TimeoutMS = 1500
	}
	o.TimeoutMS = clamp(o.TimeoutMS, 1, 5000)
	switch o.Surface {
	case "prose", "user", "assistant", "reasoning", "tools", "all", "typed", "pasted", "quoted", "resent", "template", "harness", "automated", "attachment":
	default:
		return fmt.Errorf("unknown search surface %q", o.Surface)
	}
	for _, pair := range []struct{ name, value string }{{"from", o.From}, {"to", o.To}} {
		if pair.value != "" {
			if _, ok := parseTime(strings.TrimSpace(pair.value)); !ok {
				return fmt.Errorf("%s must be a date or timestamp", pair.name)
			}
		}
	}
	if o.From != "" && o.To != "" && timeBound(o.From, false) > timeBound(o.To, true) {
		return fmt.Errorf("from must not be later than to")
	}
	_, _, err := messageFilters(textFilterArgs(*o), "m.", false)
	return err
}
func textFilterArgs(o TextSearchOptions) map[string]any {
	a := map[string]any{}
	if o.Roles != nil {
		a["roles"] = o.Roles
	}
	if o.Kinds != nil {
		a["kinds"] = o.Kinds
	}
	return a
}
func textSpanSurface(s string) bool {
	return slices.Contains([]string{"typed", "pasted", "quoted", "resent", "template", "harness", "automated", "attachment"}, s)
}
func textSurfacePredicate(s string) string {
	switch s {
	case "prose":
		return "m.kind IN ('message','thinking','reasoning','analysis') AND (m.role IN ('user','assistant') OR m.kind IN ('thinking','reasoning','analysis'))"
	case "user", "assistant":
		return "m.kind='message' AND +m.role='" + s + "'"
	case "reasoning":
		return "m.kind IN ('thinking','reasoning','analysis')"
	case "tools":
		return "m.kind IN ('tool_call','tool_result','result','delegation','delegation_result')"
	case "all":
		return "1=1"
	default:
		return "m.kind='message' AND +m.role='user'"
	}
}

func prepareTextQuery(o TextSearchOptions) (*textQuery, error) {
	var err error
	node := o.Where
	if node == nil {
		node, err = parseTextQuery(o.Query, o.Mode)
		if err != nil {
			return nil, err
		}
	} else if o.Query != "" {
		return nil, fmt.Errorf("supply query or where, not both")
	}
	count := 0
	if err = validateTextTree(node, 0, &count); err != nil {
		return nil, err
	}
	// Clone the caller's tree before expanding terms; it may be shared by requests.
	raw, _ := json.Marshal(node)
	node = nil
	_ = json.Unmarshal(raw, &node)
	return node, nil
}

func compileTextPattern(node *textQuery, caseSensitive bool) (*regexp.Regexp, int, error) {
	var verify *regexp.Regexp
	regexInstructions := 1
	scan := node.Op == "literal" || node.Op == "regex"
	if scan {
		pattern := node.Text
		if node.Op == "literal" {
			pattern = regexp.QuoteMeta(pattern)
		}
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}
		if node.Op == "regex" {
			parsed, e := syntax.Parse(pattern, syntax.Perl)
			if e != nil {
				return nil, 0, fmt.Errorf("Go/RE2 regex: %w", e)
			}
			program, e := syntax.Compile(parsed.Simplify())
			if e != nil {
				return nil, 0, e
			}
			regexInstructions = len(program.Inst)
			if regexInstructions > 1000 {
				return nil, 0, fmt.Errorf("regex exceeds 1,000 compiled instructions; simplify repetitions or alternatives")
			}
		}
		var err error
		verify, err = regexp.Compile(pattern)
		if err != nil {
			return nil, 0, fmt.Errorf("Go/RE2 regex: %w", err)
		}
	}
	return verify, regexInstructions, nil
}

// SearchText never reports an exact count after hitting a work budget. The
// original text verifier sees at most min(1 MiB, MaxBytes) per message;
// oversized records are counted and make the final count a lower bound.
func (c *Catalog) SearchText(parent context.Context, o TextSearchOptions) (result textSearchResult, err error) {
	started := time.Now()
	defer func() { result.ElapsedMS = time.Since(started).Milliseconds() }()
	result.Items = []textSearchItem{}
	result.Expansions = map[string][]string{}
	result.CountRelation = "lower_bound"
	if err = normalizeTextOptions(&o); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(o.TimeoutMS)*time.Millisecond)
	defer cancel()
	node, err := prepareTextQuery(o)
	if err != nil {
		return result, err
	}
	result.Query = node
	result.Surface = o.Surface
	result.Coverage = "Stored message text only; omitted source history is unknown. Reasoning covers explicit retained thinking/reasoning/analysis messages."
	span := textSpanSurface(o.Surface)
	if span {
		result.Coverage = "Matches individual stored authorship spans, never text outside the category. Classification may be absent or stale; search does not rebuild it."
	}
	// Read before expanding terms so a vocabulary append during expansion is
	// detected by the same generation check as an append during verification.
	generation, e := c.textSearchGeneration(ctx, o)
	if e != nil {
		return result, e
	}
	result.Generation = generation
	if err = c.expandTextQuery(ctx, node, o, result.Expansions); err != nil {
		return result, fmt.Errorf("query expansion: %w", err)
	}
	verify, regexInstructions, err := compileTextPattern(node, o.CaseSensitive)
	if err != nil {
		return result, err
	}
	scan := node.Op == "literal" || node.Op == "regex"
	strict := o.CaseSensitive || o.Separators
	ordered := node.needsOrderedVerifier()
	result.Strategy = "word_index"
	from := `messages_fts f CROSS JOIN messages m ON m.id=f.message_id`
	ready, e := c.textSearchReady(ctx)
	if e != nil {
		return result, e
	}
	if o.Surface == "reasoning" && ready {
		var n int
		e = c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM text_search_documents WHERE surface='reasoning')`).Scan(&n)
		if e != nil {
			return result, e
		}
		if n == 0 {
			result.Coverage = "No explicit thinking/reasoning/analysis messages are retained in this catalog. Embedded or discarded reasoning is not indexed as this surface."
		}
	}
	small := o.Surface == "prose" || o.Surface == "user" || o.Surface == "assistant" || o.Surface == "reasoning" || span
	if !scan && ready && small {
		from = `messages_fts f CROSS JOIN text_search_documents d ON d.message_id=f.message_id CROSS JOIN messages m ON m.rowid=d.message_rowid`
	}
	key := "f.rowid"
	match := "messages_fts MATCH ?"
	arguments := []any{node.fts(strict || span)}
	if scan {
		result.Strategy = "scoped_scan"
		from = "messages m"
		key = "m.rowid"
		match = "1=1"
		arguments = nil
		scoped := o.ConversationID != "" || o.WorkspaceID != "" || (o.WorkspaceIDs != nil && len(o.WorkspaceIDs) <= 100)
		ready, e := c.textSearchReady(ctx)
		if e != nil {
			return result, e
		}
		small := o.Surface == "prose" || o.Surface == "user" || o.Surface == "assistant" || o.Surface == "reasoning" || span
		if !scoped && (!small || !ready) {
			return result, fmt.Errorf("this scan needs a conversation or workspace filter (or a ready small-surface index); token searches can cover the full archive")
		}
		if ready && small {
			result.Strategy = "surface_scan"
			from = "text_search_documents d CROSS JOIN messages m ON m.rowid=d.message_rowid"
			key = "d.message_rowid"
			surface := o.Surface
			if span {
				surface = "user"
			}
			if surface != "prose" {
				match = "d.surface=?"
				arguments = []any{surface}
			}
		}
		// Trigrams cover messages, not reasoning or arbitrary tool output. Use only
		// mandatory case-sensitive ASCII literals, and only once backfill is complete.
		if o.Surface == "user" || o.Surface == "assistant" || span {
			literal := node.Text
			if node.Op == "regex" {
				expr, _ := syntax.Parse(verify.String(), syntax.Perl)
				literal = textRegexRequiredLiteral(expr)
			}
			if !o.CaseSensitive {
				literal = asciiFoldLiteral(literal)
			}
			ascii := true
			for _, r := range literal {
				if r > 127 {
					ascii = false
				}
			}
			progress, e := c.substringIndexProgress(ctx)
			if e != nil {
				return result, e
			}
			if ascii && utf8.RuneCountInString(literal) >= 3 && progress.Ready {
				result.Strategy = "trigram_verify"
				from = "messages_trigram f CROSS JOIN message_fts_rows fr ON fr.fts_rowid=f.rowid CROSS JOIN messages m ON m.id=fr.message_id"
				key = "f.rowid"
				match = "messages_trigram MATCH ?"
				arguments = []any{quoteFTS(literal)}
			}
		}
	}
	// Classified spans are a genuinely small corpus. On the replica, seeking
	// the global FTS index once per authored message took >5s; streaming the
	// existing surface index and tokenizing bounded spans avoids that plan.
	if span && !scan && ready {
		result.Strategy = "span_scan"
		from = "text_search_documents d CROSS JOIN messages m ON m.rowid=d.message_rowid"
		key = "d.message_rowid"
		match = "d.surface=?"
		arguments = []any{"user"}
	}
	// Hash the semantic request and the physical cursor order, never page size
	// or budgets. The catalog path prevents applying another library's cursor.
	identity := o
	identity.Cursor = ""
	identity.Limit = 0
	identity.MaxBytes = 0
	identity.MaxCandidates = 0
	identity.TimeoutMS = 0
	identity.WorkspaceIDs = append([]string(nil), o.WorkspaceIDs...)
	slices.Sort(identity.WorkspaceIDs)
	digest := sha256.Sum256([]byte(c.Path + "\x00" + fmt.Sprint(o.WorkspaceIDs != nil) + "\x00" + result.Strategy + "\x00" + jsonText(identity)))
	fingerprint := hex.EncodeToString(digest[:])
	cursor := textSearchCursor{Version: 1, Fingerprint: fingerprint, Generation: generation, Before: math.MaxInt64}
	if o.Cursor != "" {
		data, e := base64.RawURLEncoding.DecodeString(o.Cursor)
		if e != nil || len(data) > 4096 {
			return result, fmt.Errorf("invalid search cursor")
		}
		if e = json.Unmarshal(data, &cursor); e != nil || cursor.Version != 1 || cursor.Fingerprint != fingerprint || cursor.Generation != generation || cursor.Before < 0 || cursor.Count < 0 || cursor.Skipped < 0 {
			return result, fmt.Errorf("search cursor expired or query changed; restart the search")
		}
	}
	result.Count = cursor.Count
	result.SkippedOversize = cursor.Skipped
	pageBefore := cursor.Before
	clauses := []string{match, key + "<?", textSurfacePredicate(o.Surface)}
	if !scan && ready && small && o.Surface != "prose" {
		surface := o.Surface
		if span {
			surface = "user"
		}
		clauses = append(clauses, "d.surface="+quoteSQLText(surface))
	}
	arguments = append(arguments, cursor.Before)
	filters, values, e := messageFilters(textFilterArgs(o), "m.", false)
	if e != nil {
		return result, e
	}
	clauses = append(clauses, filters)
	arguments = append(arguments, values...)
	if o.SelectedOnly {
		clauses = append(clauses, "m.selected=1")
	}
	for _, f := range []struct{ column, value string }{{"m.conversation_id", o.ConversationID}, {"c.workspace_id", o.WorkspaceID}, {"w.source_kind", o.Source}, {"c.provider", o.Provider}} {
		if f.value != "" {
			clauses = append(clauses, f.column+"=?")
			arguments = append(arguments, f.value)
		}
	}
	if o.WorkspaceIDs != nil {
		clauses = append(clauses, "c.workspace_id IN (SELECT value FROM json_each(?))")
		arguments = append(arguments, jsonText(o.WorkspaceIDs))
	}
	if o.Repository != "" {
		clauses = append(clauses, `w.repository_id IN (SELECT id FROM repositories WHERE instr(lower(display_name),lower(?))>0 OR instr(lower(COALESCE(canonical_remote,'')),lower(?))>0)`)
		arguments = append(arguments, o.Repository, o.Repository)
	}
	if o.From != "" {
		clauses = append(clauses, "julianday(m.created_at)>=julianday(?)")
		arguments = append(arguments, timeBound(o.From, false))
	}
	if o.To != "" {
		clauses = append(clauses, "julianday(m.created_at)<=julianday(?)")
		arguments = append(arguments, timeBound(o.To, true))
	}
	if span {
		clauses = append(clauses, "EXISTS(SELECT 1 FROM message_authorship_spans a WHERE a.message_id=m.id AND a.category=?)")
		arguments = append(arguments, o.Surface)
	}
	if key == "f.rowid" && (o.ConversationID != "" || o.WorkspaceID != "" || (o.WorkspaceIDs != nil && len(o.WorkspaceIDs) <= 100)) {
		scope, args := "", []any{}
		if o.ConversationID != "" {
			scope = "m.conversation_id=?"
			args = []any{o.ConversationID}
		} else if o.WorkspaceID != "" {
			scope = "m.conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?)"
			args = []any{o.WorkspaceID}
		} else {
			scope = "m.conversation_id IN (SELECT id FROM conversations WHERE workspace_id IN (SELECT value FROM json_each(?)))"
			args = []any{jsonText(o.WorkspaceIDs)}
		}
		var low, high sql.NullInt64
		var total, mapped int
		boundsSQL := `SELECT MIN(fr.fts_rowid),MAX(fr.fts_rowid),count(*),count(fr.fts_rowid) FROM messages m LEFT JOIN message_fts_rows fr ON fr.message_id=m.id WHERE length(CAST(m.text AS BLOB))>0 AND ` + scope
		if ready && small {
			boundsSQL = `SELECT MIN(fr.fts_rowid),MAX(fr.fts_rowid),count(*),count(fr.fts_rowid) FROM text_search_documents m LEFT JOIN message_fts_rows fr ON fr.message_id=m.message_id WHERE m.byte_length>0 AND ` + scope
		}
		e = c.reader().QueryRowContext(ctx, boundsSQL, args...).Scan(&low, &high, &total, &mapped)
		if e != nil {
			return result, fmt.Errorf("scope planning exceeded its budget; narrow the workspace selection: %w", e)
		}
		if total == mapped {
			clauses = append(clauses, "f.rowid BETWEEN ? AND ?")
			arguments = append(arguments, low.Int64, high.Int64)
		}
	}
	// A record that cannot fit even on an empty page must be skipped, not
	// deferred forever at the same cursor. Keep the byte cap strict and avoid
	// loading its body; the existing oversize accounting makes the gap explicit.
	maxMessage := min(1<<20, (8<<20)/regexInstructions, o.MaxBytes)
	selectSQL := fmt.Sprintf(`SELECT %s,m.id,m.conversation_id,c.workspace_id,w.title,c.provider,COALESCE(c.started_at,''),m.role,m.kind,COALESCE(m.created_at,''),m.content_hash,COALESCE(m.evidence_locator,''),length(CAST(m.text AS BLOB)),CASE WHEN length(CAST(m.text AS BLOB))<=%d THEN m.text ELSE '' END
 FROM %s JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id WHERE %s ORDER BY %s DESC LIMIT ?`, key, maxMessage, from, strings.Join(clauses, " AND "), key)
	arguments = append(arguments, o.MaxCandidates+1)
	// Spans need their original byte ranges and an independent tokenizer. This
	// connection is never the catalog, so search cannot write library content.
	var verifier *textVerifier
	if !scan && (span || strict || ordered) {
		verifier, e = newTextVerifier(ctx)
		if e != nil {
			return result, e
		}
		defer verifier.db.Close()
	}
	rows, e := c.reader().QueryContext(ctx, selectSQL, arguments...)
	if e != nil {
		if ctx.Err() != nil && parent.Err() == nil {
			result.StopReason = "time_budget"
			result.stopPage(cursor, pageBefore)
			return result, nil
		}
		return result, e
	}
	defer rows.Close()
	for rows.Next() {
		var position int64
		var item textSearchItem
		var body string
		var size int
		if e = rows.Scan(&position, &item.MessageID, &item.ConversationID, &item.WorkspaceID, &item.Title, &item.Provider, &item.StartedAt, &item.Role, &item.MessageKind, &item.CreatedAt, &item.ContentHash, &item.EvidenceLocator, &size, &body); e != nil {
			if ctx.Err() != nil && parent.Err() == nil {
				result.StopReason = "time_budget"
				break
			}
			return result, e
		}
		if result.Candidates >= o.MaxCandidates {
			result.StopReason = "candidate_budget"
			break
		}
		if size <= maxMessage && result.VerifiedBytes+size > o.MaxBytes {
			result.StopReason = "byte_budget"
			break
		}
		if ctx.Err() != nil {
			result.StopReason = "time_budget"
			break
		}
		result.Candidates++
		cursor.Before = position
		if size > maxMessage {
			result.SkippedOversize++
			cursor.Skipped++
			continue
		}
		result.VerifiedBytes += size
		lo, hi, ok := 0, 0, true
		snippetBody, snippetBase := body, 0
		if scan {
			at := verify.FindStringSubmatchIndex(body)
			ok = at != nil
			if ok {
				lo, hi = at[0], at[1]
				item.Captures, item.CapturesClipped = textCaptures(verify, at, 0)
			}
		} else if !span && (strict || ordered) {
			lo, hi, ok, e = verifier.match(ctx, node, body, o.CaseSensitive, o.Separators)
			if e != nil {
				if ctx.Err() != nil {
					result.StopReason = "time_budget"
					cursor.Before = position + 1
					break
				}
				return result, e
			}
		}
		if span {
			// Defer strict/NOT verification until the selected span: an excluded word
			// elsewhere in the message must not exclude a matching typed span.
			ok = false
			spans, e := queryMapsContext(ctx, c.reader(), `SELECT start_byte,end_byte FROM message_authorship_spans WHERE message_id=? AND category=? ORDER BY position`, item.MessageID, o.Surface)
			if e != nil {
				if ctx.Err() != nil {
					result.StopReason = "time_budget"
					cursor.Before = position + 1
					break
				}
				return result, e
			}
			for _, a := range spans {
				s, t := int(integer(a["start_byte"])), int(integer(a["end_byte"]))
				if s < 0 || t < s || t > len(body) {
					continue
				}
				piece := body[s:t]
				x, y, matched := 0, 0, false
				if scan {
					at := verify.FindStringSubmatchIndex(piece)
					if at != nil {
						x, y, matched = at[0], at[1], true
						item.Captures, item.CapturesClipped = textCaptures(verify, at, s)
					}
				} else {
					x, y, matched, e = verifier.match(ctx, node, piece, o.CaseSensitive, o.Separators)
					if e != nil {
						if ctx.Err() != nil {
							break
						}
						return result, e
					}
				}
				if matched {
					lo, hi, ok = s+x, s+y, true
					item.SpanCategory = o.Surface
					snippetBody, snippetBase = piece, s
					break
				}
			}
			if ctx.Err() != nil {
				result.StopReason = "time_budget"
				cursor.Before = position + 1
				break
			}
		}
		if !ok {
			continue
		}
		if hi == 0 && !scan {
			lo, hi = lexicalExcerpt(body, node)
		}
		item.StartByte, item.EndByte = lo, hi
		item.RangeKind = "excerpt"
		if scan {
			item.RangeKind = "match"
		}
		item.Snippet = searchSnippet(snippetBody, lo-snippetBase, min(hi-snippetBase, lo-snippetBase+240))
		item.Count = 1
		result.Count++
		cursor.Count = result.Count
		item.cursor = encodeTextCursor(cursor)
		result.Items = append(result.Items, item)
		if len(result.Items) >= o.Limit {
			result.StopReason = "page_limit"
			break
		}
	}
	if e = rows.Err(); e != nil {
		if ctx.Err() != nil && parent.Err() == nil {
			result.StopReason = "time_budget"
		} else {
			return result, e
		}
	}
	if parent.Err() != nil {
		return result, parent.Err()
	}
	if result.StopReason == "" {
		if result.SkippedOversize > 0 {
			result.StopReason = "oversized_messages"
		} else {
			result.Complete = true
			result.CountRelation = "exact"
			result.StopReason = "complete"
		}
	} else {
		result.stopPage(cursor, pageBefore)
	}
	// Detect writes that overlapped this page instead of mixing source versions.
	_ = rows.Close()
	{
		checkCtx, checkCancel := context.WithTimeout(parent, 100*time.Millisecond)
		defer checkCancel()
		after, e := c.textSearchGeneration(checkCtx, o)
		if e != nil {
			return result, e
		}
		if after != generation {
			return result, fmt.Errorf("messages changed during search; restart the search")
		}
	}
	return result, nil
}
func lexicalExcerpt(body string, node *textQuery) (int, int) {
	if lo, hi, found := lexicalLocation(body, node); found {
		return lo, hi
	}
	return 0, min(len(body), 160)
}

func lexicalLocation(body string, node *textQuery) (int, int, bool) {
	if node.Op == "or" {
		// Prefer a whole matching branch, not a word in a failed conjunction
		// or exclusion. Stop as soon as we can place the excerpt.
		for _, child := range node.Children {
			if lo, hi, matched := child.strictMatch(body, false, false); matched {
				return lo, hi, true
			}
		}
	}
	if len(node.Children) > 0 {
		children := node.Children
		if node.Op == "not" {
			children = children[:1]
		}
		// FTS normalization can match spellings the strict locator cannot
		// place (e.g. cafe/café). Try all positive clauses before falling back.
		for _, child := range children {
			if lo, hi, found := lexicalLocation(body, child); found {
				return lo, hi, true
			}
		}
		return 0, 0, false
	}
	loc := node.strictLocations(body, false, false)
	if len(loc) > 0 {
		return loc[0][0], loc[0][1], true
	}
	return 0, 0, false
}

func quoteSQLText(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

// Go folds K with Kelvin sign and S with long-s; SQLite trigrams need not.
// Drop such letters from candidate literals instead of losing those matches.
func asciiFoldLiteral(value string) string {
	best, current := "", ""
	for _, r := range value {
		safe := r < 128
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f > 127 {
				safe = false
			}
		}
		if !safe {
			current = ""
			continue
		}
		current += string(r)
		if len(current) > len(best) {
			best = current
		}
	}
	return best
}
func textRegexRequiredLiteral(n *syntax.Regexp) string {
	switch n.Op {
	case syntax.OpLiteral:
		value := string(n.Rune)
		if n.Flags&syntax.FoldCase != 0 {
			return asciiFoldLiteral(value)
		}
		return value
	case syntax.OpCapture, syntax.OpPlus:
		return textRegexRequiredLiteral(n.Sub[0])
	case syntax.OpRepeat:
		if n.Min > 0 {
			return textRegexRequiredLiteral(n.Sub[0])
		}
	case syntax.OpConcat:
		best := ""
		for _, child := range n.Sub {
			value := textRegexRequiredLiteral(child)
			if len(value) > len(best) {
				best = value
			}
		}
		return best
	}
	return ""
}

func textCaptures(pattern *regexp.Regexp, locations []int, offset int) ([]textCapture, bool) {
	captures := []textCapture{}
	names := pattern.SubexpNames()
	clipped := false
	for i := 1; i < len(locations)/2; i++ {
		if locations[2*i] < 0 {
			continue
		}
		if len(captures) == 16 {
			clipped = true
			break
		}
		captures = append(captures, textCapture{Group: i, Name: names[i], StartByte: offset + locations[2*i], EndByte: offset + locations[2*i+1]})
	}
	return captures, clipped
}
