// Package querytable implements the query-table wire contract for Pharos's
// map-backed datasets. Its types and limits mirror Pythia Software query-table
// v0.6.0. The map executor retains Pharos-specific text and regex conventions.
package querytable

import (
	"encoding/json"
	"fmt"

	shared "github.com/Pythia-Software/query-table/backends/go"
)

const (
	MaxLimit        = 10_000
	MaxOffset       = 1_000_000
	MaxSelect       = 200
	MaxWhere        = 100
	MaxOrderBy      = 20
	MaxAggregations = 20
)

// Share the upstream wire contract while retaining Pharos's map conventions.
type WhereClause = shared.WhereClause
type WhereTerm = shared.WhereTerm
type OrderBy = shared.OrderBy
type RegexExtract = shared.RegexExtract
type Aggregation = shared.AggSpec

type Query struct {
	Select       []string      `json:"select"`
	Where        []WhereTerm   `json:"where"`
	OrderBy      []OrderBy     `json:"orderBy"`
	Limit        int           `json:"limit"`
	Offset       int           `json:"offset"`
	Aggregations []Aggregation `json:"aggregations,omitempty"`
}

func (q *Query) UnmarshalJSON(data []byte) error {
	wire, err := shared.DecodeSQLiteQuery(data)
	if err != nil {
		return err
	}
	*q = Query{Select: wire.Select, Where: wire.Where, OrderBy: []OrderBy(wire.OrderBy), Limit: wire.Limit, Offset: wire.Offset, Aggregations: wire.Aggregations}
	return nil
}

func Decode(data []byte) (Query, error) {
	var query Query
	if err := json.Unmarshal(data, &query); err != nil {
		return query, fmt.Errorf("query json: %w", err)
	}
	if query.Limit == 0 {
		query.Limit = 100
	}
	if err := query.Validate(); err != nil {
		return Query{}, err
	}
	return query, nil
}

func (q Query) Validate() error {
	if q.Limit < 1 || q.Limit > MaxLimit {
		return fmt.Errorf("limit must be between 1 and %d", MaxLimit)
	}
	if q.Offset < 0 || q.Offset > MaxOffset {
		return fmt.Errorf("offset must be between 0 and %d", MaxOffset)
	}
	if len(q.Select) > MaxSelect || len(q.Where) > MaxWhere || len(q.OrderBy) > MaxOrderBy || len(q.Aggregations) > MaxAggregations {
		return fmt.Errorf("query exceeds structural limits")
	}
	literals := 0
	for _, term := range q.Where {
		predicates := term.Predicates()
		if len(predicates) == 0 {
			return fmt.Errorf("where group must contain a predicate")
		}
		for _, clause := range predicates {
			literals++
			if clause.Field == "" || len(clause.Field) > 256 || len(clause.Value) > 10_000 {
				return fmt.Errorf("invalid filter clause")
			}
		}
	}
	if literals > MaxWhere {
		return fmt.Errorf("where has %d clauses; maximum is %d", literals, MaxWhere)
	}
	return nil
}
