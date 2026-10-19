package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// nativeMetrics lets tests turn the native path off, to compare its answers
// with the adapter's.
var nativeMetrics = true

// The adapter reduces each metric in its own pass over the source, through
// exact-arithmetic SQL functions, and fuses metrics only when they read the
// same field. For Human Words' message mix (nine sums over nine fields) and
// the Usage Token chart's sums that took several times the legacy route's
// single pass. The native path below answers plain counts and sums the way
// the legacy route did: one pass per grouping, natively in SQLite over a SQL
// dataset's source, or in Go over Usage's rows already in memory, so that
// they need no temporary-table copy. Cross-field metric fusion upstream in
// Query Table would make it unnecessary.
//
// It answers only what it computes exactly as the adapter does. In SQLite,
// every counted or summed value is a stored integer and each sum's absolute
// total stays within JavaScript's safe integers (beyond which the adapter
// reports an error). In memory, sums also take other numbers, added exactly
// as the adapter adds them (see exactSum), and text fields can be counted
// distinctly. Every group key is text. Anything else, or any metric beyond a
// plain count, distinct count or sum, falls through to the adapter, as does a
// request the adapter's compiler refuses. It validates the request with the adapter, as
// countOnlyMetricsV2 does, and keeps the dataset's population and group
// budgets.

// nativeMetricsV2 answers q natively over d's SQL source. With a table, d
// reads that SQL dataset: its population and value bounds come from c's
// per-generation cache (see nativeTableBounds). Without one, d's source is a
// copy the adapter staged, at most a population, and each statement checks
// its own values.
func (c *Catalog) nativeMetricsV2(ctx context.Context, tx *sql.Tx, d qt.SQLiteV2Dataset, q qt.MetricQuery, o qt.PlanOptions, table *sqlDataset) (qt.SQLiteV2ExecutionResult, bool, error) {
	for _, term := range q.Where {
		for _, clause := range term.Predicates() {
			if _, base := d.Schema.Fields[clause.Field]; !base {
				return qt.SQLiteV2ExecutionResult{}, false, nil
			}
		}
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	// The adapter's own row filter, as it applies it to count the population.
	filter, err := qt.CompileSQLiteWhere(q.Where, d.Schema, qt.SQLiteOptions{Now: now})
	if err != nil {
		return qt.SQLiteV2ExecutionResult{}, false, nil
	}
	predicate := ""
	if filter.SQL != "" {
		predicate = " WHERE " + numberQueryParameters(filter.SQL, len(d.SourceArgs))
	}
	args := append(append([]any{}, d.SourceArgs...), filter.Args...)
	return nativeAnswer(ctx, tx, d, q, o, func(keys []string, fields nativeFields, population int) ([]nativeGroup, int64, bool, error) {
		if len(fields.distinct) > 0 {
			return nil, 0, false, nil
		}
		if table != nil {
			count := fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM (%s) AS r%s LIMIT %d)", d.SourceSQL, predicate, population+1)
			matched, safe, err := c.nativeTableBounds(ctx, tx, *table, d.Schema, fields.names, population, count, args)
			if err != nil || !safe || matched > int64(population) {
				return nil, matched, err == nil && matched > int64(population), err
			}
		}
		return nativeGroups(ctx, tx, d.SourceSQL, predicate, args, keys, fields.names, fields.counted, table == nil)
	})
}

// nativeTableBounds checks a SQL dataset's request before it is read. It
// counts the rows matching it only when the whole source has more than a
// population, and reports whether every value of fields across the whole
// source is an integer and their absolute totals are safe integers: each
// request's rows are a subset, so its sums are safe whenever these are. Each
// answer is cached for the snapshot's data generation, as page counts are, so
// a request repeated between writes reads its rows once.
func (c *Catalog) nativeTableBounds(ctx context.Context, tx *sql.Tx, table sqlDataset, schema qt.Schema, fields []string, population int, count string, args []any) (int64, bool, error) {
	matched, err := c.queryCount(ctx, tx, "SELECT COUNT(*) FROM ("+table.project([]string{schema.IDField}).v2Source(schema)+")", nil)
	if err == nil && matched > int64(population) {
		matched, err = c.queryCount(ctx, tx, count, args)
	}
	if err != nil || matched > int64(population) || len(fields) == 0 {
		return matched, err == nil, err
	}
	totals := []string{}
	for _, field := range fields {
		value := "r." + sqlIdentifier(field)
		totals = append(totals, "CASE WHEN SUM(typeof("+value+") NOT IN ('integer','null'))>0 THEN 9007199254740992 ELSE COALESCE(SUM(ABS("+value+")),0) END")
	}
	largest := totals[0]
	if len(totals) > 1 {
		largest = "MAX(" + strings.Join(totals, ",") + ")"
	}
	total, err := c.queryCount(ctx, tx, "SELECT "+largest+" FROM ("+table.project(fields).v2Source(schema)+") AS r", nil)
	if err != nil {
		// SQLite refuses an integer overflow; the adapter reports it per value.
		return matched, false, ctx.Err()
	}
	return matched, total <= 1<<53-1, nil
}

