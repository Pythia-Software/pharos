package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
	"modernc.org/sqlite"
)

const queryTableComputedDDL = qt.SQLiteComputedColumnsDDL
const queryTableScope = "local"
const queryTablePopulation = 250_000
const queryTableDeadline = 30 * time.Second

// Registration is process-wide and must precede every physical connection.
func init() {
	for _, fn := range append(qt.SQLiteFunctions(), qt.SQLiteV2Functions()...) {
		if err := sqlite.RegisterDeterministicScalarFunction(fn.Name, int32(fn.Arity), func(_ *sqlite.FunctionContext, values []driver.Value) (driver.Value, error) { return fn.Call(values) }); err != nil {
			panic(err)
		}
	}
	for _, descriptor := range qt.SQLiteV2Aggregates() {
		if err := sqlite.RegisterFunction(descriptor.Name, &sqlite.FunctionImpl{NArgs: int32(descriptor.Arity), Deterministic: true, MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) {
			return &queryTableAggregate{fn: descriptor.New()}, nil
		}}); err != nil {
			panic(err)
		}
	}
}

type queryV2ExecutionResult struct {
	qt.SQLiteV2ExecutionResult
	Find *libraryFindSummary
}

type queryTableAggregate struct{ fn qt.SQLiteAggregateFunction }

func (a *queryTableAggregate) Step(_ *sqlite.FunctionContext, v []driver.Value) error {
	return a.fn.Step(v)
}
func (a *queryTableAggregate) WindowValue(_ *sqlite.FunctionContext) (driver.Value, error) {
	return a.fn.Value()
}
func (a *queryTableAggregate) WindowInverse(_ *sqlite.FunctionContext, _ []driver.Value) error {
	return errors.New("query-table aggregates do not support sliding windows")
}
func (a *queryTableAggregate) Final(_ *sqlite.FunctionContext) { a.fn = nil }

func sqliteQuerySchema(dataset string) (qt.Schema, error) {
	switch dataset {
	case "library":
		return PharosLibrarySchema(), nil
	case "activity":
		return PharosActivitySchema(), nil
	case "usage":
		return PharosUsageSchema(), nil
	case "writing":
		return PharosWritingSchema(), nil
	case "writing_messages":
		return PharosWritingMessagesSchema(), nil
	case "mcp_calls":
		return PharosMcpCallsSchema(), nil
	case "tools":
		return PharosToolsSchema(), nil
	case "tool_calls":
		return PharosToolCallsSchema(), nil
	case "skill_usages":
		return PharosSkillUsagesSchema(), nil
	case "tl1_attempts":
		return PharosTl1AttemptsSchema(), nil
	}
	return qt.Schema{}, fmt.Errorf("unknown query-table dataset %q", dataset)
}

func queryV2Dataset(dataset string, schema qt.Schema) qt.SQLiteV2Dataset {
	return qt.SQLiteV2Dataset{Schema: schema, Scope: queryTableScope, Dataset: schema.Name, MaxRows: querytable.MaxLimit, MaxGroups: maxSQLBuckets, MaxPopulation: queryTablePopulation}
}

// The outer schema binds to stable column aliases; the host owns the joins and
// mirrored-work exclusion. SQLite can flatten this projection and use indexes.
func (d sqlDataset) v2Source(schema qt.Schema) string {
	names := make([]string, 0, len(schema.Fields))
	for name := range schema.Fields {
		names = append(names, name)
	}
	slices.Sort(names)
	columns := make([]string, 0, len(names))
	for _, name := range names {
		expression := d.columns[name]
		if expression == "" {
			expression = "NULL"
		}
		columns = append(columns, expression+" AS "+sqlIdentifier(name))
	}
	where := ""
	if d.base != "" {
		where = " WHERE " + d.base
	}
	return "SELECT " + strings.Join(columns, ",") + " " + d.from + where
}

