package querytable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// SQLiteDataset is trusted host configuration. FromSQL includes FROM/JOIN
// (e.g. "FROM runs r"); BaseWhereSQL is an always-applied authorization/scope
// predicate without WHERE, with anonymous ? parameters in BaseArgs. Never fill
// these strings from request data. IDField must identify rows uniquely across
// the complete join. The caller owns driver registration, pool configuration,
// authorization, cancellation deadlines and indexes.
type SQLiteDataset struct {
	Schema       Schema
	FromSQL      string
	BaseWhereSQL string
	BaseArgs     []any
	// Zero uses 10000 rows / 2000 groups. Exceeding a limit is an error; groups
	// are never silently truncated. Counts and metrics cover all matching rows.
	MaxLimit  int
	MaxGroups int
}

type SQLiteRowsResult struct {
	Rows  []map[string]any `json:"rows"`
	Total int64            `json:"total"`
}
type SQLiteDistinctResult struct {
	Values  []string `json:"values"`
	HasMore bool     `json:"hasMore"`
	HasNull bool     `json:"hasNull"`
}
type SQLiteAggregationRequest struct {
	Where        []WhereTerm `json:"where"`
	Aggregations []AggSpec   `json:"aggregations"`
	Diagnostics  []any       `json:"diagnostics,omitempty"`
}
type SQLiteBucket struct {
	Keys  []any `json:"keys"`
	Value any   `json:"value"`
	Count int64 `json:"count"`
}
type SQLiteMetric struct {
	ID      string         `json:"id"`
	Buckets []SQLiteBucket `json:"buckets"`
}
type SQLiteAggregationResult struct {
	Metrics []SQLiteMetric `json:"metrics"`
}
type SQLiteFieldStat struct {
	Distinct int64 `json:"distinct"`
	Min      any   `json:"min,omitempty"`
	Max      any   `json:"max,omitempty"`
}

func (d SQLiteDataset) validate() error {
	if err := validateSQLiteSchema(d.Schema); err != nil {
		return err
	}
	if strings.TrimSpace(d.FromSQL) == "" {
		return fmt.Errorf("SQLite dataset requires FromSQL")
	}
	if d.MaxLimit < 0 || d.MaxGroups < 0 || d.MaxGroups == int(^uint(0)>>1) {
		return fmt.Errorf("invalid SQLite dataset limit")
	}
	if d.BaseWhereSQL == "" && len(d.BaseArgs) > 0 {
		return fmt.Errorf("BaseArgs requires BaseWhereSQL")
	}
	return nil
}
func (d SQLiteDataset) scope(where string, args []any) (string, []any) {
	parts := []string{}
	if d.BaseWhereSQL != "" {
		parts = append(parts, "("+d.BaseWhereSQL+")")
	}
	if where != "" {
		parts = append(parts, "("+where+")")
	}
	all := append(append([]any{}, d.BaseArgs...), args...)
	if len(parts) == 0 {
		return "", all
	}
	return " WHERE " + strings.Join(parts, " AND "), all
}
func sqliteRead[T any](ctx context.Context, db *sql.DB, run func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	if db == nil {
		return zero, fmt.Errorf("nil SQLite database")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	result, err := run(tx)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

// Rows reads the count and page in one short transaction. For a shared snapshot
// across multiple adapter operations, begin a transaction and use RowsIn etc.
func (d SQLiteDataset) Rows(ctx context.Context, db *sql.DB, q WireQuery) (SQLiteRowsResult, error) {
	return sqliteRead(ctx, db, func(tx *sql.Tx) (SQLiteRowsResult, error) { return d.RowsIn(ctx, tx, q) })
}
func (d SQLiteDataset) RowsIn(ctx context.Context, tx *sql.Tx, q WireQuery) (SQLiteRowsResult, error) {
	if err := d.validate(); err != nil {
		return SQLiteRowsResult{}, err
	}
	if tx == nil {
		return SQLiteRowsResult{}, fmt.Errorf("nil SQLite transaction")
	}
	maxLimit := d.MaxLimit
	if maxLimit == 0 {
		maxLimit = 10000
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > maxLimit {
		return SQLiteRowsResult{}, fmt.Errorf("row limit must be 1..%d", maxLimit)
	}
	if len(q.Select) == 0 {
		for name := range d.Schema.Fields {
			q.Select = append(q.Select, name)
		}
		sort.Strings(q.Select)
	}
	found := false
	for _, name := range q.Select {
		if name == d.Schema.IDField {
			found = true
		}
	}
	if !found {
		q.Select = append(q.Select, d.Schema.IDField)
	}
	compiled, err := CompileSQLite(q, d.Schema, SQLiteOptions{Now: time.Now()})
	if err != nil {
		return SQLiteRowsResult{}, err
	}
	where, countArgs := d.scope(compiled.WhereSQL, compiled.WhereArgs)
	result := SQLiteRowsResult{Rows: []map[string]any{}}
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) "+d.FromSQL+where, countArgs...).Scan(&result.Total); err != nil {
		return result, err
	}
	args := append(append([]any{}, d.BaseArgs...), compiled.Args...)
	args = append(args, q.Limit, q.Offset)
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(compiled.SelectExprs, ",")+" "+d.FromSQL+where+" ORDER BY "+compiled.OrderSQL+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		values := make([]any, len(q.Select))
		targets := make([]any, len(values))
		for i := range targets {
			targets[i] = &values[i]
		}
		if err = rows.Scan(targets...); err != nil {
			return result, err
		}
		row := make(map[string]any, len(values))
		for i, name := range q.Select {
			value, e := sqliteOutput(values[i], d.Schema.Fields[name])
			if e != nil {
				return result, fmt.Errorf("field %q: %w", name, e)
			}
			row[name] = value
		}
		id := row[d.Schema.IDField]
		if id == nil || id == "" {
			return result, fmt.Errorf("SQLite row identity is NULL or empty")
		}
		keyBytes, e := json.Marshal(id)
		if e != nil {
			return result, e
		}
		key := string(keyBytes)
		if seen[key] {
			return result, fmt.Errorf("duplicate SQLite row identity %s", key)
		}
		seen[key] = true
		result.Rows = append(result.Rows, row)
	}
	return result, rows.Err()
}

