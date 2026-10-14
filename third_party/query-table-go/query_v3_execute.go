package querytable

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

func queryTableName() string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return "qt_v3_" + hex.EncodeToString(bytes[:])
}
func queryIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
func (d QueryDatasetV3) param(n int) string {
	if d.Dialect == "sqlite" {
		return "?" + strconv.Itoa(n)
	}
	return "$" + strconv.Itoa(n)
}
func (d QueryDatasetV3) valueType(typ string) string {
	if d.Dialect == "sqlite" {
		if typ == "number" {
			return "REAL"
		}
		if typ == "bool" {
			return "INTEGER"
		}
		return "TEXT"
	}
	switch typ {
	case "number":
		return "double precision"
	case "bool":
		return "boolean"
	case "datetime":
		return "timestamptz"
	default:
		return "text"
	}
}

// Execute owns one snapshot and always rolls back on failure. A retained host
// snapshot must use ExecuteIn and the caller's authorized transaction instead.
func (d QueryDatasetV3) Execute(ctx context.Context, db *sql.DB, q QueryRequestV3, o PlanOptions) (QueryResultV3, error) {
	if db == nil {
		return QueryResultV3{}, fmt.Errorf("nil query database")
	}
	if q.Snapshot != "" {
		return QueryResultV3{}, diagnostic("snapshot_transaction_required", "snapshot requests require caller-owned ExecuteIn")
	}
	level := sql.LevelDefault
	if d.Dialect == "postgres" {
		level = sql.LevelRepeatableRead
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: level})
	if err != nil {
		return QueryResultV3{}, err
	}
	defer tx.Rollback()
	out, err := d.ExecuteIn(ctx, tx, q, o)
	if err != nil {
		return QueryResultV3{}, err
	}
	if err = tx.Commit(); err != nil {
		return QueryResultV3{}, err
	}
	return out, nil
}

