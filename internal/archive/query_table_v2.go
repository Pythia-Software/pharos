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
	case "conversation_activity":
		return PharosConversationActivitySchema(), nil
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
	case "findings":
		return PharosFindingsSchema(), nil
	case "parallelism":
		return PharosParallelismSchema(), nil
	}
	return qt.Schema{}, fmt.Errorf("unknown query-table dataset %q", dataset)
}

// queryV2MaxGroups bounds the groups of a V2 metric; past it the metric
// fails rather than dropping groups. Tests lower it.
var queryV2MaxGroups = 2000

// queryV2UsageMaxGroups is Usage's V2 metric group budget. Its compact facts
// chart by hour, week or month and by repository or model, which over long
// ranges exceeds the ordinary budget; V3 allows Usage the same.
var queryV2UsageMaxGroups = 100_000

// Daily Tool summaries can have more than 2,000 program or date groups.
// Match their V3 budget so a valid chart does not need a second execution.
var queryV2ToolMaxGroups = 10_000

func queryV2Dataset(dataset string, schema qt.Schema) qt.SQLiteV2Dataset {
	groups := queryV2MaxGroups
	if schema.Name == "pharos_usage" {
		groups = queryV2UsageMaxGroups
	}
	if schema.Name == "pharos_tools" {
		groups = queryV2ToolMaxGroups
	}
	return qt.SQLiteV2Dataset{Schema: schema, Scope: queryTableScope, Dataset: schema.Name, MaxRows: querytable.MaxLimit, MaxGroups: groups, MaxPopulation: queryTablePopulation}
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
	return d.v2CandidateSource(schema, terms, nil, exact...)
}

func (d sqlDataset) v2WindowedSource(schema qt.Schema, terms []qt.WhereTerm, clock time.Time, exact ...map[string][2]string) (string, []any) {
	return d.v2CandidateSource(schema, terms, &clock, exact...)
}

