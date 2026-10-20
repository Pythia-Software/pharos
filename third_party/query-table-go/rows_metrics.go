package querytable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ExecuteV2Rows runs V2 metrics over an already-authorized in-memory population.
// It never executes SourceSQL. tx is used only for expected computed revisions
// when no Resolver was supplied; ordinary requests can pass nil. SourceSQL may
// be empty. Callers must bind token/snapshot validation to this same population.
//
// Row keys are schema field names with direct SQL column bindings. Supported
// storage is nil/missing (NULL), string (TEXT), int64 (INTEGER), float64 (REAL),
// and bool for bool fields (bound as 0/1). Text arrays use JSON strings; empty
// datetime strings are NULL. No other Go types are implicitly coerced.
//
// Count, count(field), sum/avg/min/max of numbers, min/max of text/enum/datetime and
// count_distinct of scalar fields are supported, with text/enum/bool grouping,
// per-metric Where, MinCount, sorting and GroupLimit. Formulas, computed fields,
// distributions, shownRows and other groupings return ErrRowsUnsupported.
// Runtime fallback also returns an empty result, so hosts can safely stage and
// retry the complete request. Field storage errors retain V2 bucket diagnostics.
func ExecuteV2Rows(ctx context.Context, tx *sql.Tx, d SQLiteV2Dataset, rows []map[string]any, q MetricQuery, o PlanOptions) (SQLiteV2ExecutionResult, error) {
	return executeV2Rows(ctx, tx, d, rows, q, o, nil)
}

