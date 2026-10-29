package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
	"modernc.org/sqlite"
)

// Record search is a host-owned source restriction. It uses the text engine's
// parser and unicode61 verifier, but never searches related conversations.
type recordSearchSpec struct {
	Field         string     `json:"field"`
	Query         string     `json:"query,omitempty"`
	Mode          string     `json:"mode,omitempty"`
	Where         *textQuery `json:"where,omitempty"`
	CaseSensitive bool       `json:"case_sensitive,omitempty"`
	Separators    bool       `json:"separators,omitempty"`
}

type recordSearchPlan struct {
	ctx               context.Context
	field             string
	id                int64
	spec              recordSearchSpec
	node              *textQuery
	pattern           *regexp.Regexp
	regexInstructions int
	verifier          *textVerifier
}

var recordSearchSequence atomic.Int64
var recordSearchPlans sync.Map

// Tests lower these budgets to exercise oversized records with small fixtures.
var recordSearchMaxTextBytes = 1 << 20
var recordSearchMaxRegexWork = 8 << 20

func init() {
	sqlite.MustRegisterScalarFunction("pharos_record_match", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		plan, ok := recordSearchPlans.Load(args[1])
		if !ok {
			return nil, fmt.Errorf("record search has expired; retry the query")
		}
		if args[0] == nil {
			return int64(0), nil
		}
		current := plan.(*recordSearchPlan)
		matched, err := current.matches(current.ctx, sqliteText(args[0]))
		if matched {
			return int64(1), err
		}
		return int64(0), err
	})
}

// recordKeywordHostState reports whether a request's host query state holds
// a record keyword clause (record_<dataset>). Analyze registers the clause as
// Query Table host state, which every request from the table carries. The
// browser compiles the clause into record_search, which this service
// validates and runs; the host state itself is the editor's definition and is
// not interpreted here. An absent host state (other clients, cards, previews)
// declares nothing.
func recordKeywordHostState(dataset string, raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	var state qt.HostQueryState
	if err := json.Unmarshal(raw, &state); err != nil || state.Version < 1 {
		return false, fmt.Errorf("this query's host state could not be read")
	}
	return len(state.Params["record_"+dataset]) > 0, nil
}

// checkRecordKeywordHostState refuses a request whose host state declares a
// keyword clause without record_search, or record_search without a declared
// clause, so a definition with a keyword clause never runs as the broader
// query without it. It checks presence only: comparing the two would need the
// editor's compiler (text-search-model.ts) here, and record_search, which the
// service validates in full, is what runs.
func checkRecordKeywordHostState(dataset string, raw json.RawMessage, values url.Values) error {
	if len(raw) == 0 {
		return nil
	}
	declared, err := recordKeywordHostState(dataset, raw)
	if err != nil {
		return err
	}
	if declared != (values.Get("record_search") != "") {
		return fmt.Errorf("this query's keyword filter arrived without its compiled search, or the search without the filter; reload the page")
	}
	return nil
}

func (s *Server) recordSearch(ctx context.Context, dataset string, values url.Values) (*recordSearchPlan, error) {
	raw := values.Get("record_search")
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 32000 {
		return nil, fmt.Errorf("record search is too large; remove some conditions")
	}
	var spec recordSearchSpec
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&spec)
	if decodeErr == nil {
		decodeErr = requireQueryEOF(decoder)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("invalid record search: %w", decodeErr)
	}
	schema, err := s.queryTableSchema(dataset)
	if err != nil {
		return nil, err
	}
	field, ok := schema.Fields[spec.Field]
	if !ok || !field.Backend || !field.Filterable || (field.Kind != querytable.Text && field.Kind != querytable.Enum) {
		return nil, fmt.Errorf("record search requires a filterable text field in %s", dataset)
	}
	if spec.Mode == "" {
		spec.Mode = "advanced"
	}
	node, err := prepareTextQuery(TextSearchOptions{Query: spec.Query, Mode: spec.Mode, Where: spec.Where})
	if err != nil {
		return nil, err
	}
	pattern, instructions, err := compileTextPattern(node, spec.CaseSensitive)
	if err != nil {
		return nil, err
	}
	plan := &recordSearchPlan{ctx: ctx, field: spec.Field, id: recordSearchSequence.Add(1), spec: spec, node: node, pattern: pattern, regexInstructions: instructions}
	if pattern == nil {
		plan.verifier, err = newTextVerifier(ctx)
		if err != nil {
			return nil, err
		}
	}
	recordSearchPlans.Store(plan.id, plan)
	return plan, nil
}

