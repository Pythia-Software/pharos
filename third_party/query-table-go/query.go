// Package querytable compiles a frontend QueryState into parameterized SQL,
// validated against a server-defined field schema. It is the Go projection of
// @pythia-software/query-table-core: the wire types here mirror the TS shapes exactly so a
// base64url ?q= token (or a JSON request body) round-trips without translation.
//
// Safety model: the column
// expression for each field comes from the schema, never from request input. An
// unknown field is a compile error before any SQL is built. Values reach SQL
// only as bound placeholders ($N). The validated operator and the schema-defined
// expression are the only request-influenced tokens in the SQL text.
package querytable

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Structural limits mirror @pythia-software/query-table-core. Compile and
// DecodeWireQuery both enforce them so callers are protected whether a query
// came from a URL token or was decoded from a JSON request body elsewhere.
const (
	// MaxQueryLimit is the largest row limit representable by this platform.
	// The package does not impose a smaller application-level row cap; consumers
	// and their databases own any operational limit appropriate for the dataset.
	MaxQueryLimit       = int(^uint(0) >> 1)
	MaxQueryOffset      = 1_000_000
	MaxSelectColumns    = 200
	MaxWhereClauses     = 100
	MaxOrderByTerms     = 20
	MaxAggregations     = 20
	MaxGroupByFields    = 20
	maxFieldNameLength  = 256
	maxFilterValueBytes = 10_000
	maxRegexBytes       = 10_000
	maxQueryTokenBytes  = 2 * 1024 * 1024
)

// WhereClause mirrors @pythia-software/query-table-core WhereClause. Negated wraps
// the predicate in a null-exclusive NOT; it is set only for ops without a
// complementary operator (contains/starts_with/ends_with/includes), matching the
// frontend's negateClause.
type WhereClause struct {
	Field   string `json:"field"`
	Op      string `json:"op"`
	Value   string `json:"value"`
	Negated bool   `json:"negated,omitempty"`
}

// WhereTerm is one conjunct of the WHERE (mirrors the TS WhereTerm =
// WhereClause | OrGroup). It is either a single predicate (Field/Op/Value set,
// Any nil) or an OR group (Any set). The two shapes are disjoint on the wire — a
// literal carries "field", a group carries "any" — so the default JSON
// (un)marshaling distinguishes them with no custom code. WHERE is the AND of
// these terms, i.e. conjunctive normal form.
type WhereTerm struct {
	Field   string        `json:"field,omitempty"`
	Op      string        `json:"op,omitempty"`
	Value   string        `json:"value,omitempty"`
	Negated bool          `json:"negated,omitempty"`
	Any     []WhereClause `json:"any,omitempty"`
}

// IsGroup reports whether the term is an OR group rather than a single predicate.
func (t WhereTerm) IsGroup() bool { return t.Any != nil }

// Literal returns the predicate a non-group term represents.
func (t WhereTerm) Literal() WhereClause {
	return WhereClause{Field: t.Field, Op: t.Op, Value: t.Value, Negated: t.Negated}
}

// Predicates returns the literals the term contributes: itself for a single
// predicate, or its members for a group.
func (t WhereTerm) Predicates() []WhereClause {
	if t.IsGroup() {
		return t.Any
	}
	return []WhereClause{t.Literal()}
}

// OrderBy mirrors @pythia-software/query-table-core OrderByClause.
type OrderBy struct {
	Field   string        `json:"field"`
	Dir     string        `json:"dir"`             // "asc" | "desc"
	Nulls   string        `json:"nulls,omitempty"` // "first" | "last" | "" (default last)
	Extract *RegexExtract `json:"extract,omitempty"`
}

// RegexExtract transforms a sort value before comparison. PostgreSQL's
// substring(text FROM regex) returns the first capture group when present and
// the whole match otherwise; a non-match is NULL.
type RegexExtract struct {
	Regex string `json:"regex"`
}

// AggSpec mirrors @pythia-software/query-table-core AggregationClause: one aggregate op over one
// measure column (Field; empty ⇒ COUNT(*)), broken down by zero or more group
// columns. Compiled by CompileAggregation; the metric panel runs one per spec
// over the WHERE-filtered set (no paging).
type AggSpec struct {
	Diagnostics  []string            `json:"diagnostics,omitempty"`
	Display      *MetricDisplayHint  `json:"display,omitempty"`
	Expression   string              `json:"expression,omitempty"`
	ExpressionY  string              `json:"expressionY,omitempty"`
	Scope        string              `json:"scope,omitempty"`
	Sort         []MetricSort        `json:"sort,omitempty"`
	GroupLimit   int                 `json:"groupLimit,omitempty"`
	Distribution *MetricDistribution `json:"distribution,omitempty"`
	ID           string              `json:"id"`
	Op           string              `json:"op"`
	Field        string              `json:"field,omitempty"`
	GroupBy      []string            `json:"groupBy,omitempty"`
}