func sqliteOutput(v any, f FieldSpec) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch f.Kind {
	case FieldNumber:
		var n float64
		switch value := v.(type) {
		case int64:
			n = float64(value)
		case float64:
			n = value
		default:
			return nil, fmt.Errorf("numeric binding has storage type %T", v)
		}
		if math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, fmt.Errorf("numeric_range")
		}
		if math.Trunc(n) == n && math.Abs(n) > sqliteSafeInteger {
			return nil, fmt.Errorf("unsafe_integer")
		}
		return v, nil
	case FieldBool:
		if n, ok := v.(int64); ok && (n == 0 || n == 1) {
			return n == 1, nil
		}
		return nil, fmt.Errorf("boolean binding must contain 0, 1 or NULL")
	case FieldTextArray:
		var a []string
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("textarray binding must be JSON text")
		}
		var raw []any
		if json.Unmarshal([]byte(s), &raw) != nil || raw == nil {
			return nil, fmt.Errorf("invalid JSON textarray")
		}
		for _, item := range raw {
			if _, ok := item.(string); !ok {
				return nil, fmt.Errorf("textarray must contain only strings")
			}
		}
		if err := json.Unmarshal([]byte(s), &a); err != nil {
			return nil, err
		}
		if a == nil {
			a = []string{}
		}
		return a, nil
	case FieldDatetime:
		var t time.Time
		switch f.SQLiteDatetimeFormat {
		case "unix-seconds", "unix-millis":
			scale := int64(1)
			if f.SQLiteDatetimeFormat == "unix-millis" {
				scale = 1000
			}
			var err error
			t, err = sqliteUnixTime(v, scale)
			if err != nil {
				return nil, err
			}

		default:
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("datetime binding requires text storage")
			}
			if s == "" {
				return nil, nil
			}
			var e error
			t, e = sqliteParseTime(s)
			if e != nil {
				return nil, e
			}
			if f.SQLiteDatetimeFormat == "utc-millis" && s != t.UTC().Format("2006-01-02T15:04:05.000Z") {
				return nil, fmt.Errorf("utc-millis binding is not canonical")
			}
		}
		return t.UTC().Format(time.RFC3339Nano), nil
	case FieldText, FieldEnum:
		if s, ok := v.(string); ok {
			return s, nil
		}
		return nil, fmt.Errorf("text binding has storage type %T", v)
	}
	return v, nil
}