// nativeRowMetricsV2 answers q, which must have no row filter left to apply,
// over rows held in memory: the rows the adapter would copy into its source.
func nativeRowMetricsV2(ctx context.Context, tx *sql.Tx, d qt.SQLiteV2Dataset, q qt.MetricQuery, o qt.PlanOptions, rows []map[string]any) (qt.SQLiteV2ExecutionResult, bool, error) {
	if len(q.Where) > 0 {
		return qt.SQLiteV2ExecutionResult{}, false, nil
	}
	if len(rows) > queryTablePopulation {
		// As stageQueryRows refuses to copy them.
		return qt.SQLiteV2ExecutionResult{}, true, fmt.Errorf("source population exceeds %d rows", queryTablePopulation)
	}
	return nativeAnswer(ctx, tx, d, q, o, func(keys []string, fields nativeFields, _ int) ([]nativeGroup, int64, bool, error) {
		groups, ok := nativeRowGroups(rows, keys, fields)
		if !ok {
			return nil, 0, false, nil
		}
		// A sum of other numbers reaches the page as the adapter's does.
		fractional := []float64{}
		for _, group := range groups {
			for _, value := range group.values {
				if n, ok := value.sum.(float64); ok {
					fractional = append(fractional, n)
				}
			}
		}
		if len(fractional) > 0 {
			numbers, err := adapterNumbers(ctx, tx, fractional)
			if err != nil {
				return nil, 0, false, err
			}
			for i := range groups {
				for j := range groups[i].values {
					if _, ok := groups[i].values[j].sum.(float64); ok {
						groups[i].values[j].sum, numbers = numbers[0], numbers[1:]
					}
				}
			}
		}
		return groups, int64(len(rows)), true, nil
	})
}

type nativeGroup struct {
	keys   []any
	rows   int64
	values []nativeValue // one per field
}

type nativeValue struct {
	count, distinct int64
	sum             any // int64 or float64, or nil when the group has no value
}

// nativeFields are the fields one grouping reads, and how.
type nativeFields struct {
	names             []string
	counted, distinct map[string]bool // fields a metric counts, or counts distinctly
}