// Candidate restrictions preserve the full shared predicate as the final test.
// An OR can be narrowed only when every branch has a safe indexed candidate.
func (d sqlDataset) v2IndexedSource(schema qt.Schema, terms []qt.WhereTerm, exact ...map[string][2]string) (string, []any) {
	source := d.v2Source(schema)
	args := []any{}
	conditions := []string{}
	bind := func(value any) string { args = append(args, value); return fmt.Sprintf("?%d", len(args)) }
	for _, term := range terms {
		branches := []string{}
		saved := len(args)
		for _, clause := range term.Predicates() {
			predicate := ""
			if !clause.Negated && d.toolSearchIndexed {
				if search := toolSearchTerm(clause.Field, clause.Op, clause.Value); search != "" {
					predicate = "t.rowid IN (SELECT rowid FROM tool_command_fts WHERE tool_command_fts MATCH " + bind(search) + ")"
				}
			}
			if !clause.Negated && clause.Op == "=" && d.buckets[clause.Field] != "" {
				if from, to, ok := bucketRange(clause.Field, clause.Value); ok {
					if len(exact) > 0 {
						if bounds, found := exact[0][clause.Field+"\x00"+clause.Value]; found {
							from, to = bounds[0], bounds[1]
						}
					}
					predicate = "(" + d.buckets[clause.Field] + ">=" + bind(from) + " AND " + d.buckets[clause.Field] + "<" + bind(to) + ")"
				}
			}
			if predicate == "" {
				branches = nil
				args = args[:saved]
				break
			}
			branches = append(branches, predicate)
		}
		if len(branches) > 0 {
			conditions = append(conditions, "("+strings.Join(branches, " OR ")+")")
		}
	}
	if len(conditions) > 0 {
		conjunction := " WHERE "
		if d.base != "" {
			conjunction = " AND "
		}
		source += conjunction + strings.Join(conditions, " AND ")
	}
	return source, args
}

// Ask SQLite itself for the local-calendar UTC boundaries, so DST and its
// timezone convention agree with the final date(...,'localtime') predicate.
// Tight bounds avoid evaluating expensive calendar expressions on adjacent days.
func queryBucketBounds(ctx context.Context, db *sql.DB, terms []qt.WhereTerm) (map[string][2]string, error) {
	result := map[string][2]string{}
	for _, term := range terms {
		for _, clause := range term.Predicates() {
			if clause.Negated || clause.Op != "=" {
				continue
			}
			layout := "2006-01-02"
			if clause.Field == "month" {
				layout = "2006-01"
			} else if clause.Field != "day" && clause.Field != "week" {
				continue
			}
			start, err := time.Parse(layout, clause.Value)
			if err != nil {
				continue
			}
			end := start.AddDate(0, 0, 1)
			if clause.Field == "week" {
				end = start.AddDate(0, 0, 7)
			}
			if clause.Field == "month" {
				end = start.AddDate(0, 1, 0)
			}
			key := clause.Field + "\x00" + clause.Value
			if _, ok := result[key]; ok {
				continue
			}
			var from, to sql.NullString
			err = db.QueryRowContext(ctx, `SELECT strftime('%Y-%m-%dT%H:%M:%fZ',?,'utc'),strftime('%Y-%m-%dT%H:%M:%fZ',?,'utc')`, start.Format("2006-01-02T15:04:05"), end.Format("2006-01-02T15:04:05")).Scan(&from, &to)
			if err != nil {
				return nil, err
			}
			if from.Valid && to.Valid {
				result[key] = [2]string{from.String, to.String}
			}
		}
	}
	return result, nil
}

