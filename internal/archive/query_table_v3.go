package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

// queryV3MaxRows is the V3 row budget. Tests lower it, so that a few rows
// make a source larger than the budget.
var queryV3MaxRows = querytable.MaxLimit

func queryV3Dataset(schema qt.Schema) qt.QueryDatasetV3 {
	groups := 10_000
	// Usage has one compact fact per execution/day/model. The built-in Work
	// preset must cover a library with more than 10,000 workspaces.
	if schema.Name == "pharos_usage" {
		groups = 100_000
	}
	return qt.QueryDatasetV3{Schema: schema, Scope: queryTableScope, Dataset: schema.Name, Dialect: "sqlite",
		MaxRows: queryV3MaxRows, MaxGroups: groups, MaxPopulation: 10_000_000, MaxStagingBytes: 512 << 20}
}

func authorizeQueryV3Computed(ctx context.Context, tx *sql.Tx, d *qt.QueryDatasetV3) error {
	ids, err := queryComputedIDs(ctx, tx, d.Dataset)
	if err != nil {
		return err
	}
	d.ComputedGroupable = map[string]bool{}
	d.ComputedFilterable = map[string]bool{}
	for _, id := range ids {
		d.ComputedGroupable[id] = true
		d.ComputedFilterable[id] = true
	}
	return nil
}

func (s *Server) postQueryV3(w http.ResponseWriter, r *http.Request, dataset string) {
	schema, err := sqliteQuerySchema(dataset)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1_000_000)
	var request qt.QueryRequestV3
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err == nil {
		err = requireQueryEOF(decoder)
	}
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTableDeadline)
	defer cancel()
	result, find, err := s.executeQueryV3(ctx, dataset, r.URL.Query(), schema, request)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, struct {
		qt.QueryResultV3
		Find *libraryFindSummary `json:"find,omitempty"`
	}{result, find}, http.StatusOK)
}

// Dependency discovery uses the same canonical definitions/revisions as execution.
// Map-backed datasets stage only these inputs; unrelated message/tool payloads
// never have to be copied to SQLite just to count or group their records.
func (s *Server) queryV3SourceFields(ctx context.Context, schema qt.Schema, q qt.QueryRequestV3) ([]string, error) {
	tx, err := s.Catalog.reader().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	d := queryV3Dataset(schema)
	d.SourceSQL = (sqlDataset{}).v2Source(schema)
	if err = authorizeQueryV3Computed(ctx, tx, &d); err != nil {
		return nil, err
	}
	plan, err := d.SourcePlan(ctx, q, qt.PlanOptions{Identity: queryTableScope + ":" + schema.Name,
		Resolver: qt.SQLiteComputedDefinitionResolver{Tx: tx, Scope: d.Scope, Dataset: d.Dataset}})
	return plan.BaseFields, err
}