// visit is a test hook for counting physical population visits.
func executeV2Rows(ctx context.Context, tx *sql.Tx, d SQLiteV2Dataset, rows []map[string]any, q MetricQuery, o PlanOptions, visit func()) (SQLiteV2ExecutionResult, error) {
	out := SQLiteV2ExecutionResult{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if d.SourceSQL == "" {
		d.SourceSQL = "SELECT 1"
	}
	maxRows, maxGroups, maxPopulation, err := d.limits()
	if err != nil {
		return out, err
	}
	if q.Limit > maxRows {
		return out, diagnostic("resource_limit", "metric row window exceeds host budget")
	}
	o = d.options(tx, o)
	for _, term := range q.Where {
		for _, p := range term.Predicates() {
			if strings.HasPrefix(p.Field, "@computed/") {
				return out, fmt.Errorf("%w: computed filter %s", ErrRowsUnsupported, p.Field)
			}
		}
	}
	for _, spec := range q.Metrics {
		names := append([]string{spec.Field}, spec.GroupBy...)
		for _, term := range spec.Where {
			for _, p := range term.Predicates() {
				names = append(names, p.Field)
			}
		}
		if spec.Expression != "" {
			if n, e := parseExpression(spec.Expression); e == nil {
				names = append(names, rowExpressionFields(n)...)
			}
		}
		for _, name := range names {
			if strings.HasPrefix(name, "@computed/") {
				return out, fmt.Errorf("%w: computed field %s", ErrRowsUnsupported, name)
			}
		}
	}
	// Reuse the SQL compiler for policy, request and clause validation. It builds
	// plans only: no SQL runs here, including on the nil-transaction fast path.
	_, failures, err := compileMetricsPartial(ctx, q, d.Schema, o, true)
	if err != nil {
		return out, err
	}
	filter, err := CompileRowFilter(q.Where, d.Schema, SQLiteOptions{Now: o.Now})
	if err != nil {
		return out, err
	}
	revisions := o.ExpectedRevisions
	if q.ExpectedRevisions != nil {
		revisions = q.ExpectedRevisions
	}
	result := SQLiteMetricsV2Result{Metrics: make([]SQLiteMetricV2, len(q.Metrics))}
	cohorts := []*rowMetricCohort{}
	byPopulation := map[string]*rowMetricCohort{}
	for i, spec := range q.Metrics {
		if failures[i] != nil {
			message, _ := metricClauseError(ctx, failures[i])
			result.Metrics[i] = failedMetric(spec.ID, spec.Scope, message)
			continue
		}
		if spec.Scope == "shownRows" || spec.DateGrouping != nil || spec.Distribution != nil || spec.ExpressionY != "" {
			return out, fmt.Errorf("%w: metric %s shape", ErrRowsUnsupported, spec.ID)
		}
		// Accept either shorthand or the equivalent single aggregate expression.
		n, e := parseExpression(MetricExpression(spec))
		if e != nil {
			return out, e
		}
		op, field := strings.ToLower(n.op), ""
		if len(n.args) == 1 && n.args[0].op == "field" {
			field = n.args[0].text
		} else if op != "count" || len(n.args) != 0 {
			return out, fmt.Errorf("%w: metric %s expression", ErrRowsUnsupported, spec.ID)
		}
		switch op {
		case "count", "count_distinct", "sum", "avg", "min", "max":
		default:
			return out, fmt.Errorf("%w: metric %s operation", ErrRowsUnsupported, spec.ID)
		}
		if field != "" {
			f := d.Schema.Fields[field]
			if !rowFieldSupported(field, f) || f.Kind == FieldTextArray {
				return out, fmt.Errorf("%w: metric binding %s", ErrRowsUnsupported, field)
			}
		}
		for _, name := range spec.GroupBy {
			f := d.Schema.Fields[name]
			if !rowFieldSupported(name, f) || f.Kind != FieldText && f.Kind != FieldEnum && f.Kind != FieldBool {
				return out, fmt.Errorf("%w: grouping %s", ErrRowsUnsupported, name)
			}
		}
		mf, e := CompileRowFilter(spec.Where, d.Schema, SQLiteOptions{Now: o.Now})
		if e != nil {
			return out, e
		}
		key, _ := json.Marshal(struct {
			Groups []string
			Where  []WhereTerm
		}{spec.GroupBy, spec.Where})
		c := byPopulation[string(key)]
		if c == nil {
			c = &rowMetricCohort{filter: mf, groupBy: spec.GroupBy, groups: map[string]*rowMetricGroup{}}
			cohorts = append(cohorts, c)
			byPopulation[string(key)] = c
		}
		m := rowMetricSpec{index: i, spec: spec, op: op, field: field, binding: d.Schema.Fields[field], reduction: -1}
		for j, r := range c.reducers {
			if r.op == op && r.field == field {
				m.reduction = j
				break
			}
		}
		if m.reduction < 0 {
			m.reduction = len(c.reducers)
			c.reducers = append(c.reducers, m)
		}
		c.metrics = append(c.metrics, m)
	}
	total := int64(0)
	for i, row := range rows {
		if visit != nil {
			visit()
		}
		if i%256 == 0 {
			if err = ctx.Err(); err != nil {
				return out, err
			}
		}
		matches, e := filter.Match(row)
		if e != nil {
			return out, e
		}
		if !matches {
			continue
		}
		total++
		if total > int64(maxPopulation) {
			return out, diagnostic("resource_limit", "matching population exceeds host budget")
		}
		for _, c := range cohorts {
			// Overflow is a clause failure; keep scanning to enforce the request's
			// population bound and to produce unaffected sibling populations.
			if c.overflow {
				continue
			}
			matches, e = c.filter.Match(row)
			if e != nil {
				return out, e
			}
			if !matches {
				continue
			}
			g, e := c.group(row, d.Schema, maxGroups)
			if e != nil {
				return out, e
			}
			if g == nil {
				continue
			}
			g.count++
			for j := range c.reducers {
				if e = g.reductions[j].step(row, &c.reducers[j]); e != nil {
					return out, e
				}
			}
		}
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	// Match ExecuteV2In's request-error priority: population bounds are
	// enforced before checking revision entries unused by the metric plans.
	if _, _, err = d.verifyV2Revisions(ctx, o, revisions); err != nil {
		return out, err
	}
	for _, c := range cohorts {
		// SQLite emits one ungrouped aggregate even for an empty population.
		if len(c.groupBy) == 0 && len(c.groups) == 0 {
			c.groups[""] = newRowMetricGroup([]any{}, len(c.reducers))
		}
		for _, m := range c.metrics {
			if c.overflow {
				result.Metrics[m.index] = failedMetric(m.spec.ID, m.spec.Scope, "group population exceeds host budget")
				continue
			}
			metric := SQLiteMetricV2{ID: m.spec.ID, Scope: "allMatching", ProcessedRows: total, Coverage: "exact", Buckets: []SQLiteMetricV2Bucket{}}
			hidden := int64(0)
			for _, g := range c.groups {
				if g.count < int64(m.spec.MinCount) {
					hidden++
					continue
				}
				value, code, e := g.reductions[m.reduction].value(&m)
				if e != nil {
					return out, e
				}
				metric.Buckets = append(metric.Buckets, SQLiteMetricV2Bucket{Keys: append([]any{}, g.keys...), Value: value, Error: code, Count: g.count})
			}
			metric.GroupCount = int64(len(metric.Buckets))
			if m.spec.MinCount > 0 {
				metric.HiddenGroups = &hidden
			}
			sortRowMetric(metric.Buckets, m.spec)
			if m.spec.GroupLimit > 0 && len(metric.Buckets) > m.spec.GroupLimit {
				metric.Buckets = metric.Buckets[:m.spec.GroupLimit]
			}
			if (m.op == "min" || m.op == "max") && m.binding.Kind == FieldDatetime {
				for i := range metric.Buckets {
					metric.Buckets[i].Value = typedQueryValue(metric.Buckets[i].Value, "datetime")
				}
			}
			result.Metrics[m.index] = metric
		}
	}
	out.Metrics = &result
	return out, nil
}

func rowExpressionFields(n *exprNode) []string {
	var names []string
	if n.op == "field" {
		names = append(names, n.text)
	}
	for _, arg := range n.args {
		names = append(names, rowExpressionFields(arg)...)
	}
	return names
}

// ExecuteV2Rows is also available as a dataset method, alongside ExecuteV2In.
func (d SQLiteV2Dataset) ExecuteV2Rows(ctx context.Context, tx *sql.Tx, rows []map[string]any, q MetricQuery, o PlanOptions) (SQLiteV2ExecutionResult, error) {
	return ExecuteV2Rows(ctx, tx, d, rows, q, o)
}

type rowMetricSpec struct {
	index     int
	reduction int
	spec      AggSpec
	op, field string
	binding   FieldSpec
}
type rowMetricCohort struct {
	filter   RowFilter
	groupBy  []string
	metrics  []rowMetricSpec
	reducers []rowMetricSpec
	groups   map[string]*rowMetricGroup
	overflow bool
}
type rowMetricGroup struct {
	keys       []any
	count      int64
	reductions []rowMetricReduction
}

func newRowMetricGroup(keys []any, n int) *rowMetricGroup {
	return &rowMetricGroup{keys: keys, reductions: make([]rowMetricReduction, n)}
}
func (c *rowMetricCohort) group(row map[string]any, s Schema, limit int) (*rowMetricGroup, error) {
	key := ""
	keys := make([]any, 0, len(c.groupBy))
	for _, name := range c.groupBy {
		binding := s.Fields[name]
		v, e, err := rowMetricValue(row[name], &binding)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return nil, fmt.Errorf("%w: grouping storage %s", ErrRowsUnsupported, name)
		}
		if s.Fields[name].Kind == FieldBool && v != nil {
			v = v.(int64) != 0
		}
		keys = append(keys, v)
	}
	if len(keys) > 0 {
		b, _ := json.Marshal(keys)
		key = string(b)
	}
	if g := c.groups[key]; g != nil {
		return g, nil
	}
	if len(c.groups) >= limit {
		c.overflow = true
		c.groups = nil
		return nil, nil
	}
	g := newRowMetricGroup(keys, len(c.reducers))
	c.groups[key] = g
	return g, nil
}

func rowMetricValue(raw any, f *FieldSpec) (any, string, error) {
	v, err := rowStorage(raw, f)
	if err != nil || v == nil {
		return v, "", err
	}
	switch f.Kind {
	case FieldNumber:
		n, ok := sqliteFloat(v)
		if !ok {
			return nil, "storage_type", nil
		}
		if e := sqliteNumericError(n); e != "" {
			return nil, e, nil
		}
	case FieldText, FieldEnum:
		s, ok := v.(string)
		if ok && strings.ContainsRune(s, 0) {
			return nil, "", fmt.Errorf("%w: embedded NUL text", ErrRowsUnsupported)
		}
		if !ok {
			return nil, "storage_type", nil
		}
		if !utf8.ValidString(s) || len(s) > maxFormulaTextBytes {
			return nil, "text_range", nil
		}
	case FieldDatetime:
		if text, ok := v.(string); ok && strings.ContainsRune(text, 0) {
			return nil, "", fmt.Errorf("%w: datetime with embedded NUL", ErrRowsUnsupported)
		}
		value, code := sqliteV2Datetime(v, f.SQLiteDatetimeFormat)
		return value, code, nil
	case FieldBool:
		n, ok := v.(int64)
		if !ok || n != 0 && n != 1 {
			return nil, "storage_type", nil
		}
	}
	return v, "", nil
}

type rowMetricReduction struct {
	count   int64
	seen    map[string]struct{}
	numeric rowDecimalAggregate
	extreme any
	err     string
}

func (r *rowMetricReduction) step(row map[string]any, m *rowMetricSpec) error {
	if m.field == "" {
		r.count++
		return nil
	}
	v, code, err := rowMetricValue(row[m.field], &m.binding)
	if err != nil {
		return err
	}
	if m.op != "count" && v != nil && m.binding.Kind == FieldNumber {
		n, _ := sqliteFloat(v)
		if sqliteUnsafe(n) {
			code = "unsafe_integer"
			v = nil
		}
	}
	if code != "" {
		if r.err == "" || code < r.err {
			r.err = code
		}
		return nil
	}
	if v == nil {
		return nil
	}
	switch m.op {
	case "count":
		r.count++
	case "count_distinct":
		if r.seen == nil {
			r.seen = map[string]struct{}{}
		}
		key, text := v.(string)
		if !text {
			n, _ := sqliteFloat(v)
			if n == 0 {
				n = 0
			}
			key = strconv.FormatFloat(n, 'g', -1, 64)
		}
		r.seen[key] = struct{}{}
	case "sum", "avg":
		n, _ := sqliteFloat(v)
		r.numeric.add(n)
	case "min", "max":
		if r.extreme == nil || m.op == "min" && rowCompare(v, r.extreme) < 0 || m.op == "max" && rowCompare(v, r.extreme) > 0 {
			r.extreme = v
		}
	}
	return nil
}
func (r *rowMetricReduction) value(m *rowMetricSpec) (any, string, error) {
	if r.err != "" {
		return nil, r.err, nil
	}
	switch m.op {
	case "count":
		return float64(r.count), "", nil
	case "count_distinct":
		return float64(len(r.seen)), "", nil
	case "min", "max":
		v := r.extreme
		if n, ok := sqliteFloat(v); ok {
			return n, "", nil
		}
		return v, "", nil
	default:
		raw, err := r.numeric.value(m.op)
		if err != nil {
			return nil, "", err
		}
		var n sqliteNumericResult
		if err = json.Unmarshal([]byte(raw.(string)), &n); err != nil {
			return nil, "", err
		}
		if n.Value == nil {
			return nil, n.Error, nil
		}
		return *n.Value, n.Error, nil
	}
}
func sortRowMetric(buckets []SQLiteMetricV2Bucket, spec AggSpec) {
	terms := append([]MetricSort{}, spec.Sort...)
	if len(terms) == 0 {
		terms = []MetricSort{{Key: "value", Dir: "desc"}}
	}
	seen := map[string]bool{}
	for _, term := range terms {
		seen[term.Key] = true
	}
	for i := range spec.GroupBy {
		name := "group" + strconv.Itoa(i)
		if !seen[name] {
			terms = append(terms, MetricSort{Key: name, Dir: "asc"})
		}
	}
	get := func(b SQLiteMetricV2Bucket, key string) any {
		if key == "value" {
			return b.Value
		}
		if key == "count" {
			return b.Count
		}
		i, _ := strconv.Atoi(strings.TrimPrefix(key, "group"))
		return b.Keys[i]
	}
	sort.SliceStable(buckets, func(i, j int) bool {
		a, b := buckets[i], buckets[j]
		if (a.Error != "") != (b.Error != "") {
			return a.Error == ""
		}
		for _, term := range terms {
			av, bv := get(a, term.Key), get(b, term.Key)
			if av == nil && bv == nil {
				continue
			}
			if av == nil || bv == nil {
				if strings.EqualFold(term.Nulls, "first") {
					return av == nil
				}
				return bv == nil
			}
			cmp := rowCompare(av, bv)
			if cmp == 0 {
				continue
			}
			if term.Dir == "desc" {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
}
