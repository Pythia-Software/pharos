package archive

import (
	"context"
	"database/sql"
	"fmt"
	"io"
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
var queryV3MaxGroups = 10_000
var queryV3MaxPopulation = 10_000_000
var queryV3UsageMaxGroups = 100_000

func queryV3Dataset(schema qt.Schema) qt.QueryDatasetV3 {
	groups := queryV3MaxGroups
	// Usage has one compact fact per execution/day/model. The built-in Work
	// preset and message-label conversation results must cover libraries with
	// more than 10,000 workspaces or conversations. Session shape has one
	// compact row per session, and its dashboard counts each session or root
	// conversation, a group per conversation.
	if schema.Name == "pharos_usage" || schema.Name == "pharos_message_labels" || schema.Name == "pharos_session_shape" {
		groups = queryV3UsageMaxGroups
	}
	return qt.QueryDatasetV3{Schema: schema, Scope: queryTableScope, Dataset: schema.Name, Dialect: "sqlite",
		MaxRows: queryV3MaxRows, MaxGroups: groups, MaxPopulation: queryV3MaxPopulation, MaxStagingBytes: 512 << 20}
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
		qt.WriteQueryError(w, &qt.PlanDiagnostic{Code: qt.ErrorCodeInvalidRequest, Message: err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1_000_000))
	if err != nil {
		qt.WriteQueryError(w, err)
		return
	}
	request, err := qt.DecodeQueryRequestV3(body)
	if err != nil {
		qt.WriteQueryError(w, err)
		return
	}
	if dataset == "messages" && request.Distinct != nil && (request.Distinct.Field == "text" || request.Distinct.Field == "content" || request.Distinct.Field == "prompt") {
		qt.WriteQueryError(w, &qt.PlanDiagnostic{Code: qt.ErrorCodeInvalidQuery, Message: "message text has no distinct-value list"})
		return
	}
	parent := r.Context()
	if request.Distinct != nil {
		var release func()
		parent, release, err = s.beginQueryMetadata(parent)
		if err != nil {
			qt.WriteQueryError(w, err)
			return
		}
		defer release()
	}
	ctx, cancel := context.WithTimeout(parent, queryTableDeadline)
	defer cancel()
	result, find, err := s.executeQueryV3(ctx, dataset, r.URL.Query(), schema, request)
	if err != nil {
		qt.WriteQueryError(w, err)
		return
	}
	// Agent-event rows resting on superseded execution evidence are left out
	// (withheldAgentEvents); say how many, so absent rows read as pending.
	var coverage map[string]any
	if dataset == "agent_events" || dataset == "agent_event_messages" || dataset == "agent_event_rates" {
		if coverage, err = agentEventsCoverage(ctx, s.Catalog.reader()); err != nil {
			qt.WriteQueryError(w, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, struct {
		qt.QueryResultV3
		Find     *libraryFindSummary `json:"find,omitempty"`
		Coverage map[string]any      `json:"coverage,omitempty"`
	}{result, find, coverage}, http.StatusOK)
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
	d.SourceSQL = (sqlDataset{}).querySourceSQL(schema)
	if err = authorizeQueryV3Computed(ctx, tx, &d); err != nil {
		return nil, err
	}
	// Distinct values read source.where and a field outside the result SELECT.
	// Plan that authorized field as a row projection to discover its complete
	// base/computed inputs; execute the original distinct request unchanged.
	if q.Distinct != nil {
		field := q.Distinct.Field
		q.Distinct = nil
		q.Rows = true
		q.Result = qt.WireQuery{Select: []string{field}, Where: q.Source.Where, Limit: 1}
	}
	plan, err := d.SourcePlan(ctx, q, qt.PlanOptions{Identity: queryTableScope + ":" + schema.Name,
		Resolver: qt.SQLiteComputedDefinitionResolver{Tx: tx, Scope: d.Scope, Dataset: d.Dataset}})
	return plan.BaseFields, err
}

func (s *Server) executeQueryV3(ctx context.Context, dataset string, params url.Values, schema qt.Schema, q qt.QueryRequestV3) (qt.QueryResultV3, *libraryFindSummary, error) {
	if err := checkRecordKeywordHostState(dataset, q.HostState, params); err != nil {
		return qt.QueryResultV3{}, nil, &qt.PlanDiagnostic{Code: qt.ErrorCodeInvalidQuery, Message: err.Error()}
	}
	record, recordErr := s.recordSearch(ctx, dataset, params)
	if recordErr != nil {
		return qt.QueryResultV3{}, nil, &qt.PlanDiagnostic{Code: qt.ErrorCodeInvalidQuery, Message: recordErr.Error()}
	}
	defer record.close()
	if dataset == "parallelism" {
		var err error
		ctx, err = withParallelismPreferences(ctx, params.Get("parallelism_settings"))
		if err != nil {
			return qt.QueryResultV3{}, nil, err
		}
	}
	if dataset == "rhythm" {
		var err error
		ctx, err = withHabitsPreferences(ctx, params.Get("habits_settings"))
		if err != nil {
			return qt.QueryResultV3{}, nil, err
		}
	}
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
	if q.Grouping != nil || q.Distinct != nil {
		where = q.Source.Where
	}
	table, sqlBacked := s.Catalog.sqlDatasetFor(dataset)
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
			if record != nil && !slices.Contains(required, record.field) {
				required = append(required, record.field)
			}
		}
		if err == nil {
			var bounds map[string][2]string
			bounds, err = queryBucketBounds(ctx, s.Catalog.reader(), where)
			if err == nil {
				projected, _ := table.project(required).preferCoveringScan(where)
				d.SourceSQL, d.SourceArgs = projected.windowedQuerySource(schema, where, time.UnixMilli(q.Now), bounds)
			}
		}
	} else {
		required, err = s.queryV3SourceFields(ctx, schema, q)
		if record != nil && !slices.Contains(required, record.field) {
			required = append(required, record.field)
		}
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
					readFields := libraryFieldsFor(required...)
					if q.Grouping == nil && q.Rows {
						// libraryPage fills native display columns after filtering,
						// sorting and paging. Only predicates, order, metrics and
						// computed expressions need their inputs across all workspaces.
						copy := q
						copy.Result.Select = []string{schema.IDField}
						for _, field := range q.Result.Select {
							if _, native := localSchema.Fields[field]; !native {
								copy.Result.Select = append(copy.Result.Select, field)
							}
						}
						dependencies, e := s.queryV3SourceFields(ctx, schema, copy)
						if e != nil {
							return qt.QueryResultV3{}, nil, e
						}
						readFields = libraryFieldsFor(dependencies...)
					}
					if record != nil && readFields != nil {
						readFields[record.field] = true
					}
					rows, find, err = s.libraryRows(ctx, params, readFields, nil, early)
				}
			}
		} else if err == nil {
			rows, err = s.queryTableSourceRows(ctx, dataset, params, queryFieldsFor(dataset, required...), s.mapRowsWhere(dataset, where, time.UnixMilli(q.Now))...)
		}
	}
	if err != nil {
		return qt.QueryResultV3{}, nil, err
	}
	if find != nil && q.Grouping != nil {
		summary := find.summary()
		if summary.Limited || summary.Search != nil && !summary.Search.Complete {
			return qt.QueryResultV3{}, nil, &qt.PlanDiagnostic{Code: qt.ErrorCodeResourceLimit, Message: "This search is incomplete. Complete or narrow the search before grouping its results."}
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
		if q.Grouping != nil || q.Distinct != nil {
			q.Source.Where = nil
		} else {
			q.Result.Where = nil
		}
	}
	// Small summary/map populations benefit from memory scratch storage: SQLite's
	// ephemeral CTE B-trees otherwise write pages even with a temp cache. Keep the
	// full raw tool ledger and message payloads on the spill-capable path. The
	// executor still enforces the same staging-byte budget before any copy.
	// Agent event rates hold a few narrow rows per kind for each message read:
	// file scratch made a distinct share over them take seconds, not a tenth.
	memoryScratch := dataset == "tools" || dataset == "skill_usages" || dataset == "agent_event_rates" || dataset == "message_labels" && !slices.Contains(required, "text") || !sqlBacked && len(rows) <= 100_000
	mode := 1
	if memoryScratch {
		mode = 2
	}
	// Prepare cached dependencies before holding either a heavy slot or a
	// reader transaction. Hour-dependent usage metrics stage a second source.
	var hours []map[string]any
	if dataset == "usage" && q.Grouping == nil && len(q.Metrics) > 0 && slices.Contains(required, "hour") {
		localSchema, e := s.queryTableSchema("usage")
		if e != nil {
			return qt.QueryResultV3{}, nil, e
		}
		resolved, e := querytable.ResolveRelativeWhere(where, localSchema, time.UnixMilli(q.Now))
		if e != nil {
			return qt.QueryResultV3{}, nil, e
		}
		hours, err = s.Catalog.hourlyUsageForAggregation(ctx, resolved, required)
		if err != nil {
			return qt.QueryResultV3{}, nil, err
		}
	}
	finish, err := heavyQueries.acquire(ctx)
	if err != nil {
		return qt.QueryResultV3{}, nil, err
	}
	defer finish()
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
	if err = record.restrictSource(ctx, tx, &d, q); err != nil {
		return qt.QueryResultV3{}, nil, err
	}
	if sqlBacked && record == nil {
		// A SQL-backed source reads only catalog tables that advance the
		// query generation (or stay in step with ones that do), so its
		// counts are reusable within one. Staged rows are read outside the
		// transaction, and record search gives each request its own plan.
		d.PopulationCounter = s.Catalog.populationCounter(queryPopulationFastCounts(dataset, schema)...)
	}
	var result qt.QueryResultV3
	if dataset == "usage" && q.Grouping == nil && len(q.Metrics) > 0 {
		result, err = s.executeUsageQueryV3(ctx, tx, d, q, hours, required)
	} else {
		result, err = d.ExecuteIn(ctx, tx, q, qt.PlanOptions{Identity: queryTableScope + ":" + schema.Name})
	}
	if err != nil {
		return qt.QueryResultV3{}, nil, fmt.Errorf("%s query: %w", strings.TrimPrefix(schema.Name, "pharos_"), err)
	}
	if err = release(); err != nil {
		return qt.QueryResultV3{}, nil, err
	}
	finish()
	// Group rows have their own identity. Source-record enrichment is only valid
	// for the ordinary row projection and must never target a synthetic group ID.
	if q.Grouping == nil && q.Rows {
		switch dataset {
		case "tool_calls":
			err = s.Catalog.priceToolCalls(result.Rows)
		case "writing_messages":
			err = s.Catalog.attachAuthoredText(ctx, result.Rows)
		case "messages":
			err = s.Catalog.attachMessageText(ctx, result.Rows)
		case "library":
			result.Rows, err = s.Catalog.libraryPage(ctx, result.Rows)
			if find != nil && err == nil {
				err = s.Catalog.attachLibraryFind(ctx, find, result.Rows)
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

// Usage exposes daily rows but metrics that read hour reduce the hourly ledger.
// Both populations are selected in the same request; Query Table owns every
// filter, expression, row window and metric reduction on the staged sources.
func (s *Server) executeUsageQueryV3(ctx context.Context, tx *sql.Tx, d qt.QueryDatasetV3, q qt.QueryRequestV3, hours []map[string]any, required []string) (qt.QueryResultV3, error) {
	options := qt.PlanOptions{Identity: queryTableScope + ":" + d.Schema.Name, Resolver: qt.SQLiteComputedDefinitionResolver{Tx: tx, Scope: d.Scope, Dataset: d.Dataset}}
	daily, hourly, shown := q, q, q
	daily.Metrics, hourly.Metrics, shown.Metrics = nil, nil, nil
	hourly.Rows, shown.Rows = false, false
	hourly.Result.Where, shown.Result.Where = nil, nil
	for _, spec := range q.Metrics {
		probe := q
		probe.Rows = false
		probe.Result = qt.WireQuery{}
		dependency := spec
		dependency.Scope = "allMatching"
		probe.Metrics = []qt.AggSpec{dependency}
		plan, err := d.SourcePlan(ctx, probe, options)
		if err != nil {
			return qt.QueryResultV3{}, err
		}
		if !slices.Contains(plan.BaseFields, "hour") {
			daily.Metrics = append(daily.Metrics, spec)
		} else if spec.Scope == "shownRows" {
			spec.Scope = "allMatching"
			shown.Metrics = append(shown.Metrics, spec)
		} else {
			hourly.Metrics = append(hourly.Metrics, spec)
		}
	}
	result, err := d.ExecuteIn(ctx, tx, daily, options)
	if err != nil || len(hourly.Metrics)+len(shown.Metrics) == 0 {
		return result, err
	}
	byID := map[string]qt.MetricResult{}
	for _, metric := range result.Metrics {
		byID[metric.ID] = metric
	}
	for i, request := range []qt.QueryRequestV3{hourly, shown} {
		if len(request.Metrics) == 0 {
			continue
		}
		source := hours
		if i == 1 {
			window := daily
			window.Rows = true
			window.Result.Select = []string{d.Schema.IDField}
			window.Metrics = nil
			page, err := d.ExecuteIn(ctx, tx, window, options)
			if err != nil {
				return qt.QueryResultV3{}, err
			}
			ids := map[string]bool{}
			for _, row := range page.Rows {
				ids[firstString(row[d.Schema.IDField])] = true
			}
			source = make([]map[string]any, 0, len(hours))
			for _, row := range hours {
				if ids[usageDailyID(row)] {
					source = append(source, row)
				}
			}
		}
		part := d
		part.PopulationCounter = nil // staged per request; never reused
		part.SourceSQL, err = stageQueryRowsWithBudget(ctx, tx, fmt.Sprintf("pharos_usage_hours_%d", i), source, d.Schema, d.MaxPopulation, required)
		if err != nil {
			return qt.QueryResultV3{}, err
		}
		part.SourceArgs = nil
		out, err := part.ExecuteIn(ctx, tx, request, options)
		if err != nil {
			return qt.QueryResultV3{}, err
		}
		for _, metric := range out.Metrics {
			if i == 1 {
				metric.Scope = "shownRows"
			}
			byID[metric.ID] = metric
		}
	}
	result.Metrics = nil
	for _, spec := range q.Metrics {
		result.Metrics = append(result.Metrics, byID[spec.ID])
	}
	return result, nil
}
