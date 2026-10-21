package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

type queryCountKey struct {
	generation int64
	plan       [32]byte
}

// Counts are tagged with the generation read inside the execution transaction,
// rather than a timer or a different connection's observation. A writer cannot
// make the page and its reused total describe different snapshots.
type queryCountCache struct {
	mu     sync.Mutex
	counts map[queryCountKey]int64
}

func (c *Catalog) queryCount(ctx context.Context, tx *sql.Tx, statement string, args []any, fast ...func(context.Context, *sql.Tx) (int64, bool, error)) (int64, error) {
	var generation int64
	if err := tx.QueryRowContext(ctx, "SELECT value FROM query_cache_generation WHERE id=1").Scan(&generation); err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return 0, err
	}
	key := queryCountKey{generation, sha256.Sum256(append([]byte(statement+"\x00"), encoded...))}
	cache := &c.queryCounts
	cache.mu.Lock()
	total, exists := cache.counts[key]
	cache.mu.Unlock()
	if exists {
		return total, nil
	}
	for _, count := range fast {
		total, exists, err = count(ctx, tx)
		if err != nil {
			return 0, err
		}
		if exists {
			break
		}
	}
	if !exists {
		if err := tx.QueryRowContext(ctx, statement, args...).Scan(&total); err != nil {
			return 0, err
		}
	}
	cache.mu.Lock()
	if len(cache.counts) >= 128 {
		cache.counts = nil
	}
	if cache.counts == nil {
		cache.counts = map[queryCountKey]int64{}
	}
	cache.counts[key] = total
	cache.mu.Unlock()
	return total, nil
}

// The cube was built from the same trusted joins and mirror exclusion as the
// raw ledger. Use its indexed counts only when every freshness guard matches
// inside this read transaction; pending analyses always fall back to raw rows.
func (c *Catalog) toolCubeFresh(ctx context.Context, tx *sql.Tx) (bool, error) {
	var fresh bool
	err := tx.QueryRowContext(ctx, `SELECT
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_generation'),'')=
 COALESCE((SELECT value FROM meta WHERE key='tool_ledger_generation'),'')||? AND
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_day'),'')=? AND
 NOT EXISTS(SELECT 1 FROM tool_rollup_dirty) AND
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_full_revision'),'')=
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_built_full_revision'),'') AND
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_timezone'),'')=?`, "/"+toolRollupVersion, c.clock().Format("2006-01-02"), toolRollupTimezone()).Scan(&fresh)
	return fresh, err
}

func (c *Catalog) freshToolCubeCount(ctx context.Context, tx *sql.Tx) (int64, bool, error) {
	fresh, err := c.toolCubeFresh(ctx, tx)
	if err != nil || !fresh {
		return 0, false, err
	}
	var total int64
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(call_count),0) FROM tool_call_cube").Scan(&total)
	return total, err == nil, err
}

// Timestamp-only page counts can use the same exact whole-day cube population
// as Machine Time, provided every freshness guard holds in this transaction.
func (c *Catalog) freshToolCubeRangeCount(ctx context.Context, tx *sql.Tx, terms []querytable.WhereTerm) (int64, bool, error) {
	lower, upper, rest, valid := machineTimeDayRange(terms)
	if !valid || lower == "" || upper == "" || len(rest) != 0 {
		return 0, false, nil
	}
	fresh, err := c.toolCubeFresh(ctx, tx)
	if err != nil || !fresh {
		return 0, false, err
	}
	var total int64
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(call_count),0) FROM tool_call_cube WHERE day>=? AND day<?", lower, upper).Scan(&total)
	return total, err == nil, err
}

// Broad activity drilldowns can match most of the raw ledger. The compact
// projection supplies the same exact count without joining every source call.
func (c *Catalog) freshMachineCubeCount(ctx context.Context, tx *sql.Tx, terms []querytable.WhereTerm, schema querytable.Schema) (int64, bool, error) {
	lower, upper, where, valid := machineTimeDayRange(terms)
	if !valid {
		return 0, false, nil
	}
	for _, field := range whereFields(where) {
		if !machineTimeDimensions.has(field) {
			return 0, false, nil
		}
	}
	fresh, err := c.toolCubeFresh(ctx, tx)
	if err != nil || !fresh {
		return 0, false, err
	}
	rows, err := c.machineTimeRowsIn(ctx, tx)
	if err != nil {
		return 0, false, err
	}
	rows, err = querytable.Filter(rows, where, schema)
	if err != nil {
		return 0, false, err
	}
	var total int64
	for _, row := range rows {
		day := firstString(row["day"])
		if machineTimeWithinDay(day, lower, upper) {
			total += integer(row["machine_calls"])
		}
	}
	return total, true, nil
}