func (d sqlDataset) v2CandidateSource(schema qt.Schema, terms []qt.WhereTerm, clock *time.Time, exact ...map[string][2]string) (string, []any) {
	source := d.v2Source(schema)
	args := []any{}
	conditions := []string{}
	bind := func(value any) string { args = append(args, value); return fmt.Sprintf("?%d", len(args)) }
	for _, term := range terms {
		branches := []string{}
		saved := len(args)
		for _, clause := range term.Predicates() {
			predicate := ""
			if d.calendarFacts && clause.Field == "day" && !clause.Negated && clause.Op == "is_null" {
				predicate = "t.rowid IN (SELECT rowid FROM tool_query_facts WHERE day IS NULL OR day='')"
			}
			if !clause.Negated && clause.Op == "=" && clause.Value != "" && d.lookups[clause.Field] && d.columns[clause.Field] != "" &&
				(schema.Fields[clause.Field].Kind == qt.FieldText || schema.Fields[clause.Field].Kind == qt.FieldEnum) {
				predicate = d.columns[clause.Field] + "=" + bind(clause.Value)
			}
			if clock != nil && !clause.Negated && d.timestamps[clause.Field] != "" && schema.Fields[clause.Field].Kind == qt.FieldDatetime {
				if from, to, ok := indexedDatetimeBounds(clause, *clock); ok {
					column := d.timestamps[clause.Field]
					if from != "" {
						predicate = column + ">=" + bind(from)
					}
					if to != "" {
						if predicate != "" {
							predicate += " AND "
						}
						predicate += column + "<" + bind(to)
					}
					predicate = "(" + predicate + ")"
				}
			}
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
			if d.queryText.term(clause) {
				predicate = d.queryText.predicate(clause, bind, d.pageFrom == "FROM tool_query_facts t")
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

// Raw ISO text bounds cover whole UTC days, with an extra day on either
// side for valid offset timestamps and mixed fractional precision. They are
// candidates only: the compiler's typed, millisecond-precise predicate remains
// authoritative. Never narrow a negated or partially indexable OR predicate.
func indexedDatetimeBounds(clause qt.WhereClause, clock time.Time) (string, string, bool) {
	if clause.Negated || !slices.Contains([]string{">", ">=", "<", "<=", "="}, clause.Op) {
		return "", "", false
	}
	value, err := qt.ResolveRelativeDatetime(clause.Value, clock)
	if err != nil {
		return "", "", false
	}
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		instant, err = time.Parse("2006-01-02", value)
	}
	if err != nil || instant.Year() < 2 || instant.Year() > 9997 {
		return "", "", false
	}
	utc := instant.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	from, to := day.AddDate(0, 0, -1).Format("2006-01-02"), day.AddDate(0, 0, 2).Format("2006-01-02")
	switch clause.Op {
	case ">", ">=":
		return from, "", true
	case "<", "<=":
		return "", to, true
	default:
		return from, to, true
	}
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
	return stageQueryRowsWithBudget(ctx, tx, name, rows, schema, queryTablePopulation, required...)
}

func stageQueryRowsWithBudget(ctx context.Context, tx *sql.Tx, name string, rows []map[string]any, schema qt.Schema, population int, required ...[]string) (string, error) {
	if len(rows) > population {
		return "", fmt.Errorf("source population exceeds %d rows", population)
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
	// Batch narrow rows so loading a map population does not make one driver
	// round trip per record. Stay below SQLite's conservative parameter limit.
	batchSize := max(1, min(128, 999/max(1, len(names))))
	rowSQL := "(" + strings.Join(placeholders, ",") + ")"
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO "+table+" VALUES "+strings.TrimSuffix(strings.Repeat(rowSQL+",", batchSize), ","))
	if err != nil {
		return "", err
	}
	defer stmt.Close()
	for batch := range slices.Chunk(rows, batchSize) {
		values := make([]any, 0, len(batch)*len(names))
		for _, row := range batch {
			for _, field := range names {
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
				values = append(values, value)
			}
		}
		if len(batch) == batchSize {
			_, err = stmt.ExecContext(ctx, values...)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES "+strings.TrimSuffix(strings.Repeat(rowSQL+",", len(batch)), ","), values...)
		}
		if err != nil {
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
	tx, err := s.Catalog.reader().BeginTx(ctx, nil)
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
	v3 := queryV3Dataset(schema)
	// Capability validation uses the same schema-only source as V2's description.
	// Actual execution replaces it with the authorized dataset source.
	v3.SourceSQL = d.SourceSQL
	if err = authorizeQueryV3Computed(ctx, tx, &v3); err != nil {
		writeError(w, err, 500)
		return
	}
	execution, unavailable, err := describeQueryComputed(ctx, tx, v3, ids)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, map[string]any{"metricCapabilities": d.Capabilities(), "queryCapabilities": v3.Capabilities(), "computedExecution": execution, "computedDiagnostics": unavailable}, 200)
}

type queryComputedDescriber interface {
	DescribeComputedPartialIn(context.Context, *sql.Tx, []string, qt.PlanOptions) (qt.ComputedDescription, error)
}

// Schema changes can invalidate persisted formulas. Keep the complete store
// catalogue repairable, advertising only definitions whose whole graph compiles.
// Database and context failures still fail the request rather than hiding data.
func describeQueryComputed(ctx context.Context, tx *sql.Tx, d queryComputedDescriber, ids []string) (qt.SQLiteComputedExecution, map[string]*qt.PlanDiagnostic, error) {
	execution := qt.SQLiteComputedExecution{Profile: qt.SQLiteExpressionProfile, ResolvedRevisions: map[string]string{}, Fields: map[string]qt.SQLiteComputedFieldCapability{}}
	unavailable := map[string]*qt.PlanDiagnostic{}
	// The adapter bounds each description to 64 definitions. A saved catalogue
	// can be larger even though one execution references at most 64 definitions.
	batches := slices.Collect(slices.Chunk(ids, 64))
	if len(batches) == 0 {
		batches = [][]string{nil}
	}
	for _, batch := range batches {
		current, err := d.DescribeComputedPartialIn(ctx, tx, batch, qt.PlanOptions{})
		if err != nil {
			return execution, unavailable, err
		}
		maps.Copy(execution.ResolvedRevisions, current.Execution.ResolvedRevisions)
		maps.Copy(execution.Fields, current.Execution.Fields)
		maps.Copy(unavailable, current.Diagnostics)
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
	deadline := queryTableDeadline
	if r.URL.Query().Get("interactive_search") == "1" {
		deadline = 2500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()
	result, err := s.executeQueryV2(ctx, dataset, r.URL.Query(), schema, rowRequest, metrics)
	if err != nil {
		if deadline != queryTableDeadline && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("This exact record query exceeded 2.5 seconds. Add a workspace, conversation, or date filter, or use Library's bounded text search for broad searches.")
		}
		writeError(w, err, 400)
		return
	}
	if rowRequest != nil {
		writeJSON(w, struct {
			*qt.SQLiteRowsV2Result
			Find *libraryFindSummary `json:"find,omitempty"`
		}{result.Rows, result.Find}, 200)
	} else {
		if result.Find != nil {
			encoded, _ := json.Marshal(result.Metrics)
			var payload map[string]any
			_ = json.Unmarshal(encoded, &payload)
			payload["find"] = result.Find
			writeJSON(w, payload, 200)
		} else {
			writeJSON(w, result.Metrics, 200)
		}
	}
}

// leaseQueryV2 is how executeStagedQueryV2 begins its transaction. Tests
// wrap it to see which requests lease temporary storage.
var leaseQueryV2 = beginQueryV2

// Bounded v2 populations use memory-backed temporary relations. Keep this
// setting on the leased connection only; restore it before returning the pool
// connection or performing host enrichment.
func beginQueryV2(ctx context.Context, db *sql.DB) (*sql.Tx, func() error, error) {
	return beginQueryWithTempStore(ctx, db, 2)
}

// V3 can stage much larger complete populations, so its temporary relations
// must be able to spill to disk. The mode is leased and restored just like V2.
func beginQueryV3(ctx context.Context, db *sql.DB) (*sql.Tx, func() error, error) {
	return beginQueryWithTempStore(ctx, db, 1)
}

func beginQueryWithTempStore(ctx context.Context, db *sql.DB, mode int) (*sql.Tx, func() error, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	var original int
	if err = conn.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&original); err != nil {
		conn.Close()
		return nil, nil, err
	}
	var originalCache int
	if err = conn.QueryRowContext(ctx, "PRAGMA temp.cache_size").Scan(&originalCache); err != nil {
		conn.Close()
		return nil, nil, err
	}
	if original != mode {
		if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA temp_store=%d", mode)); err != nil {
			conn.Close()
			return nil, nil, err
		}
	}
	// FILE temp storage defaults to a zero-page cache. Grouping then writes
	// scratch pages to the SSD repeatedly even for small, bounded populations.
	// Give this leased connection 64 MiB of scratch cache while retaining spill.
	if _, err = conn.ExecContext(ctx, "PRAGMA temp.cache_size=-65536"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		conn.Close()
		return nil, nil, err
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
		if original != mode {
			if _, resetError := conn.ExecContext(cleanup, fmt.Sprintf("PRAGMA temp_store=%d", original)); resetError != nil {
				// A cancelled transaction may have closed its physical connection already.
				// Otherwise discard a connection whose original settings cannot be restored.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
		if _, err := conn.ExecContext(cleanup, fmt.Sprintf("PRAGMA temp.cache_size=%d", originalCache)); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			conn.Close()
			return errors.Join(rollbackError, err)
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
// including canonical transitive formulas and hidden/default ordering. Execution
// revalidates revisions in its own transaction before consuming the projection.
func queryV2SourceFields(ctx context.Context, db *sql.DB, schema qt.Schema, rows *qt.ServerQueryV2, metrics *qt.MetricQuery, clock time.Time) ([]string, error) {
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
		dependencies, err := metricDependencies(ctx, *metrics, schema, options)
		if err != nil {
			return nil, err
		}
		for _, names := range dependencies {
			add(names)
		}
		add(whereFields(metrics.Where))
	}
	return slices.Sorted(maps.Keys(fields)), nil
}

// metricDependencies compiles each metric on its own, so one invalid metric
// cannot fail discovery for its siblings: the adapter reports that metric's
// error in its own result. Entries for metrics that fail to compile are nil.
// Request-level problems fail every metric here and fail execution later.
// Dependencies include the fields of the metric's own WHERE.
func metricDependencies(ctx context.Context, q qt.MetricQuery, schema qt.Schema, options qt.PlanOptions) ([][]string, error) {
	out := make([][]string, len(q.Metrics))
	for i, spec := range q.Metrics {
		one := q
		one.Metrics = []qt.AggSpec{spec}
		plans, err := qt.CompileSQLiteMetrics(ctx, one, schema, options)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			continue
		}
		for _, plan := range plans.Metrics {
			out[i] = append(out[i], plan.Dependencies...)
		}
	}
	return out, nil
}

func (s *Server) executeQueryV2(ctx context.Context, dataset string, params url.Values, schema qt.Schema, rowRequest *qt.ServerQueryV2, metrics *qt.MetricQuery, populationLimit ...int) (queryV2ExecutionResult, error) {
	if dataset == "usage" {
		var err error
		ctx, err = withCarbonPreferences(ctx, params.Get("carbon_settings"))
		if err != nil {
			return queryV2ExecutionResult{}, err
		}
	}
	if rowRequest != nil {
		copy := *rowRequest
		copy.Select = queryRowActionFields(dataset, rowRequest.Select)
		rowRequest = &copy
	}
	clock := time.Now()
	d := queryV2Dataset(dataset, schema)
	if len(populationLimit) > 0 {
		d.MaxPopulation = min(d.MaxPopulation, populationLimit[0])
	}
	table, sqlBacked := sqlDatasetFor(dataset)
	var sourceRows, usageHours []map[string]any
	var sourceNames []string
	var find *libraryFindSet
	var evidence map[string]*searchEvidence
	var bounds map[string][2]string
	if sqlBacked {
		if err := s.Catalog.prepareSQLDataset(ctx, dataset); err != nil {
			return queryV2ExecutionResult{}, err
		}
		var textErr error
		table, textErr = s.Catalog.queryTextDataset(ctx, dataset, table)
		if textErr != nil {
			return queryV2ExecutionResult{}, textErr
		}
		if dataset == "tool_calls" {
			ready, e := s.Catalog.toolSearchReady(ctx)
			if e != nil {
				return queryV2ExecutionResult{}, e
			}
			table.toolSearchIndexed = ready
		}
		if dataset == "writing_messages" {
			var err error
			table, find, err = s.Catalog.messageFindDataset(ctx, params, table)
			if err != nil {
				return queryV2ExecutionResult{}, err
			}
		}
		where := []qt.WhereTerm{}
		if rowRequest != nil {
			where = rowRequest.Where
		}
		if metrics != nil {
			where = metrics.Where
		}
		var e error
		bounds, e = queryBucketBounds(ctx, s.Catalog.reader(), where)
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
		names, e := queryV2SourceFields(ctx, s.Catalog.reader(), schema, rowRequest, metrics, clock)
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
		projected := table.project(names)
		if metrics != nil {
			var covered bool
			projected, covered = projected.preferCoveringScan(where)
			if !covered && len(table.residualWhere(where)) < len(where) {
				copy := *metrics
				copy.Where = table.residualWhere(where)
				exactNames, e := queryV2SourceFields(ctx, s.Catalog.reader(), schema, nil, &copy, clock)
				if e != nil {
					return queryV2ExecutionResult{}, e
				}
				_, covered = table.project(exactNames).preferCoveringScan(where)
			}
			if covered {
				// Narrow covered scalar populations can read the full live ledger
				// without copying command/result payloads. Exact sample/group limits
				// and the request deadline remain enforced by the adapter.
				d.MaxPopulation = 2_000_000
				if len(populationLimit) > 0 {
					d.MaxPopulation = min(d.MaxPopulation, populationLimit[0])
				}
			}
		}
		d.SourceSQL, d.SourceArgs = projected.v2WindowedSource(schema, where, clock, bounds)
	} else {
		var err error
		sourceNames, err = queryV2SourceFields(ctx, s.Catalog.reader(), schema, rowRequest, metrics, clock)
		if err != nil {
			return queryV2ExecutionResult{}, err
		}
		if dataset == "library" {
			var where []querytable.WhereTerm
			if rowRequest != nil {
				where = rowRequest.Where
			}
			if metrics != nil {
				where = metrics.Where
			}
			// Computed predicates remain in the v2 engine. Only complete
			// AND terms over stored fields can safely narrow candidates.
			localSchema, e := s.queryTableSchema("library")
			if e != nil {
				return queryV2ExecutionResult{}, e
			}
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
			sourceRows, find, err = s.libraryRows(ctx, params, libraryFieldsFor(sourceNames...), &evidence, early)
		} else {
			var where []querytable.WhereTerm
			if rowRequest != nil && metrics == nil {
				where = rowRequest.Where
			} else if metrics != nil && rowRequest == nil {
				where = metrics.Where
			}
			sourceRows, err = s.queryTableRows(ctx, dataset, params, queryFieldsFor(dataset, sourceNames...), s.mapRowsWhere(dataset, where, clock)...)
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
		if slices.Contains(sourceNames, "hour") {
			usageHours, e = s.Catalog.hourlyUsageForAggregation(ctx, metrics.Where, sourceNames)
			if e != nil {
				return queryV2ExecutionResult{}, e
			}
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
		hours, e := s.Catalog.hourlyUsageForAggregation(ctx, copy.Where, sourceNames)
		if e != nil {
			return queryV2ExecutionResult{}, e
		}
		ids := map[string]bool{}
		for _, row := range hours {
			ids[usageDailyID(row)] = true
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
	options := qt.PlanOptions{Identity: queryTableScope + ":" + schema.Name, Now: clock}
	var result queryV2ExecutionResult
	var err error
	handled := false
	if metrics != nil && rowRequest == nil && (sqlBacked || dataset == "usage") {
		result.SQLiteV2ExecutionResult, handled, err = s.metricsWithoutStaging(ctx, dataset, table, d, *metrics, options, sourceRows, usageHours, len(populationLimit) > 0)
		if err != nil {
			return result, err
		}
	}
	if !handled {
		if err = s.executeStagedQueryV2(ctx, dataset, table, sqlBacked, d, rowRequest, metrics, options, bounds, sourceRows, usageHours, sourceNames, &result, populationLimit...); err != nil {
			return result, err
		}
	}
	if find != nil {
		summary := find.summary()
		result.Find = &summary
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
				summary := find.summary()
				result.Find = &summary
			}
		}
	}
	return result, err
}

// Collection actions need source identity even when their columns are hidden.
// Keep these small dimensions in the row payload; the client's projection
// still controls which columns are displayed. Group results have no such
// source-record identity and must never receive these fields.
func queryRowActionFields(dataset string, selected []string) []string {
	if len(selected) == 0 {
		return selected
	}
	var required []string
	switch dataset {
	case "tools":
		required = []string{"day", "tool_name", "tool_category", "mcp_server", "program", "subcommand", "command_category", "provider", "model", "effort", "repository_name", "session_kind", "source_kind"}
	case "usage":
		required = []string{"workspace_id", "conversation_id", "first_usage_at"}
	case "conversation_activity":
		required = []string{"workspace_id", "conversation_id", "message_id", "event_at"}
	case "writing_messages":
		required = []string{"workspace_id", "conversation_id"}
	case "tool_calls":
		required = []string{"workspace_id", "conversation_id", "call_message_id", "result_message_id", "call_id"}
	case "parallelism":
		required = []string{"conversation_id", "started_at", "location"}
	case "skill_usages":
		required = []string{"workspace_id", "conversation_id", "tool_call_id", "evidence_message_id", "body_message_id"}
	}
	if len(required) == 0 {
		return selected
	}
	fields := slices.Clone(selected)
	for _, field := range required {
		if !slices.Contains(fields, field) {
			fields = append(fields, field)
		}
	}
	return fields
}

// metricsWithoutStaging answers metrics that need no temporary tables in a
// plain read transaction: bare counts, Machine Time's cubes and native counts
// and sums over a SQL dataset, and native Usage metrics over its rows in
// memory. Leasing memory-backed temporary storage resets the connection's
// schema on the way in and out, a fixed cost of several milliseconds that
// these requests need not pay. It reports false for any other request.
func (s *Server) metricsWithoutStaging(ctx context.Context, dataset string, table sqlDataset, d qt.SQLiteV2Dataset, q qt.MetricQuery, o qt.PlanOptions, daily, hours []map[string]any, budgeted bool) (qt.SQLiteV2ExecutionResult, bool, error) {
	tx, err := s.Catalog.reader().BeginTx(ctx, nil)
	if err != nil {
		return qt.SQLiteV2ExecutionResult{}, true, err
	}
	defer tx.Rollback()
	ids, err := queryComputedIDs(ctx, tx, d.Schema.Name)
	if err != nil {
		return qt.SQLiteV2ExecutionResult{}, true, err
	}
	d.ComputedGroupable = map[string]bool{}
	for _, id := range ids {
		d.ComputedGroupable[id] = true
	}
	if dataset == "usage" {
		return s.usageMetricsInMemory(ctx, tx, d, q, o, daily, hours)
	}
	result, handled, err := s.countOnlyMetricsV2(ctx, tx, table, d, q, o)
	// Machine Time's cubes read the tool projection. A caller's own population
	// budget bounds raw calls, so leave its requests to them.
	if !handled && err == nil && dataset == "tool_calls" && !budgeted {
		result, handled, err = s.Catalog.machineTimeMetricsV2(ctx, tx, d, q, o)
	}
	if !handled && err == nil {
		result, handled, err = s.Catalog.nativeMetricsV2(ctx, tx, d, q, o, &table)
	}
	return result, handled, err
}

// executeStagedQueryV2 runs rows, and metrics no accelerator answered, in a
// transaction leasing memory-backed temporary storage for the adapter's
// staging, and releases it before the caller enriches the rows.
func (s *Server) executeStagedQueryV2(ctx context.Context, dataset string, table sqlDataset, sqlBacked bool, d qt.SQLiteV2Dataset, rowRequest *qt.ServerQueryV2, metrics *qt.MetricQuery, options qt.PlanOptions, bounds map[string][2]string, sourceRows, usageHours []map[string]any, sourceNames []string, out *queryV2ExecutionResult, populationLimit ...int) error {
	tx, release, err := leaseQueryV2(ctx, s.Catalog.reader())
	if err != nil {
		return err
	}
	defer release()
	ids, err := queryComputedIDs(ctx, tx, d.Schema.Name)
	if err != nil {
		return err
	}
	d.ComputedGroupable = map[string]bool{}
	for _, id := range ids {
		d.ComputedGroupable[id] = true
	}
	if !sqlBacked {
		d.SourceSQL, err = stageQueryRows(ctx, tx, "pharos_query_rows", sourceRows, d.Schema, sourceNames)
		if err != nil {
			return err
		}
	}
	indexed := false
	if sqlBacked && rowRequest != nil && metrics == nil {
		population := 10_000_000
		if len(populationLimit) > 0 {
			population = min(population, populationLimit[0])
		}
		out.SQLiteV2ExecutionResult, indexed, err = s.indexedQueryRowsV2(ctx, tx, table, d, *rowRequest, options, bounds, population)
	}
	if indexed {
		// The indexed window has already executed in this same transaction.
	} else if dataset == "usage" && metrics != nil && rowRequest == nil {
		out.SQLiteV2ExecutionResult, err = s.executeUsageMetricsV2(ctx, tx, d, *metrics, sourceRows, usageHours, options, sourceNames)
	} else if metrics != nil && rowRequest == nil && !sqlBacked {
		// metricsWithoutStaging tried the native path over SQL datasets.
		out.SQLiteV2ExecutionResult, err = s.Catalog.executeMetricsV2(ctx, tx, d, *metrics, options)
	} else {
		out.SQLiteV2ExecutionResult, err = d.ExecuteV2In(ctx, tx, rowRequest, metrics, options)
	}
	if err != nil {
		return err
	}
	// Release the connection before host enrichment performs its own reads.
	return release()
}

// usageHourMetrics reports which of q's metrics read hourly rows. Granularity
// comes from a metric's inputs, not the daily row WHERE or page ordering; an
// hour predicate selects days for daily summaries.
func usageHourMetrics(ctx context.Context, d qt.SQLiteV2Dataset, q qt.MetricQuery, o qt.PlanOptions) ([]bool, bool, error) {
	inputs := q
	inputs.Where, inputs.OrderBy = nil, nil
	inputs.Metrics = slices.Clone(q.Metrics)
	for i := range inputs.Metrics {
		inputs.Metrics[i].Scope = "allMatching"
	}
	dependencies, err := metricDependencies(ctx, inputs, d.Schema, o)
	if err != nil {
		return nil, false, err
	}
	usesHour := make([]bool, len(q.Metrics))
	needsHours := false
	for i, names := range dependencies {
		usesHour[i] = slices.Contains(names, "hour")
		needsHours = needsHours || usesHour[i]
	}
	return usesHour, needsHours, nil
}

// usageDaysOf is the daily rows holding any of hours.
func usageDaysOf(daily, hours []map[string]any) []map[string]any {
	ids := map[string]bool{}
	for _, row := range hours {
		ids[usageDailyID(row)] = true
	}
	kept := []map[string]any{}
	for _, row := range daily {
		if ids[firstString(row["id"])] {
			kept = append(kept, row)
		}
	}
	return kept
}

// usageMetricsInMemory answers Usage metrics from its rows in memory with
// Query Table's in-memory executor, without copying them into SQLite. It
// splits the request as executeUsageMetricsV2 does: hour metrics read the
// hourly rows hourlyUsageForAggregation selected, and daily metrics read the
// daily rows under the request's filter, or, when it filters hours, the days
// holding the selected hours. It reports false for anything the executor
// cannot answer exactly, shown-row metrics and computed fields included, for
// the staged path to answer.
func (s *Server) usageMetricsInMemory(ctx context.Context, tx *sql.Tx, d qt.SQLiteV2Dataset, q qt.MetricQuery, o qt.PlanOptions, daily, hours []map[string]any) (qt.SQLiteV2ExecutionResult, bool, error) {
	if !nativeMetrics {
		return qt.SQLiteV2ExecutionResult{}, false, nil
	}
	if len(daily) > queryTablePopulation {
		// As the staged path refuses to copy them.
		return qt.SQLiteV2ExecutionResult{}, true, fmt.Errorf("source population exceeds %d rows", queryTablePopulation)
	}
	o.SourceSQL = (sqlDataset{}).v2Source(d.Schema)
	o.ComputedGroupable = d.ComputedGroupable
	o.ExpectedRevisions = q.ExpectedRevisions
	o.Resolver = qt.SQLiteComputedDefinitionResolver{Tx: tx, Scope: d.Scope, Dataset: d.Dataset}
	usesHour, needsHours, err := usageHourMetrics(ctx, d, q, o)
	if err != nil {
		return qt.SQLiteV2ExecutionResult{}, true, err
	}
	hasHourFilter := slices.Contains(whereFields(q.Where), "hour")
	if !needsHours && !hasHourFilter {
		return usageRowMetricsV2(ctx, tx, d, q, o, daily)
	}
	dailyRows, dailyRequest, hourRequest := daily, q, q
	dailyRequest.Metrics, hourRequest.Metrics, hourRequest.Where = nil, nil, nil
	if hasHourFilter {
		dailyRows, dailyRequest.Where = usageDaysOf(daily, hours), nil
	}
	for i, spec := range q.Metrics {
		switch {
		case usesHour[i] && spec.Scope == "shownRows":
			return qt.SQLiteV2ExecutionResult{}, false, nil
		case usesHour[i]:
			hourRequest.Metrics = append(hourRequest.Metrics, spec)
		default:
			dailyRequest.Metrics = append(dailyRequest.Metrics, spec)
		}
	}
	byID := map[string]qt.SQLiteMetricV2{}
	for _, part := range []struct {
		q    qt.MetricQuery
		rows []map[string]any
	}{{dailyRequest, dailyRows}, {hourRequest, hours}} {
		if len(part.q.Metrics) == 0 {
			continue
		}
		result, handled, err := usageRowMetricsV2(ctx, tx, d, part.q, o, part.rows)
		if err != nil || !handled {
			return qt.SQLiteV2ExecutionResult{}, err != nil, err
		}
		for _, m := range result.Metrics.Metrics {
			byID[m.ID] = m
		}
	}
	result := qt.SQLiteV2ExecutionResult{Metrics: &qt.SQLiteMetricsV2Result{Metrics: []qt.SQLiteMetricV2{}}}
	for _, spec := range q.Metrics {
		result.Metrics.Metrics = append(result.Metrics.Metrics, byID[spec.ID])
	}
	return result, true, nil
}

// usageRowMetricsV2 runs q over rows in memory, and reports false where the
// executor asks for the rows to be staged instead.
func usageRowMetricsV2(ctx context.Context, tx *sql.Tx, d qt.SQLiteV2Dataset, q qt.MetricQuery, o qt.PlanOptions, rows []map[string]any) (qt.SQLiteV2ExecutionResult, bool, error) {
	result, err := d.ExecuteV2Rows(ctx, tx, rows, q, o)
	if errors.Is(err, qt.ErrRowsUnsupported) {
		return qt.SQLiteV2ExecutionResult{}, false, nil
	}
	return result, true, err
}

// Usage keeps daily row filters and selects hourly populations per metric,
// including formulas that reach hour through canonical computed definitions.
func (s *Server) executeUsageMetricsV2(ctx context.Context, tx *sql.Tx, d qt.SQLiteV2Dataset, q qt.MetricQuery, daily, hours []map[string]any, o qt.PlanOptions, required ...[]string) (qt.SQLiteV2ExecutionResult, error) {
	o.SourceSQL = d.SourceSQL
	o.ComputedGroupable = d.ComputedGroupable
	o.ExpectedRevisions = q.ExpectedRevisions
	o.Resolver = qt.SQLiteComputedDefinitionResolver{Tx: tx, Scope: d.Scope, Dataset: d.Dataset}
	usesHour, needsHours, err := usageHourMetrics(ctx, d, q, o)
	if err != nil {
		return qt.SQLiteV2ExecutionResult{}, err
	}
	hasHourFilter := slices.Contains(whereFields(q.Where), "hour")
	if !needsHours && !hasHourFilter {
		return s.Catalog.executeMetricsV2(ctx, tx, d, q, o)
	}
	hourSource, err := stageQueryRows(ctx, tx, "pharos_query_hours", hours, d.Schema, required...)
	if err != nil {
		return qt.SQLiteV2ExecutionResult{}, err
	}
	dailyWhere := q.Where
	if hasHourFilter {
		d.SourceSQL, err = stageQueryRows(ctx, tx, "pharos_query_selected_days", usageDaysOf(daily, hours), d.Schema, required...)
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
			if ids[usageDailyID(row)] {
				selected = append(selected, row)
			}
		}
		shownHourSource, err = stageQueryRows(ctx, tx, "pharos_query_shown_hours", selected, d.Schema, required...)
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
		result, e := s.Catalog.executeMetricsV2(ctx, tx, source, request, o)
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