func (d SQLiteDataset) Distinct(ctx context.Context, db *sql.DB, field, search string, limit int) (SQLiteDistinctResult, error) {
	return sqliteRead(ctx, db, func(tx *sql.Tx) (SQLiteDistinctResult, error) { return d.DistinctIn(ctx, tx, field, search, limit) })
}
func (d SQLiteDataset) DistinctIn(ctx context.Context, tx *sql.Tx, field, search string, limit int) (SQLiteDistinctResult, error) {
	result := SQLiteDistinctResult{Values: []string{}}
	if err := d.validate(); err != nil {
		return result, err
	}
	if tx == nil {
		return result, fmt.Errorf("nil SQLite transaction")
	}
	compiled, err := CompileSQLiteDistinct(field, search, d.Schema)
	if err != nil {
		return result, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return result, fmt.Errorf("distinct limit must be 1..200")
	}
	where, args := d.scope(compiled.IsNullExpr, nil)
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 "+d.FromSQL+where+")", args...).Scan(&result.HasNull); err != nil {
		return result, err
	}
	source := d.FromSQL + compiled.ArraySQL
	expr := compiled.Expr
	condition := expr + " IS NOT NULL"
	if compiled.SearchSQL != "" {
		condition += " AND " + compiled.SearchSQL
	}
	searchArgs := compiled.Args
	where, args = d.scope(condition, searchArgs)
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT CAST("+expr+" AS TEXT) COLLATE BINARY AS value "+source+where+" ORDER BY value COLLATE BINARY LIMIT ?", args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			return result, err
		}
		result.Values = append(result.Values, s)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Values) > limit {
		result.Values = result.Values[:limit]
		result.HasMore = true
	}
	return result, nil
}

func (d SQLiteDataset) Aggregations(ctx context.Context, db *sql.DB, request SQLiteAggregationRequest) (SQLiteAggregationResult, error) {
	return sqliteRead(ctx, db, func(tx *sql.Tx) (SQLiteAggregationResult, error) { return d.AggregationsIn(ctx, tx, request) })
}
func (d SQLiteDataset) AggregationsIn(ctx context.Context, tx *sql.Tx, request SQLiteAggregationRequest) (SQLiteAggregationResult, error) {
	result := SQLiteAggregationResult{Metrics: []SQLiteMetric{}}
	if len(request.Diagnostics) > 0 {
		return result, diagnostic("residual_query", "aggregation request contains unresolved diagnostics")
	}
	if err := d.validate(); err != nil {
		return result, err
	}
	if tx == nil {
		return result, fmt.Errorf("nil SQLite transaction")
	}
	if err := (WireQuery{Where: request.Where, Aggregations: request.Aggregations}).Validate(); err != nil {
		return result, err
	}
	plan, err := CompileSQLiteWhere(request.Where, d.Schema, SQLiteOptions{Now: time.Now()})
	if err != nil {
		return result, err
	}
	where, args := d.scope(plan.SQL, plan.Args)
	maxGroups := d.MaxGroups
	if maxGroups == 0 {
		maxGroups = 2000
	}
	// Validate the entire batch before executing any member.
	plans := make([]AggCompileResult, len(request.Aggregations))
	ids := map[string]bool{}
	for i, a := range request.Aggregations {
		if ids[a.ID] {
			return result, fmt.Errorf("duplicate aggregation id %q", a.ID)
		}
		ids[a.ID] = true
		plans[i], err = CompileSQLiteAggregation(a, d.Schema)
		if err != nil {
			return result, err
		}
	}
	for i, a := range request.Aggregations {
		p := plans[i]
		sqlText := "SELECT " + strings.Join(p.SelectExprs, ",") + " " + d.FromSQL + where
		if p.GroupBySQL != "" {
			sqlText += " GROUP BY " + p.GroupBySQL
		}
		sqlText += " ORDER BY \"value\" DESC NULLS LAST"
		for n := range a.GroupBy {
			sqlText += ", " + sqliteIdent(fmt.Sprintf("g%d", n)) + " ASC NULLS LAST"
		}
		sqlText += " LIMIT ?"
		callArgs := append(append([]any{}, args...), maxGroups+1)
		rows, e := tx.QueryContext(ctx, sqlText, callArgs...)
		if e != nil {
			return result, e
		}
		metric := SQLiteMetric{ID: a.ID, Buckets: []SQLiteBucket{}}
		for rows.Next() {
			values := make([]any, len(a.GroupBy)+2)
			dest := make([]any, len(values))
			for n := range dest {
				dest[n] = &values[n]
			}
			if e = rows.Scan(dest...); e != nil {
				break
			}
			keys := make([]any, len(a.GroupBy))
			for n, name := range a.GroupBy {
				keys[n], e = sqliteOutput(values[n], d.Schema.Fields[name])
				if e != nil {
					break
				}
			}
			if e != nil {
				break
			}
			value := values[len(a.GroupBy)]
			if a.Field != "" && a.Op != "count" && a.Op != "count_distinct" {
				value, e = sqliteOutput(value, d.Schema.Fields[a.Field])
				if e != nil {
					break
				}
			}
			count, ok := values[len(values)-1].(int64)
			if !ok {
				e = fmt.Errorf("invalid aggregation count")
				break
			}
			metric.Buckets = append(metric.Buckets, SQLiteBucket{Keys: keys, Value: value, Count: count})
			if len(metric.Buckets) > maxGroups {
				e = fmt.Errorf("aggregation %q exceeds %d groups", a.ID, maxGroups)
				break
			}
		}
		if e == nil {
			e = rows.Err()
		}
		closeErr := rows.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			return result, e
		}
		result.Metrics = append(result.Metrics, metric)
	}
	return result, nil
}