func sqlIdentifier(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// Map-backed datasets retain their host enrichment/search. One temporary table
// is shared by the entire batch. Rollback removes it before the pooled connection
// returns, including on cancellation; nothing is written to the archive.
func stageQueryRows(ctx context.Context, tx *sql.Tx, name string, rows []map[string]any, schema qt.Schema, required ...[]string) (string, error) {
	if len(rows) > queryTablePopulation {
		return "", fmt.Errorf("source population exceeds %d rows", queryTablePopulation)
	}
	names := make([]string, 0, len(schema.Fields))
	for field := range schema.Fields {
		names = append(names, field)
	}
	if len(required) > 0 {
		names = slices.Clone(required[0])
	}
	slices.Sort(names)
	columns := make([]string, len(names))
	placeholders := make([]string, len(names))
	for i, field := range names {
		columns[i] = sqlIdentifier(field)
		placeholders[i] = "?"
	}
	table := sqlIdentifier(name)
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE "+table+" ("+strings.Join(columns, ",")+")"); err != nil {
		return "", err
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO "+table+" VALUES ("+strings.Join(placeholders, ",")+")")
	if err != nil {
		return "", err
	}
	defer stmt.Close()
	for _, row := range rows {
		values := make([]any, len(names))
		for i, field := range names {
			value := row[field]
			if schema.Fields[field].Kind == qt.FieldTextArray && value != nil {
				if _, ok := value.(string); !ok {
					data, e := json.Marshal(value)
					if e != nil {
						return "", e
					}
					value = string(data)
				}
			}
			if schema.Fields[field].Kind == qt.FieldDatetime && value == "" {
				value = nil
			}
			values[i] = value
		}
		if _, err = stmt.ExecContext(ctx, values...); err != nil {
			return "", err
		}
	}
	if len(required) == 0 {
		return "SELECT * FROM " + table, nil
	}
	// The compiler projects every schema binding before pruning its plan. Keep
	// unused aliases resolvable without reading or copying their payloads.
	bindings := map[string]string{}
	for _, field := range names {
		bindings[field] = sqlIdentifier(field)
	}
	return (sqlDataset{columns: bindings, from: "FROM " + table}).v2Source(schema), nil
}

func decodeQueryBody(r *http.Request, value any) error {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("query json: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("query body must contain exactly one JSON value")
	}
	return nil
}

func (s *Server) getQueryCapabilities(w http.ResponseWriter, r *http.Request, dataset string) {
	schema, err := sqliteQuerySchema(dataset)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTableDeadline)
	defer cancel()
	tx, err := s.Catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	defer tx.Rollback()
	d := queryV2Dataset(dataset, schema)
	// Describe validates plans without reading a population.
	d.SourceSQL = (sqlDataset{}).v2Source(schema)
	ids, err := queryComputedIDs(ctx, tx, schema.Name)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	// Canonical local definitions may be used as group keys; result budgets apply.
	d.ComputedGroupable = map[string]bool{}
	for _, id := range ids {
		d.ComputedGroupable[id] = true
	}
	execution, unavailable, err := describeQueryComputed(ctx, tx, d, ids)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, map[string]any{"metricCapabilities": d.Capabilities(), "computedExecution": execution, "computedDiagnostics": unavailable}, 200)
}

// Schema changes can invalidate persisted formulas. Keep the complete store
// catalogue repairable, advertising only definitions whose whole graph compiles.
// Database and context failures still fail the request rather than hiding data.
func describeQueryComputed(ctx context.Context, tx *sql.Tx, d qt.SQLiteV2Dataset, ids []string) (qt.SQLiteComputedExecution, map[string]*qt.PlanDiagnostic, error) {
	execution := qt.SQLiteComputedExecution{Profile: qt.SQLiteExpressionProfile, ResolvedRevisions: map[string]string{}, Fields: map[string]qt.SQLiteComputedFieldCapability{}}
	unavailable := map[string]*qt.PlanDiagnostic{}
	for _, id := range ids {
		current, err := d.DescribeComputedIn(ctx, tx, []string{id}, qt.PlanOptions{Identity: d.Scope + ":" + d.Dataset})
		if err != nil {
			var diagnostic *qt.PlanDiagnostic
			if !errors.As(err, &diagnostic) {
				return execution, unavailable, err
			}
			unavailable[id] = diagnostic
			continue
		}
		maps.Copy(execution.ResolvedRevisions, current.ResolvedRevisions)
		maps.Copy(execution.Fields, current.Fields)
	}
	return execution, unavailable, nil
}
func queryComputedIDs(ctx context.Context, tx *sql.Tx, dataset string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM query_table_computed_columns WHERE scope=? AND dataset=? ORDER BY id", queryTableScope, dataset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Validate saves atomically: the edited graph must compile and previously valid
// definitions must remain valid. Unrelated schema-invalidated definitions can
// then be repaired one at a time without accepting new invalid API edits.
type pharosComputedStore struct{ qt.SQLiteComputedColumnStore }

func (s pharosComputedStore) Save(ctx context.Context, scope, dataset string, request qt.SaveComputedColumnRequest) (qt.ComputedColumn, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return qt.ComputedColumn{}, err
	}
	defer tx.Rollback()
	schema, err := sqliteQuerySchema(strings.TrimPrefix(dataset, "pharos_"))
	if err != nil {
		return qt.ComputedColumn{}, err
	}
	ids, err := queryComputedIDs(ctx, tx, dataset)
	if err != nil {
		return qt.ComputedColumn{}, err
	}
	d := queryV2Dataset("", schema)
	d.Scope = scope
	d.SourceSQL = (sqlDataset{}).v2Source(schema)
	_, before, err := describeQueryComputed(ctx, tx, d, ids)
	if err != nil {
		return qt.ComputedColumn{}, err
	}
	column, err := s.SaveIn(ctx, tx, scope, dataset, request)
	if err != nil {
		return qt.ComputedColumn{}, err
	}
	ids, err = queryComputedIDs(ctx, tx, dataset)
	if err != nil {
		return qt.ComputedColumn{}, err
	}
	_, after, err := describeQueryComputed(ctx, tx, d, ids)
	if err != nil {
		return qt.ComputedColumn{}, err
	}
	for _, id := range ids {
		if diagnostic := after[id]; diagnostic != nil && (id == column.ID || before[id] == nil) {
			return qt.ComputedColumn{}, diagnostic
		}
	}
	if err = tx.Commit(); err != nil {
		return qt.ComputedColumn{}, err
	}
	return column, nil
}