func (s *Server) executeQueryV3(ctx context.Context, dataset string, params url.Values, schema qt.Schema, q qt.QueryRequestV3) (qt.QueryResultV3, *libraryFindSummary, error) {
	if dataset == "usage" {
		var err error
		ctx, err = withCarbonPreferences(ctx, params.Get("carbon_settings"))
		if err != nil {
			return qt.QueryResultV3{}, nil, err
		}
	}
	if q.Grouping == nil && q.Rows {
		q.Result.Select = queryRowActionFields(dataset, q.Result.Select)
	}
	d := queryV3Dataset(schema)
	where := q.Result.Where
	if q.Grouping != nil {
		where = q.Source.Where
	}
	table, sqlBacked := sqlDatasetFor(dataset)
	var rows []map[string]any
	var required []string
	var find *libraryFindSet
	var err error
	if sqlBacked {
		if err = s.Catalog.prepareSQLDataset(ctx, dataset); err != nil {
			return qt.QueryResultV3{}, nil, err
		}
		table, err = s.Catalog.queryTextDataset(ctx, dataset, table)
		if err != nil {
			return qt.QueryResultV3{}, nil, err
		}
		if dataset == "tool_calls" {
			table.toolSearchIndexed, err = s.Catalog.toolSearchReady(ctx)
		}
		if err == nil && dataset == "writing_messages" {
			table, find, err = s.Catalog.messageFindDataset(ctx, params, table)
		}
		if err == nil {
			required, err = s.queryV3SourceFields(ctx, schema, q)
		}
		if err == nil {
			var bounds map[string][2]string
			bounds, err = queryBucketBounds(ctx, s.Catalog.reader(), where)
			if err == nil {
				projected, _ := table.project(required).preferCoveringScan(where)
				d.SourceSQL, d.SourceArgs = projected.v2WindowedSource(schema, where, time.UnixMilli(q.Now), bounds)
			}
		}
	} else {
		required, err = s.queryV3SourceFields(ctx, schema, q)
		if err == nil && dataset == "library" {
			localSchema, e := s.queryTableSchema(dataset)
			err = e
			if err == nil {
				early := []querytable.WhereTerm{}
				for _, term := range where {
					safe := true
					for _, p := range term.Predicates() {
						if _, ok := localSchema.Fields[p.Field]; !ok {
							safe = false
						}
					}
					if safe {
						early = append(early, term)
					}
				}
				early, err = querytable.ResolveRelativeWhere(early, localSchema, time.UnixMilli(q.Now))
				if err == nil {
					rows, find, err = s.libraryRows(ctx, params, libraryFieldsFor(required...), nil, early)
				}
			}
		} else if err == nil {
			rows, err = s.queryTableRows(ctx, dataset, params, queryFieldsFor(dataset, required...), s.mapRowsWhere(dataset, where, time.UnixMilli(q.Now))...)
		}
	}
	if err != nil {
		return qt.QueryResultV3{}, nil, err
	}
	if find != nil && q.Grouping != nil {
		summary := find.summary()
		if summary.Limited || summary.Search != nil && !summary.Search.Complete {
			return qt.QueryResultV3{}, nil, errors.New("This search is incomplete. Complete or narrow the search before grouping its results.")
		}
	}
	// Usage WHERE hour selects daily facts by their matching ledger hours, just
	// like the ordinary row API. It must not compare only the first hour of a day.
	if dataset == "usage" && slices.Contains(whereFields(where), "hour") {
		localSchema, e := s.queryTableSchema(dataset)
		if e != nil {
			return qt.QueryResultV3{}, nil, e
		}
		resolved, e := querytable.ResolveRelativeWhere(where, localSchema, time.UnixMilli(q.Now))
		if e != nil {
			return qt.QueryResultV3{}, nil, e
		}
		hours, e := s.Catalog.hourlyUsageForAggregation(ctx, resolved, required)
		if e != nil {
			return qt.QueryResultV3{}, nil, e
		}
		ids := map[string]bool{}
		for _, hour := range hours {
			ids[usageDailyID(hour)] = true
		}
		selected := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if ids[firstString(row["id"])] {
				selected = append(selected, row)
			}
		}
		rows = selected
		if q.Grouping != nil {
			q.Source.Where = nil
		} else {
			q.Result.Where = nil
		}
	}
	var reducedPopulation *int64
	if sqlBacked && q.Grouping != nil {
		compact, reduced, request, population, handled, e := s.nativeGroupedRows(ctx, table, d, q)
		if e != nil {
			return qt.QueryResultV3{}, nil, e
		}
		if handled {
			rows, d, q = compact, reduced, request
			sqlBacked = false
			required = nativeGroupFields(q, d.Schema.IDField)
			schema = d.Schema
			reducedPopulation = &population
		}
	}
	// Small summary/map populations benefit from memory scratch storage: SQLite's
	// ephemeral CTE B-trees otherwise write pages even with a temp cache. Keep the
	// full raw tool ledger and message payloads on the spill-capable path. The
	// executor still enforces the same staging-byte budget before any copy.
	memoryScratch := dataset == "tools" || dataset == "skill_usages" || !sqlBacked && len(rows) <= 100_000
	mode := 1
	if memoryScratch {
		mode = 2
	}
	tx, release, err := beginQueryWithTempStore(ctx, s.Catalog.reader(), mode)
	if err != nil {
		return qt.QueryResultV3{}, nil, err
	}
	defer release()
	if err = authorizeQueryV3Computed(ctx, tx, &d); err != nil {
		return qt.QueryResultV3{}, nil, err
	}
	if !sqlBacked {
		d.SourceSQL, err = stageQueryRowsWithBudget(ctx, tx, "pharos_query_rows", rows, schema, d.MaxPopulation, required)
		if err != nil {
			return qt.QueryResultV3{}, nil, err
		}
	}
	result, err := d.ExecuteIn(ctx, tx, q, qt.PlanOptions{Identity: queryTableScope + ":" + schema.Name})
	if err != nil {
		return qt.QueryResultV3{}, nil, fmt.Errorf("%s query: %w", strings.TrimPrefix(schema.Name, "pharos_"), err)
	}
	if reducedPopulation != nil {
		result.SourceTotal = *reducedPopulation
	}
	if err = release(); err != nil {
		return qt.QueryResultV3{}, nil, err
	}
	// Group rows have their own identity. Source-record enrichment is only valid
	// for the ordinary row projection and must never target a synthetic group ID.
	if q.Grouping == nil && q.Rows {
		switch dataset {
		case "tool_calls":
			err = s.Catalog.priceToolCalls(result.Rows)
		case "writing_messages":
			err = s.Catalog.attachAuthoredText(ctx, result.Rows)
		case "library":
			result.Rows, err = s.Catalog.libraryPage(ctx, result.Rows)
			if find != nil {
				find.attach(result.Rows)
			}
		}
	}
	var summary *libraryFindSummary
	if find != nil {
		value := find.summary()
		summary = &value
	}
	return result, summary, err
}