// ExecuteIn stages a narrow complete source inside the authorized transaction,
// reduces groups with the existing typed SQL compiler, then executes HAVING,
// sorting, paging and metrics over that projection. All temporary tables are
// dropped before success; cancellation/budget failures never return a page.
func (d QueryDatasetV3) ExecuteIn(ctx context.Context, tx *sql.Tx, q QueryRequestV3, o PlanOptions) (QueryResultV3, error) {
	out := QueryResultV3{Version: 3, Now: q.Now, Rows: []map[string]any{}, Metrics: []SQLiteMetricV2{}}
	if err := q.Validate(); err != nil {
		return out, err
	}
	maxRows, maxGroups, maxPop, maxBytes, err := d.limits()
	if err != nil {
		return out, err
	}
	if _, err = d.sampleLimit(); err != nil {
		return out, err
	}
	if q.Result.Limit < 1 || q.Result.Limit > maxRows || len(q.Metrics) > 20 {
		return out, diagnostic("resource_limit", "invalid result window or metric budget")
	}
	o, err = d.options(ctx, tx, q, o)
	if err != nil {
		return out, err
	}
	plan, err := d.SourcePlan(ctx, q, o)
	if err != nil {
		return out, err
	}
	// Inspect the bounded narrow projection in the same authorized snapshot
	// before copying it. An oversized source must never fill our temp table.
	sizeSQL := d.stagingSizeSQL(plan.Columns)
	var count, bytes int64
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*),"+sizeSQL+" FROM ("+plan.SQL+") AS qt_source_budget", plan.Args...).Scan(&count, &bytes); err != nil {
		return out, err
	}
	if count > int64(maxPop) {
		return out, diagnostic("resource_limit", "matching population exceeds host budget")
	}
	if bytes > maxBytes {
		return out, diagnostic("resource_limit", "source staging exceeds host byte budget")
	}
	tables := []string{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		for i := len(tables) - 1; i >= 0; i-- {
			_, _ = tx.ExecContext(cleanup, "DROP TABLE IF EXISTS "+queryIdent(tables[i]))
		}
	}()
	source := queryTableName()
	tables = append(tables, source)
	if _, err = tx.ExecContext(ctx, "CREATE TEMP TABLE "+queryIdent(source)+" AS "+plan.SQL, plan.Args...); err != nil {
		return out, err
	}
	// Recheck the materialized values too, including host expressions that may
	// be volatile. The preflight is what prevents normal budget failures staging.
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*),"+sizeSQL+" FROM "+queryIdent(source)).Scan(&count, &bytes); err != nil {
		return out, err
	}
	if count > int64(maxPop) {
		return out, diagnostic("resource_limit", "matching population exceeds host budget")
	}
	if bytes > maxBytes {
		return out, diagnostic("resource_limit", "source staging exceeds host byte budget")
	}
	out.SourceTotal = count
	staged := Schema{Name: d.Schema.Name, IDField: d.Schema.IDField, Fields: map[string]FieldSpec{}}
	for _, column := range plan.Columns {
		original, ok := d.Schema.Fields[column.Field]
		if !ok {
			original = resultField(column.Field, column.Type, "", d.ComputedGroupable[strings.TrimPrefix(column.Field, "@computed/")])
			original.AggregateOps = plan.ComputedPolicies[column.Field]
			original.ServerFilter = d.ComputedFilterable[strings.TrimPrefix(column.Field, "@computed/")]
		}
		original.Expr = "r." + queryIdent(column.ValueAlias)
		original.SortExpr = ""
		original.ValueErrorExpr = "r." + queryIdent(column.ErrorAlias)
		if original.Kind == FieldDatetime {
			original.SQLiteDatetimeFormat = "rfc3339"
		}
		staged.Fields[column.Field] = original
	}
	if q.Grouping == nil {
		staged.DefaultSort = d.Schema.DefaultSort
		staged.TiebreakSort = d.Schema.TiebreakSort
	}
	for name, field := range staged.Fields {
		if field.SQLiteSortField != "" {
			field.SortExpr = staged.Fields[field.SQLiteSortField].Expr
			staged.Fields[name] = field
		}
	}
	// Invalid base storage cannot masquerade as a complete source. Canonical
	// computed runtime errors remain sidecars and propagate through reductions.
	baseErrors := []string{}
	for _, column := range plan.Columns {
		if !strings.HasPrefix(column.Field, "@computed/") {
			baseErrors = append(baseErrors, queryIdent(column.ErrorAlias)+" IS NOT NULL")
		}
	}
	if len(baseErrors) > 0 {
		var invalid int64
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+queryIdent(source)+" WHERE "+strings.Join(baseErrors, " OR ")).Scan(&invalid); err != nil {
			return out, err
		}
		if invalid > 0 {
			return out, diagnostic("invalid_source_value", "source contains invalid typed values")
		}
	}
	stageOptions := o
	stageOptions.SourceSQL = "SELECT * FROM " + queryIdent(source)
	stageOptions.SourceArgs = nil
	stageOptions.ComputedFilterable = d.ComputedFilterable
	result := staged
	if q.Grouping != nil {
		groups := queryTableName()
		tables = append(tables, groups)
		result, err = d.stageGroups(ctx, tx, groups, q.Grouping, staged, stageOptions, maxGroups)
		if err != nil {
			return out, err
		}
		stageOptions.SourceSQL = "SELECT * FROM " + queryIdent(groups)
	}
	resultQuery := q.Result
	filtered, err := compileComputedRowsDialect(ctx, WireQuery{Select: []string{result.IDField}, Where: resultQuery.Where}, result, stageOptions, d.Dialect == "sqlite")
	if err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+filtered.SQL+") AS qt_count", filtered.Args...).Scan(&out.Total); err != nil {
		return out, err
	}
	names := []string{}
	for name := range result.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	out.Schema = ProjectedSchema{Name: result.Name, IDField: result.IDField, Fields: []ProjectedField{}}
	for _, name := range names {
		out.Schema.Fields = append(out.Schema.Fields, ProjectedField{name, schemaFieldType(result.Fields[name].Kind)})
	}
	out.Execution = SQLiteComputedExecution{Profile: q.Profile, PlanToken: q.PlanToken, Snapshot: q.Snapshot, ResolvedRevisions: plan.ResolvedRevisions, Fields: map[string]SQLiteComputedFieldCapability{}}
	for _, column := range plan.Columns {
		if strings.HasPrefix(column.Field, "@computed/") {
			out.Execution.Fields[column.Field] = plan.ComputedCapabilities[column.Field]
		}
	}
	if q.Rows {
		selected := append([]string{}, resultQuery.Select...)
		if len(selected) == 0 {
			selected = names
		}
		if !containsString(selected, result.IDField) {
			selected = append(selected, result.IDField)
		}
		resultQuery.Select = selected
		rowsPlan, e := compileComputedRowsDialect(ctx, resultQuery, result, stageOptions, d.Dialect == "sqlite")
		if e != nil {
			return out, e
		}
		out.Rows, err = readQueryRows(ctx, tx, rowsPlan, q.Grouping != nil, d.Dialect)
		if err != nil {
			return out, err
		}
	}
	metricQuery := MetricQuery{Version: 2, Where: resultQuery.Where, OrderBy: []OrderBy(resultQuery.OrderBy), Limit: resultQuery.Limit, Offset: resultQuery.Offset}
	ids := map[string]bool{}
	for _, spec := range q.Metrics {
		if err = (WireQuery{Aggregations: []AggSpec{spec}, Limit: q.Result.Limit, Offset: q.Result.Offset, Where: q.Result.Where, OrderBy: q.Result.OrderBy}).Validate(); err != nil {
			return out, err
		}
		if spec.Scope != "" && spec.Scope != "allMatching" && spec.Scope != "shownRows" {
			return out, diagnostic("scope", "invalid metric scope")
		}
		if ids[spec.ID] {
			return out, diagnostic("duplicate_metric", "duplicate metric ID")
		}
		ids[spec.ID] = true
		metric, e := d.executeMetric(ctx, tx, spec, metricQuery, result, stageOptions, out.Total, maxGroups, &tables)
		if e != nil {
			return out, e
		}
		out.Metrics = append(out.Metrics, metric)
	}
	return out, ctx.Err()
}
func containsString(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
func schemaFieldType(kind FieldKind) string {
	if kind == FieldEnum {
		return "enum"
	}
	if kind == FieldTextArray {
		return "textarray"
	}
	return expressionType(kind)
}
func typedQueryValue(value any, typ string) any {
	if bytes, ok := value.([]byte); ok {
		value = string(bytes)
	}
	if date, ok := value.(time.Time); ok {
		return date.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if typ == "datetime" && value != nil {
		if date, err := time.Parse(time.RFC3339Nano, fmt.Sprint(value)); err == nil {
			return date.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	return sqliteV2TypedValue(value, typ)
}
func readQueryRows(ctx context.Context, tx *sql.Tx, p SQLPlan, grouped bool, dialect string) ([]map[string]any, error) {
	out := []map[string]any{}
	if dialect == "postgres" {
		projections := []string{}
		arrays := false
		for _, column := range p.Columns {
			value := "qt_rows." + queryIdent(column.ValueAlias)
			if column.Type == "textarray" {
				arrays = true
				value = "array_to_json(" + value + ")::text"
			}
			projections = append(projections, value+" AS "+queryIdent(column.ValueAlias), "qt_rows."+queryIdent(column.ErrorAlias))
		}
		if arrays {
			p.SQL = "SELECT " + strings.Join(projections, ",") + " FROM (" + p.SQL + ") AS qt_rows"
		}
	}
	rows, err := tx.QueryContext(ctx, p.SQL, p.Args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		values, e := sqliteScanRows(rows)
		if e != nil {
			return out, e
		}
		row := map[string]any{}
		for i, column := range p.Columns {
			value := typedQueryValue(values[2*i], column.Type)
			if column.Type == "textarray" {
				var e error
				value, e = sqliteOutput(value, FieldSpec{Kind: FieldTextArray})
				if e != nil {
					return out, e
				}
			}
			code := sqliteErrorCode(values[2*i+1])
			if code != "" {
				if !grouped && !strings.HasPrefix(column.Field, "@computed/") {
					return out, diagnostic(code, "invalid source value for "+column.Field)
				}
				value = map[string]any{"computedError": code}
			}
			row[column.Field] = value
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (d QueryDatasetV3) stageGroups(ctx context.Context, tx *sql.Tx, table string, g *GroupingDefinition, s Schema, o PlanOptions, maxGroups int) (Schema, error) {
	result := Schema{Name: s.Name + ":groups", IDField: GroupIdentityField, Fields: map[string]FieldSpec{}}
	countPlan, err := compileMetricDialect(ctx, MetricQuery{Version: 2}, AggSpec{ID: "__qt_count__", Op: "count", GroupBy: g.Keys}, s, o, d.Dialect == "sqlite")
	if err != nil {
		return result, err
	}
	// A private ordinal lets identity batches join on one indexed integer, even
	// when the public grouping keys contain NULLs or have different SQL types.
	if _, err = tx.ExecContext(ctx, "CREATE TEMP TABLE "+queryIdent(table)+" AS SELECT ROW_NUMBER() OVER () AS qt_group_ordinal,qt_groups.* FROM ("+countPlan.SQL+") AS qt_groups", countPlan.Args...); err != nil {
		return result, err
	}
	var count int64
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+queryIdent(table)).Scan(&count); err != nil {
		return result, err
	}
	if count > int64(maxGroups) {
		return result, diagnostic("resource_limit", "group population exceeds host budget")
	}
	if _, err = tx.ExecContext(ctx, "ALTER TABLE "+queryIdent(table)+" ADD COLUMN identity TEXT"); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "CREATE UNIQUE INDEX "+queryIdent(table+"_ordinal")+" ON "+queryIdent(table)+" (qt_group_ordinal)"); err != nil {
		return result, err
	}
	result.Fields[GroupIdentityField] = resultField(GroupIdentityField, "text", "r.identity", false)
	for _, summary := range g.Summaries {
		if sampledExpression(MetricExpression(summarySpec(summary, g.Keys))) {
			limit, _ := d.sampleLimit()
			var excessive int64
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+queryIdent(table)+" WHERE count>"+d.param(1), limit).Scan(&excessive); err != nil {
				return result, err
			}
			if excessive > 0 {
				return result, diagnostic("resource_limit", "exact summary sample budget exceeded")
			}
			break
		}
	}
	for i, key := range g.Keys {
		original := s.Fields[key]
		original.Expr = fmt.Sprintf("r.group%d", i)
		original.ValueErrorExpr = fmt.Sprintf("r.group%d_error", i)
		original.SortExpr = ""
		original.SQLiteSortField = ""
		result.Fields[key] = original
	}
	for i, summary := range g.Summaries {
		typ := ""
		var summaryPlan SQLPlan
		if summary.Op == "row" {
			typ = expressionType(s.Fields[summary.Field].Kind)
		} else {
			summaryPlan, err = compileMetricDialect(ctx, MetricQuery{Version: 2}, summarySpec(summary, g.Keys), s, o, d.Dialect == "sqlite")
			if err != nil {
				return result, err
			}
			typ = summaryPlan.Columns[len(summaryPlan.Columns)-1].Type
		}
		value, errorName := fmt.Sprintf("summary%d", i), fmt.Sprintf("summary%d_error", i)
		for _, definition := range []string{queryIdent(value) + " " + d.valueType(typ), queryIdent(errorName) + " TEXT"} {
			if _, err = tx.ExecContext(ctx, "ALTER TABLE "+queryIdent(table)+" ADD COLUMN "+definition); err != nil {
				return result, err
			}
		}
		groupable := typ == "text" || typ == "bool"
		if summary.Op == "row" || summary.Op == "min" || summary.Op == "max" {
			groupable = fieldGroupable(s.Fields[summary.Field])
		}
		field := resultField(summary.ID, typ, "r."+queryIdent(value), groupable)
		if (summary.Op == "row" || summary.Op == "min" || summary.Op == "max") && s.Fields[summary.Field].Kind == FieldEnum {
			field.Kind = FieldEnum
		}
		field.ValueErrorExpr = "r." + queryIdent(errorName)
		result.Fields[summary.ID] = field
		if summary.Op == "row" {
			continue
		}
		join := groupJoin("target", "summary", len(g.Keys))
		update := "UPDATE " + queryIdent(table) + " AS target SET " + queryIdent(value) + "=summary.value," + queryIdent(errorName) + "=summary.error FROM (" + summaryPlan.SQL + ") AS summary WHERE " + join
		if _, err = tx.ExecContext(ctx, update, summaryPlan.Args...); err != nil {
			return result, err
		}
	}
	if g.Representative != nil && hasRowSummary(g.Summaries) {
		representative, e := representativePlan(ctx, g, s, o, d.Dialect == "sqlite")
		if e != nil {
			return result, e
		}
		var invalid int64
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+representative.SQL+") AS qt_representatives WHERE qt_identity_count>1 OR qt_missing_identity>0", representative.Args...).Scan(&invalid); err != nil {
			return result, err
		}
		if invalid > 0 {
			return result, diagnostic("duplicate_identity", "representative rows require unique nonnull source IDs within every group")
		}
		assignments := []string{}
		for i, column := range g.Summaries {
			if column.Op == "row" {
				value := fmt.Sprintf("summary%d", i)
				assignments = append(assignments, queryIdent(value)+"=summary."+queryIdent(value), queryIdent(value+"_error")+"=summary."+queryIdent(value+"_error"))
			}
		}
		update := "UPDATE " + queryIdent(table) + " AS target SET " + strings.Join(assignments, ",") + " FROM (" + representative.SQL + ") AS summary WHERE " + groupJoin("target", "summary", len(g.Keys))
		if _, err = tx.ExecContext(ctx, update, representative.Args...); err != nil {
			return result, err
		}
	}
	if err = d.assignGroupIdentities(ctx, tx, table, g.Keys, result); err != nil {
		return result, err
	}
	return result, nil
}
func groupJoin(left, right string, n int) string {
	parts := []string{}
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("%s.group%d IS NOT DISTINCT FROM %s.group%d", left, i, right, i))
		parts = append(parts, fmt.Sprintf("%s.group%d_error IS NOT DISTINCT FROM %s.group%d_error", left, i, right, i))
	}
	return strings.Join(parts, " AND ")
}
func hasRowSummary(summaries []SummaryColumn) bool {
	for _, s := range summaries {
		if s.Op == "row" {
			return true
		}
	}
	return false
}
func representativePlan(ctx context.Context, g *GroupingDefinition, s Schema, o PlanOptions, sqlite bool) (SQLPlan, error) {
	b, err := newPlanDialect(ctx, s, o, sqlite)
	if err != nil {
		return SQLPlan{}, err
	}
	groupValues := []ValueSQL{}
	for _, key := range g.Keys {
		value, e := b.field(key)
		if e != nil {
			return SQLPlan{}, e
		}
		groupValues = append(groupValues, value)
	}
	terms := append([]OrderBy{}, g.Representative.OrderBy...)
	explicitID := false
	for _, term := range terms {
		explicitID = explicitID || term.Field == s.IDField
	}
	if !explicitID {
		terms = append(terms, OrderBy{Field: s.IDField, Dir: "desc", Nulls: "last"})
	}
	b.schema.DefaultSort = nil
	b.schema.TiebreakSort = nil
	order, err := b.order(terms)
	if err != nil {
		return SQLPlan{}, err
	}
	projections := []string{}
	partitions := []string{}
	for i, v := range groupValues {
		projections = append(projections, fmt.Sprintf("%s AS group%d,%s AS group%d_error", v.Value, i, v.Error, i))
		partitions = append(partitions, v.Value, v.Error)
	}
	for i, summary := range g.Summaries {
		if summary.Op == "row" {
			v, e := b.field(summary.Field)
			if e != nil {
				return SQLPlan{}, e
			}
			projections = append(projections, fmt.Sprintf("%s AS summary%d,%s AS summary%d_error", v.Value, i, v.Error, i))
		}
	}
	// Identity validation includes duplicates within each typed key tuple. It is
	// independent of representative sort ties and of which ROW fields are shown.
	id, err := b.field(s.IDField)
	if err != nil {
		return SQLPlan{}, err
	}
	b.stage("SELECT *,ROW_NUMBER() OVER (PARTITION BY " + strings.Join(partitions, ",") + " ORDER BY " + order + ") AS qt_rank,COUNT(*) OVER (PARTITION BY " + strings.Join(append(append([]string{}, partitions...), id.Value), ",") + ") AS qt_same_identity,SUM(CASE WHEN " + id.Value + " IS NULL THEN 1 ELSE 0 END) OVER (PARTITION BY " + strings.Join(partitions, ",") + ") AS qt_missing_identity FROM " + b.rel)
	b.stage("SELECT *,MAX(qt_same_identity) OVER (PARTITION BY " + strings.Join(partitions, ",") + ") AS qt_identity_count FROM " + b.rel)
	sql := "SELECT " + strings.Join(projections, ",") + ",qt_identity_count,qt_missing_identity," + id.Value + " AS qt_source_id FROM " + b.rel + " WHERE qt_rank=1"
	return b.finish(sql, "allMatching", nil), nil
}
func (d QueryDatasetV3) assignGroupIdentities(ctx context.Context, tx *sql.Tx, table string, keys []string, s Schema) error {
	names := []string{"qt_group_ordinal"}
	ordered := make([]int, len(keys))
	for i := range keys {
		names = append(names, fmt.Sprintf("group%d", i), fmt.Sprintf("group%d_error", i))
		ordered[i] = i
	}
	sort.Slice(ordered, func(i, j int) bool { return groupNameLess(keys[ordered[i]], keys[ordered[j]]) })
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(names, ",")+" FROM "+queryIdent(table))
	if err != nil {
		return err
	}
	type identity struct {
		id      string
		ordinal int64
	}
	ids := []identity{}
	for rows.Next() {
		var ordinal int64
		raw := make([]any, len(keys))
		errors := make([]any, len(keys))
		dest := []any{&ordinal}
		for i := range raw {
			dest = append(dest, &raw[i], &errors[i])
		}
		if e := rows.Scan(dest...); e != nil {
			rows.Close()
			return e
		}
		tuple := [][]any{}
		for _, i := range ordered {
			key := keys[i]
			v := typedQueryValue(raw[i], expressionType(s.Fields[key].Kind))
			if code := sqliteErrorCode(errors[i]); code != "" {
				v = map[string]any{"computedError": code}
			}
			if utf16Length(key)+utf16Length(fmt.Sprint(v)) > 10000 {
				rows.Close()
				return diagnostic("resource_limit", "grouping key length budget exceeded")
			}
			tuple = append(tuple, []any{key, v})
		}
		code := jsonTuple(tuple)
		if utf16Length(code) > 65536 {
			rows.Close()
			return diagnostic("resource_limit", "grouping key length budget exceeded")
		}
		ids = append(ids, identity{code, ordinal})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Two parameters per group stay below SQLite's legacy 999-variable limit.
	// PostgreSQL needs explicit VALUES types so ordinals are not inferred as text.
	const batchSize = 400
	for start := 0; start < len(ids); start += batchSize {
		values := []string{}
		args := []any{}
		for _, id := range ids[start:min(start+batchSize, len(ids))] {
			values = append(values, "(CAST("+d.param(len(args)+1)+" AS bigint),CAST("+d.param(len(args)+2)+" AS text))")
			args = append(args, id.ordinal, id.id)
		}
		update := "WITH qt_identities(ordinal,identity) AS (VALUES " + strings.Join(values, ",") + ") UPDATE " + queryIdent(table) + " AS target SET identity=ids.identity FROM qt_identities AS ids WHERE target.qt_group_ordinal=ids.ordinal"
		if _, err = tx.ExecContext(ctx, update, args...); err != nil {
			return err
		}
	}
	return nil
}
func jsonTuple(tuple [][]any) string {
	var out strings.Builder
	out.WriteByte('[')
	for i, pair := range tuple {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteByte('[')
		out.WriteString(jsonString(pair[0].(string)))
		out.WriteByte(',')
		if text, ok := pair[1].(string); ok {
			out.WriteString(jsonString(text))
		} else {
			number := pair[1]
			if n, ok := number.(float64); ok && n == 0 {
				number = float64(0) // JSON.stringify canonicalizes negative zero.
			}
			value, _ := json.Marshal(number)
			out.Write(value)
		}
		out.WriteByte(']')
	}
	out.WriteByte(']')
	return out.String()
}

// JavaScript sorts group field names by UTF-16 code units before JSON.stringify.
// SQL value ordering uses its profile's comparison rules independently.
func groupNameLess(a, b string) bool {
	left, right := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}
func jsonString(text string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 32 {
				fmt.Fprintf(&out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}