func (s *Server) queryComputedColumns(w http.ResponseWriter, r *http.Request) {
	qt.NewComputedColumnsHandler(pharosComputedStore{qt.SQLiteComputedColumnStore{DB: s.Catalog.DB}}, func(r *http.Request, dataset string, write bool) (string, error) {
		if !s.authorized(r) || !sameOrigin(r) {
			return "", errors.New("unauthorized")
		}
		schema, err := sqliteQuerySchema(strings.TrimPrefix(dataset, "pharos_"))
		if err != nil || schema.Name != dataset {
			return "", errors.New("unknown dataset")
		}
		return queryTableScope, nil
	}).ServeHTTP(w, r)
}

func (s *Server) postQueryV2(w http.ResponseWriter, r *http.Request, dataset, operation string) {
	schema, err := sqliteQuerySchema(dataset)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1_000_000)
	var rowRequest *qt.ServerQueryV2
	var metrics *qt.MetricQuery
	if operation == "rows-v2" {
		rowRequest = &qt.ServerQueryV2{}
		err = decodeQueryBody(r, rowRequest)
	} else {
		metrics = &qt.MetricQuery{}
		err = decodeQueryBody(r, metrics)
	}
	if err != nil {
		writeError(w, err, 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTableDeadline)
	defer cancel()
	result, err := s.executeQueryV2(ctx, dataset, r.URL.Query(), schema, rowRequest, metrics)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	if rowRequest != nil {
		writeJSON(w, struct {
			*qt.SQLiteRowsV2Result
			Find *libraryFindSummary `json:"find,omitempty"`
		}{result.Rows, result.Find}, 200)
	} else {
		writeJSON(w, result.Metrics, 200)
	}
}