func (p *recordSearchPlan) close() {
	if p == nil {
		return
	}
	recordSearchPlans.Delete(p.id)
	if p.verifier != nil {
		p.verifier.db.Close()
	}
}

func (p *recordSearchPlan) matches(ctx context.Context, body string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(body) > recordSearchMaxTextBytes {
		return false, fmt.Errorf("record text exceeds the 1 MiB search budget; narrow the record filters")
	}
	if p.pattern != nil {
		if p.regexInstructions*len(body) > recordSearchMaxRegexWork {
			return false, fmt.Errorf("record regex exceeds the search work budget; simplify the pattern or narrow the record filters")
		}
		return p.pattern.MatchString(body), nil
	}
	_, _, matched, err := p.verifier.match(ctx, p.node, body, p.spec.CaseSensitive, p.spec.Separators)
	return matched, err
}

func (p *recordSearchPlan) predicate(expression string) string {
	return fmt.Sprintf("pharos_record_match(%s,%d)", expression, p.id)
}

func (p *recordSearchPlan) restrictSource(ctx context.Context, tx *sql.Tx, d *qt.QueryDatasetV3, q qt.QueryRequestV3) error {
	if p == nil {
		return nil
	}
	predicate := p.predicate("record_source." + sqlIdentifier(p.field))
	where := q.Result.Where
	if q.Grouping != nil {
		where = q.Source.Where
	}
	if len(where) > 0 {
		// Use the upstream compiler for the complete source WHERE, including
		// canonical computed definitions, OR, negation and relative dates. The
		// identity set has no page/population limit: those limits apply only
		// after keyword matching. Result HAVING must stay after grouping.
		schema := d.Schema
		schema.DefaultSort, schema.TiebreakSort = nil, nil
		eligible, err := qt.CompileSQLiteComputedRows(ctx, qt.WireQuery{Select: []string{schema.IDField}, Where: where}, schema, qt.PlanOptions{
			SourceSQL: d.SourceSQL, SourceArgs: d.SourceArgs, Now: time.UnixMilli(q.Now),
			ComputedFilterable: d.ComputedFilterable, ExpectedRevisions: q.ExpectedRevisions,
			Resolver: qt.SQLiteComputedDefinitionResolver{Tx: tx, Scope: d.Scope, Dataset: d.Dataset},
		})
		if err != nil {
			return err
		}
		// CASE guarantees short-circuit evaluation. An AND or nested WHERE can
		// be reordered by SQLite and verify excluded, over-budget record text.
		predicate = "CASE WHEN record_source." + sqlIdentifier(schema.IDField) + " IN (SELECT " +
			sqlIdentifier(eligible.Columns[0].ValueAlias) + " FROM (" + eligible.SQL + ")) THEN " + predicate + " ELSE 0 END"
		d.SourceArgs = eligible.Args
	}
	d.SourceSQL = "SELECT * FROM (" + d.SourceSQL + ") record_source WHERE " + predicate
	return nil
}

func (p *recordSearchPlan) table(table sqlDataset) (sqlDataset, error) {
	if p == nil {
		return table, nil
	}
	expression, ok := table.columns[p.field]
	if !ok {
		return table, fmt.Errorf("record field %s is unavailable", p.field)
	}
	predicate := p.predicate(expression)
	if table.base != "" {
		predicate = "(" + table.base + ") AND " + predicate
	}
	table.base = predicate
	// Cubes and compact facts do not contain arbitrary record text.
	table.cube, table.scalarFacts = nil, false
	return table, nil
}
