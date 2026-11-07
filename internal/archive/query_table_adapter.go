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
	"slices"
	"strings"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
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
	case "people_message_labels":
		return PharosPeopleMessageLabelsSchema(), nil
	case "message_labels":
		return PharosMessageLabelsSchema(), nil
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
	case "compactions":
		return PharosCompactionsSchema(), nil
	case "model_requests":
		return PharosModelRequestsSchema(), nil
	case "skill_usages":
		return PharosSkillUsagesSchema(), nil
	case "tl1_attempts":
		return PharosTl1AttemptsSchema(), nil
	case "safety":
		return PharosSafetySchema(), nil
	case "fixes":
		return PharosFixesSchema(), nil
	case "findings":
		return PharosFindingsSchema(), nil
	case "parallelism":
		return PharosParallelismSchema(), nil
	case "rhythm":
		return PharosRhythmSchema(), nil
	case "session_shape":
		return PharosSessionShapeSchema(), nil
	case "agent_events":
		return PharosAgentEventsSchema(), nil
	case "agent_event_messages":
		return PharosAgentEventMessagesSchema(), nil
	}
	return qt.Schema{}, fmt.Errorf("unknown query-table dataset %q", dataset)
}

// The outer schema binds to stable column aliases; the host owns the joins and
// mirrored-work exclusion. SQLite can flatten this projection and use indexes.
func (d sqlDataset) querySourceSQL(schema qt.Schema) string {
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
func (d sqlDataset) indexedQuerySource(schema qt.Schema, terms []qt.WhereTerm, exact ...map[string][2]string) (string, []any) {
	return d.candidateQuerySource(schema, terms, nil, exact...)
}

func (d sqlDataset) windowedQuerySource(schema qt.Schema, terms []qt.WhereTerm, clock time.Time, exact ...map[string][2]string) (string, []any) {
	return d.candidateQuerySource(schema, terms, &clock, exact...)
}

func (d sqlDataset) candidateQuerySource(schema qt.Schema, terms []qt.WhereTerm, clock *time.Time, exact ...map[string][2]string) (string, []any) {
	source := d.querySourceSQL(schema)
	args := []any{}
	conditions := []string{}
	bind := func(value any) string { args = append(args, value); return fmt.Sprintf("?%d", len(args)) }
	for _, term := range terms {
		// Each literal's candidate (or exact text) predicate replaces it in
		// place: AND within a branch, OR across branches. The term narrows
		// only when every literal has one.
		branches := []string{}
		saved := len(args)
	literals:
		for _, branch := range term.Branches() {
			conjuncts := make([]string, 0, len(branch))
			for _, clause := range branch {
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
					break literals
				}
				conjuncts = append(conjuncts, predicate)
			}
			if len(conjuncts) == 1 {
				branches = append(branches, conjuncts[0])
			} else {
				branches = append(branches, "("+strings.Join(conjuncts, " AND ")+")")
			}
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
		return "", &qt.PlanDiagnostic{Code: qt.ErrorCodeResourceLimit, Message: fmt.Sprintf("source population exceeds %d rows", population)}
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
	return (sqlDataset{columns: bindings, from: "FROM " + table}).querySourceSQL(schema), nil
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
	d := queryV3Dataset(schema)
	// Describe validates plans without reading a population.
	d.SourceSQL = (sqlDataset{}).querySourceSQL(schema)
	ids, err := queryComputedIDs(ctx, tx, schema.Name)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	v3 := queryV3Dataset(schema)
	// Capability validation uses a schema-only source.
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
	writeJSON(w, map[string]any{"queryCapabilities": v3.Capabilities(), "computedExecution": execution, "computedDiagnostics": unavailable}, 200)
}

type queryComputedDescriber interface {
	DescribeComputedPartialIn(context.Context, *sql.Tx, []string, qt.PlanOptions) (qt.ComputedDescription, error)
}

// Schema changes can invalidate persisted formulas. Keep the complete store
// catalogue repairable, advertising only definitions whose whole graph compiles.
// Database and context failures still fail the request rather than hiding data.
func describeQueryComputed(ctx context.Context, tx *sql.Tx, d queryComputedDescriber, ids []string) (qt.ComputedExecution, map[string]*qt.PlanDiagnostic, error) {
	execution := qt.ComputedExecution{Profile: qt.SQLiteExpressionProfile, ResolvedRevisions: map[string]string{}, Fields: map[string]qt.ComputedFieldCapability{}}
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
	d := queryV3Dataset(schema)
	d.Scope = scope
	d.SourceSQL = (sqlDataset{}).querySourceSQL(schema)
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
	case "rhythm":
		required = []string{"conversation_id", "started_at"}
	case "session_shape":
		required = []string{"workspace_id", "conversation_id", "started_at"}
	case "skill_usages":
		required = []string{"workspace_id", "conversation_id", "tool_call_id", "evidence_message_id", "body_message_id"}
	case "people_message_labels":
		required = []string{"source_workspace_id", "source_conversation_id", "message_id"}
	case "agent_events", "agent_event_messages", "message_labels", "compactions":
		required = []string{"workspace_id", "conversation_id", "message_id"}
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