// nativeAnswer validates q with the adapter, reduces each of its groupings
// with reduce, and fills each metric's result as the adapter would. reduce
// reports the rows it matched; more than a population fails the request.
func nativeAnswer(ctx context.Context, tx *sql.Tx, d qt.SQLiteV2Dataset, q qt.MetricQuery, o qt.PlanOptions,
	reduce func(keys []string, fields nativeFields, budget int) (groups []nativeGroup, matched int64, ok bool, err error)) (qt.SQLiteV2ExecutionResult, bool, error) {
	if !nativeMetrics || len(q.Metrics) == 0 {
		return qt.SQLiteV2ExecutionResult{}, false, nil
	}
	// Metrics with the same grouping share one reduction.
	type grouping struct {
		keys   []string
		fields nativeFields
		groups []nativeGroup
	}
	groupings := map[string]*grouping{}
	order := []string{}
	signatures := make([]string, len(q.Metrics))
	for i, spec := range q.Metrics {
		if !nativeMetric(spec, d.Schema) {
			return qt.SQLiteV2ExecutionResult{}, false, nil
		}
		signature, _ := json.Marshal(spec.GroupBy)
		signatures[i] = string(signature)
		g := groupings[signatures[i]]
		if g == nil {
			g = &grouping{keys: spec.GroupBy, fields: nativeFields{counted: map[string]bool{}, distinct: map[string]bool{}}}
			groupings[signatures[i]] = g
			order = append(order, signatures[i])
		}
		if spec.Field != "" && !slices.Contains(g.fields.names, spec.Field) {
			g.fields.names = append(g.fields.names, spec.Field)
		}
		if spec.Op == "count" && spec.Field != "" {
			g.fields.counted[spec.Field] = true
		}
		if spec.Op == "count_distinct" {
			g.fields.distinct[spec.Field] = true
		}
	}
	// Validate as the adapter would, but without running every metric over an
	// empty source as countOnlyMetricsV2 does, a round trip per metric. Compile
	// the metrics: a request or metric the compiler refuses falls through, for
	// the adapter to report. The compiler checks the request, its filter and
	// window. Expected revisions, and the window against the host's row
	// budget, are checked when the adapter executes, so a request with
	// revisions, or a window over 10,000 rows, runs one count over an empty
	// source.
	empty := (sqlDataset{}).v2Source(d.Schema) + " WHERE 0"
	plan := o
	plan.SourceSQL, plan.SourceArgs, plan.ComputedGroupable = empty, nil, d.ComputedGroupable
	if _, err := qt.CompileSQLiteMetrics(ctx, q, d.Schema, plan); err != nil {
		return qt.SQLiteV2ExecutionResult{}, ctx.Err() != nil, ctx.Err()
	}
	rows := d.MaxRows
	if rows == 0 {
		rows = 10_000
	}
	if len(q.ExpectedRevisions) > 0 || len(o.ExpectedRevisions) > 0 || q.Limit > rows {
		validation, probe := d, q
		validation.SourceSQL, validation.SourceArgs = empty, nil
		probe.Metrics = []qt.AggSpec{{ID: "rows", Op: "count"}}
		if _, err := validation.ExecuteV2In(ctx, tx, nil, &probe, o); err != nil {
			return qt.SQLiteV2ExecutionResult{}, true, err
		}
	}
	result := qt.SQLiteV2ExecutionResult{Metrics: &qt.SQLiteMetricsV2Result{Metrics: make([]qt.SQLiteMetricV2, len(q.Metrics))}}
	population, maxGroups := d.MaxPopulation, d.MaxGroups
	if population == 0 {
		population = queryTablePopulation
	}
	if maxGroups == 0 {
		maxGroups = 2000
	}
	var matched int64
	for _, signature := range order {
		g := groupings[signature]
		var ok bool
		var err error
		if g.groups, matched, ok, err = reduce(g.keys, g.fields, population); err != nil || !ok {
			return qt.SQLiteV2ExecutionResult{}, err != nil, err
		}
		if matched > int64(population) {
			return qt.SQLiteV2ExecutionResult{}, true, fmt.Errorf("resource_limit: matching population exceeds host budget")
		}
	}
	for i, spec := range q.Metrics {
		metric := &result.Metrics.Metrics[i]
		*metric = qt.SQLiteMetricV2{ID: spec.ID, Scope: "allMatching", Buckets: []qt.SQLiteMetricV2Bucket{}}
		g := groupings[signatures[i]]
		if len(g.groups) > maxGroups {
			metric.Error = "group population exceeds host budget"
			continue
		}
		column := slices.Index(g.fields.names, spec.Field)
		buckets := make([]qt.SQLiteMetricV2Bucket, 0, len(g.groups))
		for _, group := range g.groups {
			bucket := qt.SQLiteMetricV2Bucket{Keys: slices.Clone(group.keys), Count: group.rows, Value: float64(group.rows)}
			switch {
			case spec.Field == "":
			case spec.Op == "count":
				bucket.Value = float64(group.values[column].count)
			case spec.Op == "count_distinct":
				bucket.Value = float64(group.values[column].distinct)
			default:
				switch sum := group.values[column].sum.(type) {
				case int64:
					bucket.Value = float64(sum)
				case float64:
					bucket.Value = sum
				default:
					bucket.Value = nil
				}
			}
			buckets = append(buckets, bucket)
		}
		sortMetricBuckets(buckets)
		metric.Buckets, metric.GroupCount, metric.ProcessedRows, metric.Coverage = buckets, int64(len(buckets)), matched, "exact"
	}
	return result, true, nil
}