// Full summary-row identities include source/session/category dimensions that
// the smaller Machine Time projection omits. The published cube keeps them all.
func (c *Catalog) freshToolDimensionCount(ctx context.Context, tx *sql.Tx, terms []qt.WhereTerm, schema qt.Schema) (int64, bool, error) {
	bound := schema
	bound.Fields = maps.Clone(schema.Fields)
	for _, term := range terms {
		for _, p := range term.Predicates() {
			expr, ok := toolCallCube.dimensionFor(p.Field)
			if presence, found := toolCallCube.present[p.Field]; found && (p.Op == "is_null" || p.Op == "is_not_null") {
				expr, ok = "CASE WHEN "+presence+" THEN 'present' END", true
			}
			if !ok {
				return 0, false, nil
			}
			field := bound.Fields[p.Field]
			field.Expr = expr
			bound.Fields[p.Field] = field
		}
	}
	fresh, err := c.toolCubeFresh(ctx, tx)
	if err != nil || !fresh {
		return 0, false, err
	}
	filter, err := qt.CompileSQLiteWhere(terms, bound, qt.SQLiteOptions{})
	if err != nil {
		return 0, false, nil
	}
	where, args := filter.SQL, slices.Clone(filter.Args)
	for _, term := range terms {
		if term.IsGroup() || term.Negated || term.Op != "=" || term.Value == "" {
			continue
		}
		field := bound.Fields[term.Field]
		if field.Kind != qt.FieldText && field.Kind != qt.FieldEnum {
			continue
		}
		if where != "" {
			where += " AND "
		}
		where += field.Expr + "=?"
		args = append(args, term.Value)
	}
	if where != "" {
		where = " WHERE " + where
	}
	var total int64
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(call_count),0) FROM tool_call_cube"+where, args...).Scan(&total)
	return total, err == nil, err
}

func (c *Catalog) freshToolTextCount(ctx context.Context, tx *sql.Tx, table sqlDataset, terms []qt.WhereTerm, schema qt.Schema, now time.Time) (int64, bool, error) {
	base, excluded, ok := textComplementWhere(table, terms)
	if !ok {
		return 0, false, nil
	}
	total, used, err := c.freshToolDimensionCount(ctx, tx, base, schema)
	if err != nil || !used {
		return 0, used, err
	}
	residual := table.residualWhere(excluded)
	source, args := table.project(append(whereFields(residual), schema.IDField)).v2WindowedSource(schema, excluded, now)
	filter, err := qt.CompileSQLiteWhere(residual, schema, qt.SQLiteOptions{Now: now})
	if err != nil {
		return 0, false, nil
	}
	if filter.SQL != "" {
		source = "SELECT * FROM (" + source + ") AS r WHERE " + numberQueryParameters(filter.SQL, len(args))
	}
	var removed int64
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+source+")", append(args, filter.Args...)...).Scan(&removed)
	return total - removed, err == nil, err
}