// FieldStats returns non-NULL distinct counts and numeric/datetime extrema in
// one transaction. Array fields have no scalar distinct statistic and are refused.
func (d SQLiteDataset) FieldStats(ctx context.Context, db *sql.DB, names []string) (map[string]SQLiteFieldStat, error) {
	return sqliteRead(ctx, db, func(tx *sql.Tx) (map[string]SQLiteFieldStat, error) { return d.FieldStatsIn(ctx, tx, names) })
}
func (d SQLiteDataset) FieldStatsIn(ctx context.Context, tx *sql.Tx, names []string) (map[string]SQLiteFieldStat, error) {
	result := map[string]SQLiteFieldStat{}
	if err := d.validate(); err != nil {
		return result, err
	}
	if tx == nil {
		return result, fmt.Errorf("nil SQLite transaction")
	}
	if len(names) > MaxSelectColumns {
		return result, fmt.Errorf("too many statistic fields")
	}
	var selectSQL []string
	for _, name := range names {
		f, ok := d.Schema.Fields[name]
		if !ok || f.Kind == FieldTextArray {
			return result, fmt.Errorf("unknown or nonscalar statistic field %q", name)
		}
		expr := sqliteGroupValue(f)
		selectSQL = append(selectSQL, "COUNT(DISTINCT "+expr+")")
		if f.Kind == FieldNumber || f.Kind == FieldDatetime {
			if f.Kind == FieldNumber {
				expr = "qt_number(" + expr + ")"
			}
			selectSQL = append(selectSQL, "MIN("+expr+")", "MAX("+expr+")")
		}
	}
	if len(names) == 0 {
		return result, nil
	}
	where, args := d.scope("", nil)
	values := make([]any, len(selectSQL))
	dest := make([]any, len(values))
	for i := range dest {
		dest[i] = &values[i]
	}
	if err := tx.QueryRowContext(ctx, "SELECT "+strings.Join(selectSQL, ",")+" "+d.FromSQL+where, args...).Scan(dest...); err != nil {
		return result, err
	}
	index := 0
	for _, name := range names {
		f := d.Schema.Fields[name]
		count, ok := values[index].(int64)
		if !ok {
			return result, fmt.Errorf("invalid distinct count")
		}
		stat := SQLiteFieldStat{Distinct: count}
		index++
		if f.Kind == FieldNumber || f.Kind == FieldDatetime {
			var err error
			stat.Min, err = sqliteOutput(values[index], f)
			if err != nil {
				return result, err
			}
			stat.Max, err = sqliteOutput(values[index+1], f)
			if err != nil {
				return result, err
			}
			index += 2
		}
		result[name] = stat
	}
	return result, nil
}