// nativeMetric reports whether the native path answers spec: a count of rows,
// a count or sum of a number field, or a distinct count of a text or enum
// field, over all matching rows, grouped by text or enum fields.
func nativeMetric(spec qt.AggSpec, schema qt.Schema) bool {
	if spec.Expression != "" || spec.ExpressionY != "" || spec.Distribution != nil || spec.DateGrouping != nil || len(spec.Sort) > 0 ||
		spec.GroupLimit != 0 || spec.MinCount != 0 || len(spec.Where) > 0 || len(spec.Diagnostics) > 0 || spec.Scope != "" && spec.Scope != "allMatching" {
		return false
	}
	text := nativeColumn(schema, spec.Field, qt.FieldText) || nativeColumn(schema, spec.Field, qt.FieldEnum)
	if !(spec.Op == "count" && spec.Field == "" || (spec.Op == "count" || spec.Op == "sum") && nativeColumn(schema, spec.Field, qt.FieldNumber) || spec.Op == "count_distinct" && text) {
		return false
	}
	for _, key := range spec.GroupBy {
		if !nativeColumn(schema, key, qt.FieldText) && !nativeColumn(schema, key, qt.FieldEnum) {
			return false
		}
	}
	return true
}

// nativeColumn reports whether name is a schema field of kind read straight
// from the source column of that name.
func nativeColumn(schema qt.Schema, name string, kind qt.FieldKind) bool {
	field, ok := schema.Fields[name]
	return ok && field.Kind == kind && !field.Synthetic && field.Expr == "r."+sqlIdentifier(name)
}

// nativeGroups reduces the source rows matching predicate by keys in one
// statement: each group's row count, and for each field its sum, with its
// non-null count if a metric counts it (a sum is NULL exactly when there is
// none). SQLite's SUM is an integer exactly when every value it adds is one.
// With check, the statement also takes each field's least value, so that a
// negative value cannot hide an absolute total beyond safe integers; without
// it, the caller has bounded the values. It reports false when the adapter
// could answer differently: a value that is not an integer, an unsafe sum or
// one SQLite cannot hold, or a key that is not valid text within the
// adapter's text limit.
func nativeGroups(ctx context.Context, tx *sql.Tx, source, predicate string, args []any, keys, fields []string, counted map[string]bool, check bool) ([]nativeGroup, int64, bool, error) {
	columns := []string{}
	for i, key := range keys {
		columns = append(columns, fmt.Sprintf("r.%s AS k%d", sqlIdentifier(key), i))
	}
	columns = append(columns, "COUNT(*)")
	for _, field := range fields {
		value := "r." + sqlIdentifier(field)
		count, least := "NULL", "NULL"
		if counted[field] {
			count = "COUNT(" + value + ")"
		}
		if check {
			least = "MIN(" + value + ")"
		}
		columns = append(columns, count, "SUM("+value+")", least)
	}
	statement := "SELECT " + strings.Join(columns, ",") + " FROM (" + source + ") AS r" + predicate
	if len(keys) > 0 {
		positions := make([]string, len(keys))
		for i := range keys {
			positions[i] = strconv.Itoa(i + 1)
		}
		statement += " GROUP BY " + strings.Join(positions, ",")
	}
	result, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		// SQLite refuses an integer overflow; the adapter reports it per value.
		return nil, 0, false, ctx.Err()
	}
	defer result.Close()
	groups := []nativeGroup{}
	var matched int64
	for result.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := result.Scan(targets...); err != nil {
			return nil, 0, false, err
		}
		group := nativeGroup{keys: values[:len(keys)], rows: integer(values[len(keys)])}
		for _, key := range group.keys {
			if !nativeKey(key) {
				return nil, 0, false, nil
			}
		}
		for i := range fields {
			count, sum, least := values[len(keys)+1+3*i], values[len(keys)+2+3*i], values[len(keys)+3+3*i]
			if sum != nil && (!isInt64(sum) || integer(sum) < 0 || integer(sum) > 1<<53-1 || check && (!isInt64(least) || integer(least) < 0)) {
				return nil, 0, false, nil
			}
			group.values = append(group.values, nativeValue{count: integer(count), sum: sum})
		}
		matched += group.rows
		groups = append(groups, group)
	}
	if err := result.Err(); err != nil {
		return nil, 0, false, ctx.Err()
	}
	return groups, matched, true, nil
}