func (s *Server) countOnlyMetricsV2(ctx context.Context, tx *sql.Tx, table sqlDataset, d qt.SQLiteV2Dataset, q qt.MetricQuery, o qt.PlanOptions) (qt.SQLiteV2ExecutionResult, bool, error) {
	if d.Dataset != "pharos_tool_calls" || len(q.Where) > 0 || len(q.Metrics) == 0 || len(d.SourceArgs) > 0 {
		return qt.SQLiteV2ExecutionResult{}, false, nil
	}
	for _, spec := range q.Metrics {
		count := spec.Op == "count" && spec.Field == "" && spec.Expression == "" || strings.ToUpper(strings.TrimSpace(spec.Expression)) == "COUNT()"
		// A metric's own WHERE narrows its population below the table total.
		if !count || len(spec.GroupBy) > 0 || len(spec.Where) > 0 || spec.MinCount > 0 || spec.ExpressionY != "" || spec.Distribution != nil || spec.DateGrouping != nil || spec.Scope != "" && spec.Scope != "allMatching" {
			return qt.SQLiteV2ExecutionResult{}, false, nil
		}
	}
	// Run the ordinary adapter over an empty source first, preserving validation,
	// computed revision checks, diagnostics and the complete result envelope.
	validation := d
	validation.SourceSQL = (sqlDataset{}).v2Source(d.Schema) + " WHERE 0"
	validation.SourceArgs = nil
	result, err := validation.ExecuteV2In(ctx, tx, nil, &q, o)
	if err != nil {
		return result, true, err
	}
	source := table.project([]string{d.Schema.IDField}).v2Source(d.Schema)
	statement := "SELECT COUNT(*) FROM (SELECT 1 FROM (" + source + ") AS r LIMIT 10000001)"
	total, err := s.Catalog.queryCount(ctx, tx, statement, nil, s.Catalog.freshToolCubeCount)
	if err != nil {
		return result, true, err
	}
	if total > int64(d.MaxPopulation) {
		return result, true, fmt.Errorf("resource_limit: matching population exceeds host budget")
	}
	for i := range result.Metrics.Metrics {
		metric := &result.Metrics.Metrics[i]
		metric.ProcessedRows = total
		metric.Buckets[0].Count = total
		metric.Buckets[0].Value = float64(total)
	}
	return result, true, nil
}