// WireQuery is the server-bound subset of QueryState (the TS ServerQuery / the
// {s,w,o,...} `?q=` payload). View-only state (column widths) never arrives.
//
// OrderBy unmarshals from BOTH the new array form and the legacy single-object
// form, so legacy links keep compiling.
type WireQuery struct {
	Select       []string    `json:"select,omitempty"`
	Where        []WhereTerm `json:"w,omitempty"`
	OrderBy      OrderBys    `json:"o,omitempty"`
	Limit        int         `json:"l,omitempty"`
	Offset       int         `json:"f,omitempty"`
	Aggregations []AggSpec   `json:"g,omitempty"`
}

// UnmarshalJSON accepts both the readable ServerQuery property names emitted
// by @pythia-software/query-table-core and the compact names used inside a URL token. It
// validates resource limits before returning, so a normal json.Decoder is a
// safe API boundary even when the caller does not use DecodeWireQuery.
func (q *WireQuery) UnmarshalJSON(data []byte) error {
	var payload struct {
		Select              []string        `json:"select"`
		CompactSelect       json.RawMessage `json:"s"`
		LegacySelect        json.RawMessage `json:"c"`
		Where               []WhereTerm     `json:"where"`
		CompactWhere        []WhereTerm     `json:"w"`
		OrderBy             OrderBys        `json:"orderBy"`
		CompactOrderBy      OrderBys        `json:"o"`
		Limit               *int            `json:"limit"`
		CompactLimit        *int            `json:"l"`
		Offset              *int            `json:"offset"`
		CompactOffset       *int            `json:"f"`
		Aggregations        []AggSpec       `json:"aggregations"`
		CompactAggregations []AggSpec       `json:"g"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	q.Select = payload.Select
	if q.Select == nil {
		q.Select = decodeSelect(payload.CompactSelect, payload.LegacySelect)
	}
	q.Where = payload.Where
	if q.Where == nil {
		q.Where = payload.CompactWhere
	}
	q.OrderBy = payload.OrderBy
	if q.OrderBy == nil {
		q.OrderBy = payload.CompactOrderBy
	}
	q.Limit = 0
	if payload.Limit != nil {
		q.Limit = *payload.Limit
	} else if payload.CompactLimit != nil {
		q.Limit = *payload.CompactLimit
	}
	q.Offset = 0
	if payload.Offset != nil {
		q.Offset = *payload.Offset
	} else if payload.CompactOffset != nil {
		q.Offset = *payload.CompactOffset
	}
	q.Aggregations = payload.Aggregations
	if q.Aggregations == nil {
		q.Aggregations = payload.CompactAggregations
	}

	return q.Validate()
}

// Validate rejects malformed query shapes. A zero Limit is allowed so an
// omitted value can be replaced by the caller's default; a negative value is
// never allowed. Values too large for the platform fail during JSON decoding.
func (q WireQuery) Validate() error {
	if q.Limit < 0 || q.Limit > MaxQueryLimit {
		return fmt.Errorf("limit must be between 0 and %d", MaxQueryLimit)
	}
	if q.Offset < 0 || q.Offset > MaxQueryOffset {
		return fmt.Errorf("offset must be between 0 and %d", MaxQueryOffset)
	}
	if len(q.Select) > MaxSelectColumns {
		return fmt.Errorf("select has %d fields; maximum is %d", len(q.Select), MaxSelectColumns)
	}
	if len(q.Where) > MaxWhereClauses {
		return fmt.Errorf("where has %d terms; maximum is %d", len(q.Where), MaxWhereClauses)
	}
	if len(q.OrderBy) > MaxOrderByTerms {
		return fmt.Errorf("orderBy has %d terms; maximum is %d", len(q.OrderBy), MaxOrderByTerms)
	}
	if len(q.Aggregations) > MaxAggregations {
		return fmt.Errorf("aggregations has %d items; maximum is %d", len(q.Aggregations), MaxAggregations)
	}
	for _, field := range q.Select {
		if field == "" || len(field) > maxFieldNameLength {
			return fmt.Errorf("invalid select field name")
		}
	}
	whereLiterals := 0
	for _, term := range q.Where {
		for _, clause := range term.Predicates() {
			if clause.Field == "" || len(clause.Field) > maxFieldNameLength {
				return fmt.Errorf("invalid filter field name")
			}
			if len(clause.Value) > maxFilterValueBytes {
				return fmt.Errorf("filter value for %q exceeds %d bytes", clause.Field, maxFilterValueBytes)
			}
			whereLiterals++
		}
	}
	if whereLiterals > MaxWhereClauses {
		return fmt.Errorf("where has %d clauses; maximum is %d", whereLiterals, MaxWhereClauses)
	}
	for _, term := range q.OrderBy {
		if term.Field == "" || len(term.Field) > maxFieldNameLength {
			return fmt.Errorf("invalid sort field name")
		}
		if term.Extract != nil && len(term.Extract.Regex) > maxRegexBytes {
			return fmt.Errorf("sort regex for %q exceeds %d bytes", term.Field, maxRegexBytes)
		}
	}
	for _, aggregation := range q.Aggregations {
		if aggregation.ID == "" || len(aggregation.ID) > maxFieldNameLength {
			return fmt.Errorf("invalid aggregation id")
		}
		if len(aggregation.GroupBy) > MaxGroupByFields {
			return fmt.Errorf("aggregation %q has %d group fields; maximum is %d", aggregation.ID, len(aggregation.GroupBy), MaxGroupByFields)
		}
	}
	return nil
}

// OrderBys is a slice of OrderBy that also accepts a single object on decode
// (legacy compatibility) and a base64url `c`/`s` is handled at the WireQuery
// level via DecodeWireQuery.
type OrderBys []OrderBy

// UnmarshalJSON accepts either `[{...},{...}]` (current) or `{...}` (legacy).
func (o *OrderBys) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "null" || trimmed == "" {
		*o = nil
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []OrderBy
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*o = arr
		return nil
	}
	var single OrderBy
	if err := json.Unmarshal(b, &single); err != nil {
		return err
	}
	*o = []OrderBy{single}
	return nil
}

// wirePayload is the compact JSON the frontend base64url-encodes. `c` is the
// legacy alias for select column names; `s` is the current select (tuples).
type wirePayload struct {
	Select json.RawMessage `json:"s,omitempty"` // [field] | [field,width] tuples, or legacy string[]
	Legacy json.RawMessage `json:"c,omitempty"` // legacy string[]
	Where  []WhereTerm     `json:"w,omitempty"`
	Order  OrderBys        `json:"o,omitempty"`
	Limit  int             `json:"l,omitempty"`
	Offset int             `json:"f,omitempty"`
	Aggs   []AggSpec       `json:"g,omitempty"`
}

// DecodeWireQuery decodes the base64url-encoded JSON `?q=` token produced by
// @pythia-software/query-table-core encodeQuery. The select tuples carry widths the server
// ignores; only the field names are extracted. Mirrors the charset/padding
// fix-ups so a bookmark round-trips bit-for-bit.
func DecodeWireQuery(token string) (WireQuery, error) {
	var q WireQuery
	if token == "" {
		return q, nil
	}
	if len(token) > maxQueryTokenBytes {
		return q, fmt.Errorf("query token exceeds %d bytes", maxQueryTokenBytes)
	}
	s := strings.ReplaceAll(token, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return q, fmt.Errorf("base64: %w", err)
	}
	var p wirePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return q, fmt.Errorf("json: %w", err)
	}
	q.Where = p.Where
	q.OrderBy = p.Order
	q.Limit = p.Limit
	q.Offset = p.Offset
	q.Aggregations = p.Aggs
	q.Select = decodeSelect(p.Select, p.Legacy)
	if err := q.Validate(); err != nil {
		return WireQuery{}, fmt.Errorf("query: %w", err)
	}
	return q, nil
}

// decodeSelect pulls field names from the `s` tuples (or legacy `c`/`s` string[]).
func decodeSelect(s, legacy json.RawMessage) []string {
	for _, raw := range []json.RawMessage{s, legacy} {
		if len(raw) == 0 {
			continue
		}
		// Try string[] first (legacy), then tuple form.
		var names []string
		if err := json.Unmarshal(raw, &names); err == nil {
			return names
		}
		var tuples [][]json.RawMessage
		if err := json.Unmarshal(raw, &tuples); err == nil {
			out := make([]string, 0, len(tuples))
			for _, t := range tuples {
				if len(t) == 0 {
					continue
				}
				var name string
				if err := json.Unmarshal(t[0], &name); err == nil {
					out = append(out, name)
				}
			}
			return out
		}
	}
	return nil
}