// nativeRowGroups is nativeGroups over rows in memory, with the checks the
// adapter makes on the values a copy into SQLite would store. Sums are
// exact (see exactSum). A distinct count counts text values, the empty string
// among them, but not NULLs, comparing them byte for byte.
func nativeRowGroups(rows []map[string]any, keys []string, fields nativeFields) ([]nativeGroup, bool) {
	type accumulator struct {
		count    int64
		sum      exactSum
		distinct map[string]bool
	}
	type group struct {
		keys   []any
		rows   int64
		fields []accumulator
	}
	groups := []*group{}
	byKey := map[string]*group{}
	var id strings.Builder
	var digits []byte
	for _, row := range rows {
		id.Reset()
		for _, key := range keys {
			switch value := row[key].(type) {
			case nil:
				id.WriteString("-")
			case string:
				id.WriteString(strconv.Itoa(len(value)) + ":" + value)
			default:
				return nil, false
			}
		}
		g := byKey[id.String()]
		if g == nil {
			g = &group{keys: make([]any, len(keys)), fields: make([]accumulator, len(fields.names))}
			for i, key := range keys {
				if g.keys[i] = row[key]; !nativeKey(g.keys[i]) {
					return nil, false
				}
			}
			byKey[id.String()] = g
			groups = append(groups, g)
		}
		g.rows++
		for i, field := range fields.names {
			value := row[field]
			if value == nil {
				continue
			}
			a := &g.fields[i]
			if fields.distinct[field] {
				text, ok := value.(string)
				if !ok || !nativeKey(text) {
					return nil, false
				}
				if a.distinct == nil {
					a.distinct = map[string]bool{}
				}
				a.distinct[text] = true
				continue
			}
			var ok bool
			if digits, ok = a.sum.add(value, digits); !ok {
				return nil, false
			}
			a.count++
		}
	}
	if len(keys) == 0 && len(groups) == 0 {
		groups = append(groups, &group{keys: []any{}, fields: make([]accumulator, len(fields.names))})
	}
	out := make([]nativeGroup, len(groups))
	for i, g := range groups {
		out[i] = nativeGroup{keys: g.keys, rows: g.rows, values: make([]nativeValue, len(fields.names))}
		for j := range g.fields {
			sum, ok := g.fields[j].sum.total()
			if !ok {
				return nil, false
			}
			out[i].values[j] = nativeValue{count: g.fields[j].count, distinct: int64(len(g.fields[j].distinct)), sum: sum}
		}
	}
	return out, true
}

// nativeKey reports whether a group key is one the adapter keeps as is: a
// NULL, or valid text within its text limit.
func nativeKey(key any) bool {
	text, ok := key.(string)
	return key == nil || ok && utf8.ValidString(text) && len(text) <= 100_000
}

// executeMetricsV2 answers metrics over a staged source natively when it
// can, and with the adapter otherwise.
func (c *Catalog) executeMetricsV2(ctx context.Context, tx *sql.Tx, d qt.SQLiteV2Dataset, q qt.MetricQuery, o qt.PlanOptions) (qt.SQLiteV2ExecutionResult, error) {
	result, handled, err := c.nativeMetricsV2(ctx, tx, d, q, o, nil)
	if handled || err != nil {
		return result, err
	}
	return d.ExecuteV2In(ctx, tx, nil, &q, o)
}

// sortMetricBuckets orders buckets as the adapter does: errors last, then by
// value, largest first and nulls last, then by each key in ascending binary
// order with nulls last.
func sortMetricBuckets(buckets []qt.SQLiteMetricV2Bucket) {
	sort.SliceStable(buckets, func(i, j int) bool {
		left, right := buckets[i], buckets[j]
		if (left.Error != "") != (right.Error != "") {
			return left.Error == ""
		}
		a, aok := left.Value.(float64)
		b, bok := right.Value.(float64)
		if aok != bok {
			return aok
		}
		if aok && a != b {
			return a > b
		}
		for k := range left.Keys {
			x, xok := left.Keys[k].(string)
			y, yok := right.Keys[k].(string)
			if xok != yok {
				return xok
			}
			if x != y {
				return x < y
			}
		}
		return false
	})
}

func isInt64(value any) bool { _, ok := value.(int64); return ok }