// Only raw base-field predicates and indexable scalar ordering are pushed below
// the row window. Canonical computed definitions, typed selected values and the
// complete revision envelope are still checked by ExecuteV2In on that page.
func (s *Server) indexedQueryRowsV2(ctx context.Context, tx *sql.Tx, table sqlDataset, d qt.SQLiteV2Dataset, q qt.ServerQueryV2, o qt.PlanOptions, bounds map[string][2]string, population int) (qt.SQLiteV2ExecutionResult, bool, error) {
	for _, term := range q.Where {
		for _, clause := range term.Predicates() {
			if _, base := d.Schema.Fields[clause.Field]; !base {
				return qt.SQLiteV2ExecutionResult{}, false, nil
			}
		}
	}
	order := slices.Clone([]qt.OrderBy(q.OrderBy))
	if len(order) == 0 {
		order = slices.Clone(d.Schema.DefaultSort)
	}
	order = append(order, d.Schema.TiebreakSort...)
	order = append(order, qt.OrderBy{Field: d.Schema.IDField, Dir: "asc"})
	terms := make([]string, 0, len(order))
	for _, term := range order {
		field, base := d.Schema.Fields[term.Field]
		if !base || term.Extract != nil || field.SQLiteSortField != "" || field.SortExpr != "" && field.SortExpr != field.Expr {
			return qt.SQLiteV2ExecutionResult{}, false, nil
		}
		expression := field.Expr
		switch {
		case term.Field == d.Schema.IDField && field.Kind == qt.FieldText:
		case field.Kind == qt.FieldDatetime && field.SQLiteDatetimeFormat == "utc-millis":
		case field.Kind == qt.FieldNumber:
			expression = "CASE WHEN qt_v2_field_error(" + expression + ",'number') IS NULL THEN " + expression + " END"
		default:
			return qt.SQLiteV2ExecutionResult{}, false, nil
		}
		// The compiler below validates direction and null placement; SQL tokens
		// here are selected from constants, never copied from request text.
		dir, nulls := "ASC", "LAST"
		if term.Dir == "desc" {
			dir = "DESC"
		}
		if term.Nulls == "first" {
			nulls = "FIRST"
		}
		terms = append(terms, expression+" COLLATE BINARY "+dir+" NULLS "+nulls)
	}
	o.SourceSQL, o.SourceArgs = d.SourceSQL, d.SourceArgs
	o.ComputedGroupable = d.ComputedGroupable
	o.Resolver = qt.SQLiteComputedDefinitionResolver{Tx: tx, Scope: d.Scope, Dataset: d.Dataset}
	if !slices.Contains(q.Select, d.Schema.IDField) {
		q.Select = append(slices.Clone(q.Select), d.Schema.IDField)
	}
	if _, err := qt.CompileSQLiteRowsV2(ctx, q, d.Schema, o); err != nil {
		return qt.SQLiteV2ExecutionResult{}, true, err
	}
	residual := table.residualWhere(q.Where)
	filter, err := qt.CompileSQLiteWhere(residual, d.Schema, qt.SQLiteOptions{Now: o.Now})
	if err != nil {
		return qt.SQLiteV2ExecutionResult{}, true, err
	}
	countFields := append(whereFields(residual), d.Schema.IDField)
	countTable, _ := table.project(countFields).preferCoveringScan(q.Where)
	countSource, countArgs := countTable.v2WindowedSource(d.Schema, q.Where, o.Now, bounds)
	countPredicate := ""
	if filter.SQL != "" {
		countPredicate = " WHERE " + numberQueryParameters(filter.SQL, len(countArgs))
	}
	countArgs = append(countArgs, filter.Args...)
	countSQL := "SELECT COUNT(*) FROM (SELECT 1 FROM (" + countSource + ") AS r" + countPredicate + fmt.Sprintf(" LIMIT %d)", population+1)
	var fast []func(context.Context, *sql.Tx) (int64, bool, error)
	if d.Dataset == "pharos_tool_calls" {
		if len(q.Where) == 0 && len(countArgs) == 0 {
			fast = append(fast, s.Catalog.freshToolCubeCount)
		} else {
			fast = append(fast, func(ctx context.Context, tx *sql.Tx) (int64, bool, error) {
				return s.Catalog.freshToolCubeRangeCount(ctx, tx, q.Where)
			})
			schema, err := s.queryTableSchema("tool_calls")
			if err != nil {
				return qt.SQLiteV2ExecutionResult{}, true, err
			}
			fast = append(fast, func(ctx context.Context, tx *sql.Tx) (int64, bool, error) {
				return s.Catalog.freshMachineCubeCount(ctx, tx, q.Where, schema)
			})
			fast = append(fast, func(ctx context.Context, tx *sql.Tx) (int64, bool, error) {
				return s.Catalog.freshToolDimensionCount(ctx, tx, q.Where, d.Schema)
			})
			fast = append(fast, func(ctx context.Context, tx *sql.Tx) (int64, bool, error) {
				return s.Catalog.freshToolTextCount(ctx, tx, table, q.Where, d.Schema, o.Now)
			})
		}
	}
	total, err := s.Catalog.queryCount(ctx, tx, countSQL, countArgs, fast...)
	if err != nil {
		return qt.SQLiteV2ExecutionResult{}, true, err
	}
	if total > int64(population) {
		return qt.SQLiteV2ExecutionResult{}, true, fmt.Errorf("resource_limit: matching population exceeds host budget")
	}
	if int64(q.Offset) >= total {
		// No source row can reach this page. Still let the adapter validate
		// computed projections/revisions and construct its normal envelope.
		d.SourceSQL = (sqlDataset{}).v2Source(d.Schema) + " WHERE 0"
		d.SourceArgs = nil
		copy := q
		copy.Where, copy.Offset = nil, 0
		result, err := d.ExecuteV2In(ctx, tx, &copy, nil, qt.PlanOptions{Identity: o.Identity, Now: o.Now})
		if err == nil {
			result.Rows.Total = total
		}
		return result, true, err
	}
	predicate := ""
	if filter.SQL != "" {
		predicate = " WHERE " + numberQueryParameters(filter.SQL, len(d.SourceArgs))
	}
	args := append(slices.Clone(d.SourceArgs), filter.Args...)
	// A join-driven plan can ignore ORDER BY's index and sort the full ledger
	// before LIMIT, particularly with explicit NULL placement on the ID ties.
	// Prefer the host-owned paging index except for selective identity lookups.
	lookup := false
	for _, term := range q.Where {
		if !term.IsGroup() && !term.Negated && term.Op == "=" && term.Value != "" && table.lookups[term.Field] {
			lookup = true
		}
		if table.calendarFacts && term.Field == "day" && !term.Negated && term.Op == "is_null" {
			lookup = true
		}
	}
	fullSource := d.SourceSQL
	if !lookup && len(order) > 0 {
		index := table.pageIndexes[order[0].Field]
		if index != "" {
			d.SourceSQL = strings.Replace(d.SourceSQL, table.pageFrom, table.pageFrom+" INDEXED BY "+sqlIdentifier(index), 1)
		}
	}
	d.SourceSQL = "SELECT * FROM (" + d.SourceSQL + ") AS r" + predicate + " ORDER BY " + strings.Join(terms, ",") + fmt.Sprintf(" LIMIT ?%d OFFSET ?%d", len(args)+1, len(args)+2)
	if d.Dataset == "pharos_writing_messages" {
		// Sort and page narrow identities before fetching message bodies. SQLite
		// otherwise puts every selected long message into its numeric-sort B-tree.
		earlyFields := append(whereFields(residual), d.Schema.IDField)
		for _, term := range order {
			earlyFields = append(earlyFields, term.Field)
		}
		early, earlyArgs := table.project(earlyFields).v2WindowedSource(d.Schema, q.Where, o.Now, bounds)
		if slices.EqualFunc(earlyArgs, d.SourceArgs, reflect.DeepEqual) {
			if !lookup && len(order) > 0 {
				if index := table.pageIndexes[order[0].Field]; index != "" {
					early = strings.Replace(early, table.pageFrom, table.pageFrom+" INDEXED BY "+sqlIdentifier(index), 1)
				}
			}
			page := "SELECT r." + sqlIdentifier(d.Schema.IDField) + " FROM (" + early + ") AS r" + predicate + " ORDER BY " + strings.Join(terms, ",") + fmt.Sprintf(" LIMIT ?%d OFFSET ?%d", len(args)+1, len(args)+2)
			d.SourceSQL = "SELECT * FROM (" + fullSource + ") AS r WHERE r." + sqlIdentifier(d.Schema.IDField) + " IN (" + page + ")"
		}
	}
	d.SourceArgs = append(args, q.Limit, q.Offset)
	if d.Dataset == "pharos_writing_messages" && slices.Contains(q.Select, "text") {
		computed := false
		for _, name := range q.Select {
			computed = computed || strings.HasPrefix(name, "@computed/")
		}
		if !computed {
			// The authorship reader bounds displayed text after execution.
			// Full text still participates in the source WHERE above, while a
			// long selected message cannot fail the expression text-size budget.
			names := make([]string, 0, len(d.Schema.Fields))
			for name := range d.Schema.Fields {
				names = append(names, name)
			}
			slices.Sort(names)
			projections := make([]string, 0, len(names))
			for _, name := range names {
				value := "r." + sqlIdentifier(name)
				if name == "text" {
					// One extra rune preserves the reader's truncation indicator;
					// even four-byte runes remain below the expression text limit.
					value = fmt.Sprintf("substr(%s,1,%d)", value, authoredTextLimit+1)
				}
				projections = append(projections, value+" AS "+sqlIdentifier(name))
			}
			d.SourceSQL = "SELECT " + strings.Join(projections, ",") + " FROM (" + d.SourceSQL + ") AS r"
		}
	}
	copy := q
	copy.Where, copy.Offset = nil, 0
	result, err := d.ExecuteV2In(ctx, tx, &copy, nil, qt.PlanOptions{Identity: o.Identity, Now: o.Now})
	if err == nil {
		result.Rows.Total = total
	}
	return result, true, err
}

// Only compiler-owned anonymous parameters are numbered. Quoted text and
// identifiers may contain literal question marks and must be preserved.
func numberQueryParameters(sql string, start int) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if quote != 0 {
			out.WriteByte(c)
			if c == quote {
				if i+1 < len(sql) && sql[i+1] == quote {
					i++
					out.WriteByte(quote)
				} else {
					quote = 0
				}
			}
		} else if c == '\'' || c == '"' || c == '`' {
			quote = c
			out.WriteByte(c)
		} else if c == '?' {
			start++
			fmt.Fprintf(&out, "?%d", start)
		} else {
			out.WriteByte(c)
		}
	}
	return out.String()
}