// Bounded v2 populations use memory-backed temporary relations. Keep this
// setting on the leased connection only; restore it before returning the pool
// connection or performing host enrichment.
func beginQueryV2(ctx context.Context, db *sql.DB) (*sql.Tx, func() error, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	var original int
	if err = conn.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&original); err != nil {
		conn.Close()
		return nil, nil, err
	}
	if original != 2 {
		if _, err = conn.ExecContext(ctx, "PRAGMA temp_store=MEMORY"); err != nil {
			conn.Close()
			return nil, nil, err
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	released := false
	release := func() error {
		if released {
			return nil
		}
		released = true
		var rollbackError error
		if tx != nil {
			rollbackError = tx.Rollback()
			if errors.Is(rollbackError, sql.ErrTxDone) {
				rollbackError = nil
			}
		}
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if original != 2 {
			if _, resetError := conn.ExecContext(cleanup, fmt.Sprintf("PRAGMA temp_store=%d", original)); resetError != nil {
				// A cancelled transaction may have closed its physical connection already.
				// Otherwise discard a connection whose original settings cannot be restored.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
		closeError := conn.Close()
		if rollbackError != nil {
			return rollbackError
		}
		return closeError
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	return tx, release, nil
}

// Compile without reading a population to discover exact base-field inputs,
// including canonical transitive formulas and hidden/default ordering. Release
// this snapshot before Library reads; execution revalidates the revision envelope
// in its own transaction before consuming the selectively staged values.
func queryV2LibraryFields(ctx context.Context, db *sql.DB, schema qt.Schema, rows *qt.ServerQueryV2, metrics *qt.MetricQuery, clock time.Time) ([]string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids, err := queryComputedIDs(ctx, tx, schema.Name)
	if err != nil {
		return nil, err
	}
	groupable := map[string]bool{}
	for _, id := range ids {
		groupable[id] = true
	}
	options := qt.PlanOptions{SourceSQL: (sqlDataset{}).v2Source(schema), Now: clock,
		ComputedGroupable: groupable, Resolver: qt.SQLiteComputedDefinitionResolver{Tx: tx, Scope: queryTableScope, Dataset: schema.Name}}
	fields := map[string]bool{schema.IDField: true}
	add := func(names []string) {
		for _, name := range names {
			if _, base := schema.Fields[name]; base {
				fields[name] = true
			}
		}
	}
	if rows != nil {
		copy := *rows
		copy.Select = slices.Clone(rows.Select)
		if !slices.Contains(copy.Select, schema.IDField) {
			copy.Select = append(copy.Select, schema.IDField)
		}
		plan, err := qt.CompileSQLiteRowsV2(ctx, copy, schema, options)
		if err != nil {
			return nil, err
		}
		add(plan.Dependencies)
		// Raw filter predicates do not appear in SQLPlan.Dependencies.
		add(whereFields(rows.Where))
	}
	if metrics != nil {
		plans, err := qt.CompileSQLiteMetrics(ctx, *metrics, schema, options)
		if err != nil {
			return nil, err
		}
		for _, plan := range plans.Metrics {
			add(plan.Dependencies)
		}
		add(whereFields(metrics.Where))
	}
	return slices.Sorted(maps.Keys(fields)), nil
}

func (s *Server) executeQueryV2(ctx context.Context, dataset string, params url.Values, schema qt.Schema, rowRequest *qt.ServerQueryV2, metrics *qt.MetricQuery) (queryV2ExecutionResult, error) {
	clock := time.Now()
	d := queryV2Dataset(dataset, schema)
	table, sqlBacked := sqlDatasetFor(dataset)
	var sourceRows, usageHours []map[string]any
	var libraryNames []string
	var find *libraryFindSet
	var evidence map[string]*searchEvidence
	if sqlBacked {
		if err := s.Catalog.prepareSQLDataset(ctx, dataset); err != nil {
			return queryV2ExecutionResult{}, err
		}
		if dataset == "tool_calls" {
			ready, e := s.Catalog.toolSearchReady(ctx)
			if e != nil {
				return queryV2ExecutionResult{}, e
			}
			table.toolSearchIndexed = ready
		}
		where := []qt.WhereTerm{}
		if rowRequest != nil {
			where = rowRequest.Where
		}
		if metrics != nil {
			where = metrics.Where
		}
		bounds, e := queryBucketBounds(ctx, s.Catalog.DB, where)
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
		d.SourceSQL, d.SourceArgs = table.v2IndexedSource(schema, where, bounds)
	} else {
		var err error
		names := make([]string, 0, len(schema.Fields))
		for field := range schema.Fields {
			names = append(names, field)
		}
		if dataset == "library" {
			libraryNames, err = queryV2LibraryFields(ctx, s.Catalog.DB, schema, rowRequest, metrics, clock)
			if err == nil {
				sourceRows, find, err = s.libraryRows(ctx, params, libraryFieldsFor(libraryNames...), &evidence)
			}
		} else {
			sourceRows, err = s.queryTableRows(ctx, dataset, params, libraryFieldsFor(names...))
		}
		if err != nil {
			return queryV2ExecutionResult{}, err
		}
	}
	if dataset == "usage" && metrics != nil {
		document, e := querySchemaDocument("usage")
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
		localSchema, e := querytable.LoadSchema(document)
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
		copy := *metrics
		copy.Where, e = querytable.ResolveRelativeWhere(metrics.Where, localSchema, clock)
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
		metrics = &copy
		usageHours, e = s.Catalog.hourlyUsageForAggregation(ctx, metrics.Where, localSchema)
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
	}
	if dataset == "usage" && rowRequest != nil && slices.Contains(whereFields(rowRequest.Where), "hour") {
		localSchema, e := s.queryTableSchema("usage")
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
		copy := *rowRequest
		copy.Where, e = querytable.ResolveRelativeWhere(rowRequest.Where, localSchema, clock)
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
		hours, e := s.Catalog.hourlyUsageForAggregation(ctx, copy.Where, localSchema)
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
		ids := map[string]bool{}
		for _, row := range hours {
			ids[firstString(row["agent_session_id"])+"|"+firstString(row["day"])+"|"+firstString(row["model"])] = true
		}
		selected := []map[string]any{}
		for _, row := range sourceRows {
			if ids[firstString(row["id"])] {
				selected = append(selected, row)
			}
		}
		sourceRows = selected
		copy.Where = nil
		rowRequest = &copy
	}
	tx, release, err := beginQueryV2(ctx, s.Catalog.DB)
	if err != nil {
		return queryV2ExecutionResult{}, err
	}
	defer release()
	ids, err := queryComputedIDs(ctx, tx, schema.Name)
	if err != nil {
		return queryV2ExecutionResult{}, err
	}
	d.ComputedGroupable = map[string]bool{}
	for _, id := range ids {
		d.ComputedGroupable[id] = true
	}
	if !sqlBacked {
		if dataset == "library" {
			d.SourceSQL, err = stageQueryRows(ctx, tx, "pharos_query_rows", sourceRows, schema, libraryNames)
		} else {
			d.SourceSQL, err = stageQueryRows(ctx, tx, "pharos_query_rows", sourceRows, schema)
		}
		if err != nil {
			return queryV2ExecutionResult{}, err
		}
	}
	options := qt.PlanOptions{Identity: queryTableScope + ":" + schema.Name, Now: clock}
	var result queryV2ExecutionResult
	if dataset == "usage" && metrics != nil && rowRequest == nil {
		result.SQLiteV2ExecutionResult, err = s.executeUsageMetricsV2(ctx, tx, d, *metrics, sourceRows, usageHours, options)
	} else {
		result.SQLiteV2ExecutionResult, err = d.ExecuteV2In(ctx, tx, rowRequest, metrics, options)
	}
	if err != nil {
		return result, err
	}
	// Release the connection before host enrichment performs its own reads.
	if err := release(); err != nil {
		return result, err
	}
	if result.Rows != nil {
		switch dataset {
		case "tool_calls":
			err = s.Catalog.priceToolCalls(result.Rows.Rows)
		case "writing_messages":
			err = s.Catalog.attachAuthoredText(ctx, result.Rows.Rows)
		case "library":
			result.Rows.Rows, err = s.Catalog.libraryPage(ctx, result.Rows.Rows)
			if err == nil && params.Get("explain") == "1" && len(evidence) > 0 {
				err = s.Catalog.explainLibraryRows(ctx, result.Rows.Rows, params.Get("search"), evidence)
			}
			if find != nil {
				find.attach(result.Rows.Rows)
				result.Find = &libraryFindSummary{Workspaces: len(find.matches), Limited: find.limited}
			}
		}
	}
	return result, err
}

// Usage keeps daily row filters and selects hourly populations per metric,
// including formulas that reach hour through canonical computed definitions.
func (s *Server) executeUsageMetricsV2(ctx context.Context, tx *sql.Tx, d qt.SQLiteV2Dataset, q qt.MetricQuery, daily, hours []map[string]any, o qt.PlanOptions) (qt.SQLiteV2ExecutionResult, error) {
	o.SourceSQL = d.SourceSQL
	o.ComputedGroupable = d.ComputedGroupable
	o.ExpectedRevisions = q.ExpectedRevisions
	o.Resolver = qt.SQLiteComputedDefinitionResolver{Tx: tx, Scope: d.Scope, Dataset: d.Dataset}
	plans, err := qt.CompileSQLiteMetrics(ctx, q, d.Schema, o)
	if err != nil {
		return qt.SQLiteV2ExecutionResult{}, err
	}
	usesHour := make([]bool, len(q.Metrics))
	needsHours := false
	for i, p := range plans.Metrics {
		usesHour[i] = slices.Contains(p.Dependencies, "hour")
		needsHours = needsHours || usesHour[i]
	}
	hasHourFilter := slices.Contains(whereFields(q.Where), "hour")
	if !needsHours && !hasHourFilter {
		return d.ExecuteV2In(ctx, tx, nil, &q, o)
	}
	hourSource, err := stageQueryRows(ctx, tx, "pharos_query_hours", hours, d.Schema)
	if err != nil {
		return qt.SQLiteV2ExecutionResult{}, err
	}
	dailyWhere := q.Where
	if hasHourFilter {
		ids := map[string]bool{}
		for _, row := range hours {
			ids[firstString(row["agent_session_id"])+"|"+firstString(row["day"])+"|"+firstString(row["model"])] = true
		}
		kept := []map[string]any{}
		for _, row := range daily {
			if ids[firstString(row["id"])] {
				kept = append(kept, row)
			}
		}
		d.SourceSQL, err = stageQueryRows(ctx, tx, "pharos_query_selected_days", kept, d.Schema)
		if err != nil {
			return qt.SQLiteV2ExecutionResult{}, err
		}
		dailyWhere = nil
	}
	// Execute each granularity as one batch in the same authorized transaction.
	dailyRequest, hourRequest, shownHourRequest := q, q, q
	dailyRequest.Metrics = nil
	hourRequest.Metrics = nil
	shownHourRequest.Metrics = nil
	dailyRequest.Where = dailyWhere
	hourRequest.Where = nil
	shownHourRequest.Where = nil
	for i, spec := range q.Metrics {
		if usesHour[i] && spec.Scope == "shownRows" {
			spec.Scope = "allMatching" // Expand the daily table window before reducing hours.
			shownHourRequest.Metrics = append(shownHourRequest.Metrics, spec)
		} else if usesHour[i] {
			hourRequest.Metrics = append(hourRequest.Metrics, spec)
		} else {
			dailyRequest.Metrics = append(dailyRequest.Metrics, spec)
		}
	}
	shownHourSource := ""
	if len(shownHourRequest.Metrics) > 0 {
		window := qt.ServerQueryV2{Version: q.Version, Profile: q.Profile, ExpectedRevisions: q.ExpectedRevisions, PlanToken: q.PlanToken, Snapshot: q.Snapshot,
			WireQuery: qt.WireQuery{Select: []string{"id"}, Where: dailyWhere, OrderBy: qt.OrderBys(q.OrderBy), Limit: q.Limit, Offset: q.Offset}}
		page, e := d.ExecuteV2In(ctx, tx, &window, nil, o)
		if e != nil {
			return qt.SQLiteV2ExecutionResult{}, e
		}
		ids := map[string]bool{}
		for _, row := range page.Rows.Rows {
			ids[firstString(row["id"])] = true
		}
		selected := []map[string]any{}
		for _, row := range hours {
			if ids[firstString(row["agent_session_id"])+"|"+firstString(row["day"])+"|"+firstString(row["model"])] {
				selected = append(selected, row)
			}
		}
		shownHourSource, err = stageQueryRows(ctx, tx, "pharos_query_shown_hours", selected, d.Schema)
		if err != nil {
			return qt.SQLiteV2ExecutionResult{}, err
		}
	}
	byID := map[string]qt.SQLiteMetricV2{}
	for i, request := range []qt.MetricQuery{dailyRequest, hourRequest, shownHourRequest} {
		if len(request.Metrics) == 0 {
			continue
		}
		source := d
		if i == 1 {
			source.SourceSQL = hourSource
		}
		if i == 2 {
			source.SourceSQL = shownHourSource
		}
		result, e := source.ExecuteV2In(ctx, tx, nil, &request, o)
		if e != nil {
			return qt.SQLiteV2ExecutionResult{}, e
		}
		for _, m := range result.Metrics.Metrics {
			if i == 2 {
				m.Scope = "shownRows"
			}
			byID[m.ID] = m
		}
	}
	result := qt.SQLiteV2ExecutionResult{Metrics: &qt.SQLiteMetricsV2Result{Metrics: []qt.SQLiteMetricV2{}}}
	for _, spec := range q.Metrics {
		result.Metrics.Metrics = append(result.Metrics.Metrics, byID[spec.ID])
	}
	return result, nil
}
