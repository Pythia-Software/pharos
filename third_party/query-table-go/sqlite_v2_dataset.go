package querytable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"
)

// SQLiteV2Dataset executes plans over a host-authorized SELECT. SourceArgs use
// numbered ?1..?N bindings. Scope/Dataset select the authorized definition store.
// The host owns authentication, token signing, timeouts, indexes, and pool setup.
type SQLiteV2Dataset struct {
	Schema            Schema
	SourceSQL         string
	SourceArgs        []any
	Scope, Dataset    string
	ComputedGroupable map[string]bool
	// Zero defaults to 10000 rows, 2000 groups and 250000 matching source rows.
	// Limits fail explicitly; no approximate or silently truncated populations.
	MaxRows, MaxGroups, MaxPopulation int
}
type SQLiteMetricCapabilities struct {
	Version        int    `json:"version"`
	Expressions    bool   `json:"expressions"`
	ShownRows      bool   `json:"shownRows"`
	Distributions  bool   `json:"distributions"`
	ComputedFields bool   `json:"computedFields"`
	MaxGroups      int    `json:"maxGroups"`
	Profile        string `json:"profile"`
}

func (d SQLiteV2Dataset) Capabilities() SQLiteMetricCapabilities {
	groups := d.MaxGroups
	if groups == 0 {
		groups = 2000
	}
	return SQLiteMetricCapabilities{2, true, true, true, true, groups, SQLiteExpressionProfile}
}

type SQLiteValueError struct {
	Code string `json:"code"`
}
type SQLiteComputedValue struct {
	Value any               `json:"value"`
	Error *SQLiteValueError `json:"error,omitempty"`
}
type SQLiteComputedRow struct {
	ID     any                            `json:"id"`
	Values map[string]SQLiteComputedValue `json:"values"`
}
type SQLiteComputedFieldCapability struct {
	Type    string `json:"type"`
	Select  bool   `json:"select"`
	Sort    bool   `json:"sort"`
	Measure bool   `json:"measure"`
	Group   bool   `json:"group"`
}

// A fingerprint is not authorization. Hosts may attach a signed planToken and
// a snapshot only when they actually bind subsequent requests to that snapshot.
type SQLiteComputedExecution struct {
	Profile           string                                   `json:"profile"`
	PlanToken         string                                   `json:"planToken"`
	ResolvedRevisions map[string]string                        `json:"resolvedRevisions"`
	Fields            map[string]SQLiteComputedFieldCapability `json:"fields"`
	Fingerprint       string                                   `json:"fingerprint,omitempty"`
	Snapshot          string                                   `json:"snapshot,omitempty"`
}
type SQLiteRowsV2Result struct {
	Version   int                     `json:"version"`
	Rows      []map[string]any        `json:"rows"`
	Total     int64                   `json:"total"`
	Computed  []SQLiteComputedRow     `json:"computed"`
	Execution SQLiteComputedExecution `json:"execution"`
}
type SQLiteMetricV2Bucket struct {
	Distribution    MetricDistributionResult `json:"distribution,omitempty"`
	NullCount       *int64                   `json:"nullCount,omitempty"`
	InputErrorCount *int64                   `json:"inputErrorCount,omitempty"`
	Keys            []any                    `json:"keys"`
	Value           any                      `json:"value"`
	Count           int64                    `json:"count"`
	Error           string                   `json:"error,omitempty"`
	Y               *any                     `json:"y,omitempty"`
	YError          string                   `json:"yError,omitempty"`
}
type SQLiteMetricV2 struct {
	ID            string                 `json:"id"`
	Buckets       []SQLiteMetricV2Bucket `json:"buckets"`
	Scope         string                 `json:"scope"`
	ProcessedRows int64                  `json:"processedRows"`
	GroupCount    int64                  `json:"groupCount"`
	Coverage      string                 `json:"coverage"`
}
type SQLiteMetricsV2Result struct {
	Metrics []SQLiteMetricV2 `json:"metrics"`
}
type SQLiteV2ExecutionResult struct {
	Rows    *SQLiteRowsV2Result    `json:"rows,omitempty"`
	Metrics *SQLiteMetricsV2Result `json:"metrics,omitempty"`
}

func (d SQLiteV2Dataset) options(tx *sql.Tx, o PlanOptions) PlanOptions {
	o.SourceSQL = d.SourceSQL
	o.SourceArgs = append([]any{}, d.SourceArgs...)
	o.ComputedGroupable = d.ComputedGroupable
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.Resolver == nil {
		o.Resolver = SQLiteComputedDefinitionResolver{Tx: tx, Scope: d.Scope, Dataset: d.Dataset}
	}
	original := o.Resolver
	cache := map[string]ComputedColumn{}
	o.Resolver = DefinitionResolverFunc(func(ctx context.Context, id string) (ComputedColumn, error) {
		if c, ok := cache[id]; ok {
			return c, nil
		}
		c, e := original.ResolveComputed(ctx, id)
		if e == nil {
			cache[id] = c
		}
		return c, e
	})
	return o
}
func (d SQLiteV2Dataset) limits() (int, int, int, error) {
	rows, groups, pop := d.MaxRows, d.MaxGroups, d.MaxPopulation
	if rows == 0 {
		rows = 10000
	}
	if groups == 0 {
		groups = 2000
	}
	if pop == 0 {
		pop = 250000
	}
	if rows < 1 || rows > 10000 || groups < 1 || groups > 10000 || pop < 1 || pop > 10000000 {
		return 0, 0, 0, diagnostic("resource_limit", "invalid host execution budgets")
	}
	if e := validateSQLiteSchema(d.Schema); e != nil {
		return 0, 0, 0, e
	}
	if strings.TrimSpace(d.SourceSQL) == "" {
		return 0, 0, 0, diagnostic("source_required", "authorized source SELECT required")
	}
	return rows, groups, pop, nil
}

// ExecuteV2 executes any row/metric combination atomically in one short read
// transaction. To join other host operations, use ExecuteV2In with their Tx.
func (d SQLiteV2Dataset) ExecuteV2(ctx context.Context, db *sql.DB, rows *ServerQueryV2, metrics *MetricQuery, o PlanOptions) (SQLiteV2ExecutionResult, error) {
	// A fresh transaction cannot implement a snapshot retained from another request.
	if rows != nil && rows.Snapshot != "" || metrics != nil && metrics.Snapshot != "" {
		return SQLiteV2ExecutionResult{}, diagnostic("snapshot_transaction_required", "snapshot requests require caller-owned ExecuteV2In transaction")
	}
	return sqliteRead(ctx, db, func(tx *sql.Tx) (SQLiteV2ExecutionResult, error) { return d.ExecuteV2In(ctx, tx, rows, metrics, o) })
}
func (d SQLiteV2Dataset) ExecuteV2In(ctx context.Context, tx *sql.Tx, rows *ServerQueryV2, metrics *MetricQuery, o PlanOptions) (SQLiteV2ExecutionResult, error) {
	var out SQLiteV2ExecutionResult
	maxRows, maxGroups, maxPopulation, e := d.limits()
	if e != nil {
		return out, e
	}
	if tx == nil {
		return out, fmt.Errorf("nil SQLite transaction")
	}
	if rows == nil && metrics == nil {
		return out, diagnostic("query_required", "row or metric request required")
	}
	if rows != nil && metrics != nil {
		if !sqliteV2SameSlice(rows.Where, metrics.Where) || !sqliteV2SameSlice([]OrderBy(rows.OrderBy), metrics.OrderBy) || rows.Limit != metrics.Limit || rows.Offset != metrics.Offset || rows.Snapshot != metrics.Snapshot || rows.PlanToken != metrics.PlanToken || !maps.Equal(rows.ExpectedRevisions, metrics.ExpectedRevisions) {
			return out, diagnostic("view_mismatch", "combined rows and metrics require identical filters, ordering, window, revisions and token/snapshot bindings")
		}
	}
	o = d.options(tx, o)
	var rowPlan SQLPlan
	var batch MetricBatchPlan
	var where []WhereTerm
	if rows != nil {
		copy := *rows
		copy.Select = append([]string{}, rows.Select...)
		if copy.Limit < 1 || copy.Limit > maxRows {
			return out, diagnostic("resource_limit", "v2 rows require a positive bounded limit")
		}
		found := false
		for _, n := range copy.Select {
			found = found || n == d.Schema.IDField
		}
		if !found {
			copy.Select = append(copy.Select, d.Schema.IDField)
		}
		rowPlan, e = CompileSQLiteRowsV2(ctx, copy, d.Schema, o)
		if e != nil {
			return out, e
		}
		where = copy.Where
	}
	if metrics != nil {
		if metrics.Limit > maxRows {
			return out, diagnostic("resource_limit", "metric row window exceeds host budget")
		}
		batch, e = CompileSQLiteMetrics(ctx, *metrics, d.Schema, o)
		if e != nil {
			return out, e
		}
		where = metrics.Where
	}
	// Bound the population before expensive formulas, sorting and reductions. The
	// count itself scans at most budget+1 source rows in the authorized snapshot.
	filter, e := CompileSQLiteWhere(where, d.Schema, SQLiteOptions{Now: o.Now})
	if e != nil {
		return out, e
	}
	pred := ""
	if filter.SQL != "" {
		pred = " WHERE " + sqliteNumberParameters(filter.SQL, len(d.SourceArgs))
	}
	countSQL := "SELECT COUNT(*) FROM (SELECT 1 FROM (" + d.SourceSQL + ") AS r" + pred + fmt.Sprintf(" LIMIT %d)", maxPopulation+1)
	args := append(append([]any{}, d.SourceArgs...), filter.Args...)
	var total int64
	if e = tx.QueryRowContext(ctx, countSQL, args...).Scan(&total); e != nil {
		return out, e
	}
	if total > int64(maxPopulation) {
		return out, diagnostic("resource_limit", "matching population exceeds host budget")
	}
	// The frontend validates every revision in its handshake, including fields
	// hidden in this view. Resolve and check the complete requested envelope.
	revisions := o.ExpectedRevisions
	if rows != nil && rows.ExpectedRevisions != nil {
		revisions = rows.ExpectedRevisions
	}
	if metrics != nil && metrics.ExpectedRevisions != nil {
		revisions = metrics.ExpectedRevisions
	}
	verified := map[string]string{}
	capabilities := map[string]SQLiteComputedFieldCapability{}
	capabilityOptions := o
	capabilityOptions.ExpectedRevisions = revisions
	if len(revisions) > 64 {
		return out, diagnostic("dependency_limit", "revision envelope exceeds budget")
	}
	for id, revision := range revisions {
		if !computedID.MatchString(id) || revision == "" || len(revision) > 256 {
			return out, diagnostic("definition_changed", "invalid revision envelope")
		}
		c, err := o.Resolver.ResolveComputed(ctx, id)
		if err != nil {
			return out, err
		}
		if c.ID != id || c.Revision != revision {
			return out, diagnostic("definition_changed", "missing or mismatched expected revision")
		}
		verified[id] = revision
		name := "@computed/" + id
		preview, err := CompileSQLiteRowExpression(ctx, "["+name+"]", d.Schema, capabilityOptions)
		if err != nil {
			return out, err
		}
		capabilities[name] = d.capabilityForPlan(name, preview)
	}
	if rows != nil {
		for id, revision := range rowPlan.ResolvedRevisions {
			verified[id] = revision
			name := "@computed/" + id
			preview, err := CompileSQLiteRowExpression(ctx, "["+name+"]", d.Schema, capabilityOptions)
			if err != nil {
				return out, err
			}
			capabilities[name] = d.capabilityForPlan(name, preview)
		}
		rowPlan.ResolvedRevisions = verified
		r, err := d.readV2Rows(ctx, tx, rowPlan, total, rows.Snapshot)
		if err != nil {
			return SQLiteV2ExecutionResult{}, err
		}
		r.Execution.PlanToken = rows.PlanToken
		r.Execution.Fields = capabilities
		out.Rows = &r
	}
	if metrics != nil {
		r := SQLiteMetricsV2Result{Metrics: []SQLiteMetricV2{}}
		memo := map[string]SQLiteMetricV2{}
		reductions := newSQLiteReductionCache(batch)
		defer reductions.close(tx)
		for i, p := range batch.Metrics {
			key := p.Fingerprint + "\x00" + p.Scope
			if previous, ok := memo[key]; ok {
				previous = cloneSQLiteMetric(previous)
				previous.ID = p.ID
				r.Metrics = append(r.Metrics, previous)
				continue
			}
			p, err := reductions.plan(ctx, tx, p)
			if err != nil {
				return SQLiteV2ExecutionResult{}, err
			}
			m, err := d.readV2Metric(ctx, tx, p, metrics.Metrics[i], total, *metrics, maxGroups)
			if err != nil {
				return SQLiteV2ExecutionResult{}, err
			}
			memo[key] = m
			r.Metrics = append(r.Metrics, m)
		}
		out.Metrics = &r
	}
	return out, nil
}
func sqliteError(v any) *SQLiteValueError {
	if v == nil {
		return nil
	}
	return &SQLiteValueError{Code: fmt.Sprint(v)}
}
func sqliteScanRows(rows *sql.Rows) ([]any, error) {
	cols, e := rows.Columns()
	if e != nil {
		return nil, e
	}
	values := make([]any, len(cols))
	targets := make([]any, len(cols))
	for i := range values {
		targets[i] = &values[i]
	}
	e = rows.Scan(targets...)
	return values, e
}
func (d SQLiteV2Dataset) readV2Rows(ctx context.Context, tx *sql.Tx, p SQLPlan, total int64, snapshot string) (SQLiteRowsV2Result, error) {
	out := SQLiteRowsV2Result{Version: 2, Total: total, Rows: []map[string]any{}, Computed: []SQLiteComputedRow{}, Execution: SQLiteComputedExecution{Profile: p.Profile, Fingerprint: p.Fingerprint, ResolvedRevisions: p.ResolvedRevisions, Fields: map[string]SQLiteComputedFieldCapability{}, Snapshot: snapshot}}
	rows, e := tx.QueryContext(ctx, p.SQL, p.Args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	seen := map[string]bool{}
	for _, c := range p.Columns {
		if strings.HasPrefix(c.Field, "@computed/") {
			out.Execution.Fields[c.Field] = d.computedCapability(c.Field, c.Type)
		}
	}
	for rows.Next() {
		v, e := sqliteScanRows(rows)
		if e != nil {
			return out, e
		}
		row := map[string]any{}
		computed := map[string]SQLiteComputedValue{}
		for i, c := range p.Columns {
			value := v[2*i]
			err := sqliteError(v[2*i+1])
			if strings.HasPrefix(c.Field, "@computed/") {
				value = sqliteV2TypedValue(value, c.Type)
				computed[strings.TrimPrefix(c.Field, "@computed/")] = SQLiteComputedValue{value, err}
			} else {
				if err != nil {
					return out, diagnostic(err.Code, "invalid source value for "+c.Field)
				}
				f := d.Schema.Fields[c.Field]
				if f.Kind == FieldDatetime {
					f.SQLiteDatetimeFormat = "rfc3339"
				}
				value, e = sqliteOutput(value, f)
				if e != nil {
					return out, e
				}
				row[c.Field] = value
			}
		}
		id := row[d.Schema.IDField]
		if id == nil || id == "" {
			return out, diagnostic("identity_required", "row identity is NULL")
		}
		key, e := json.Marshal(id)
		if e != nil {
			return out, e
		}
		if seen[string(key)] {
			return out, diagnostic("duplicate_identity", "source contains duplicate row identities")
		}
		seen[string(key)] = true
		out.Rows = append(out.Rows, row)
		out.Computed = append(out.Computed, SQLiteComputedRow{id, computed})
	}
	return out, rows.Err()
}
func (d SQLiteV2Dataset) readV2Metric(ctx context.Context, tx *sql.Tx, p MetricSQLPlan, spec AggSpec, total int64, q MetricQuery, maxGroups int) (SQLiteMetricV2, error) {
	processed := total
	if p.Scope == "shownRows" {
		processed -= int64(q.Offset)
		if processed < 0 {
			processed = 0
		}
		if processed > int64(q.Limit) {
			processed = int64(q.Limit)
		}
	}
	out := SQLiteMetricV2{ID: p.ID, Scope: p.Scope, ProcessedRows: processed, Coverage: "exact", Buckets: []SQLiteMetricV2Bucket{}}
	rows, e := tx.QueryContext(ctx, p.SQL, p.Args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return out, e
	}
	for rows.Next() {
		values, e := sqliteScanRows(rows)
		if e != nil {
			return out, e
		}
		v := map[string]any{}
		for i, k := range cols {
			v[k] = values[i]
		}
		groups, ok := v["group_count"].(int64)
		if !ok {
			return out, fmt.Errorf("missing group count")
		}
		out.GroupCount = groups
		if groups > int64(maxGroups) {
			return out, diagnostic("resource_limit", "group population exceeds host budget")
		}
		for _, c := range p.Columns {
			v[c.ValueAlias] = sqliteV2TypedValue(v[c.ValueAlias], c.Type)
		}
		b := SQLiteMetricV2Bucket{Keys: []any{}, Value: v["value"], Count: v["count"].(int64), Error: sqliteErrorCode(v["error"])}
		for i := range spec.GroupBy {
			key := v[fmt.Sprintf("group%d", i)]
			b.Keys = append(b.Keys, key)
		}
		if spec.Distribution != nil {
			nulls := v["null_count"].(int64)
			inputErrors := v["input_error_count"].(int64)
			b.NullCount = &nulls
			b.InputErrorCount = &inputErrors
			if raw := v["distribution"]; raw != nil {
				var header struct{ Kind string }
				data, ok := raw.(string)
				if !ok {
					return out, fmt.Errorf("distribution must be JSON text")
				}
				if e = json.Unmarshal([]byte(data), &header); e != nil {
					return out, e
				}
				switch header.Kind {
				case "box":
					var payload MetricBoxDistribution
					e = json.Unmarshal([]byte(data), &payload)
					b.Distribution = &payload
				case "histogram":
					var payload MetricHistogramDistribution
					e = json.Unmarshal([]byte(data), &payload)
					b.Distribution = &payload
				default:
					return out, fmt.Errorf("unknown distribution result")
				}
				if e != nil {
					return out, e
				}
			}
		}
		if spec.ExpressionY != "" {
			yv := v["y"]
			b.Y = &yv
			b.YError = sqliteErrorCode(v["y_error"])
		}
		out.Buckets = append(out.Buckets, b)
	}
	return out, rows.Err()
}
func (d SQLiteV2Dataset) computedCapability(name, typ string) SQLiteComputedFieldCapability {
	if typ == "null" {
		typ = "text"
	}
	return SQLiteComputedFieldCapability{Type: typ, Select: true, Sort: true, Measure: true, Group: d.ComputedGroupable[strings.TrimPrefix(name, "@computed/")]}
}

// DescribeComputedIn validates current canonical definitions and their transitive
// graph in a host-owned snapshot. IDs come from the authorized catalogue. Hosts
// sign the returned envelope if plan tokens are part of their protocol.
func (d SQLiteV2Dataset) DescribeComputedIn(ctx context.Context, tx *sql.Tx, ids []string, o PlanOptions) (SQLiteComputedExecution, error) {
	out := SQLiteComputedExecution{Profile: SQLiteExpressionProfile, ResolvedRevisions: map[string]string{}, Fields: map[string]SQLiteComputedFieldCapability{}}
	if _, _, _, e := d.limits(); e != nil {
		return out, e
	}
	if tx == nil {
		return out, fmt.Errorf("nil SQLite transaction")
	}
	if len(ids) > 64 {
		return out, diagnostic("dependency_limit", "too many computed fields")
	}
	o = d.options(tx, o)
	original := o.Resolver
	o.ExpectedRevisions = out.ResolvedRevisions
	o.Resolver = DefinitionResolverFunc(func(ctx context.Context, id string) (ComputedColumn, error) {
		c, e := original.ResolveComputed(ctx, id)
		if e == nil {
			out.ResolvedRevisions[id] = c.Revision
		}
		return c, e
	})
	for _, id := range ids {
		if strings.HasPrefix(id, "@computed/") {
			id = strings.TrimPrefix(id, "@computed/")
		}
		name := "@computed/" + id
		p, e := CompileSQLiteRowExpression(ctx, "["+strings.ReplaceAll(name, "]", "]]")+"]", d.Schema, o)
		if e != nil {
			return SQLiteComputedExecution{}, e
		}
		out.Fields[name] = d.capabilityForPlan(name, p)
	}
	return out, nil
}

func sqliteV2TypedValue(v any, typ string) any {
	if v == nil {
		return nil
	}
	if typ == "number" {
		if n, ok := v.(int64); ok {
			return float64(n)
		}
	}
	if typ == "bool" {
		if n, ok := v.(int64); ok {
			return n != 0
		}
	}
	return v
}

func sqliteErrorCode(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func sqliteV2SameSlice[T any](a, b []T) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func (d SQLiteV2Dataset) capabilityForPlan(name string, p SQLPlan) SQLiteComputedFieldCapability {
	c := d.computedCapability(name, p.Columns[0].Type)
	for _, dep := range p.Dependencies {
		if f, ok := d.Schema.Fields[dep]; ok && f.AggregateOps != nil && len(f.AggregateOps) == 0 {
			c.Measure = false
		}
	}
	return c
}

// Reuse identical plans only within the transaction after validating every plan.
// Return independent results so callers cannot mutate a sibling metric.
func cloneSQLiteMetric(m SQLiteMetricV2) SQLiteMetricV2 {
	m.Buckets = append([]SQLiteMetricV2Bucket{}, m.Buckets...)
	for i := range m.Buckets {
		b := &m.Buckets[i]
		b.Keys = append([]any{}, b.Keys...)
		if b.Y != nil {
			y := *b.Y
			b.Y = &y
		}
		if b.NullCount != nil {
			n := *b.NullCount
			b.NullCount = &n
		}
		if b.InputErrorCount != nil {
			n := *b.InputErrorCount
			b.InputErrorCount = &n
		}
		switch d := b.Distribution.(type) {
		case *MetricHistogramDistribution:
			c := *d
			c.Edges = append([]float64{}, d.Edges...)
			c.Counts = append([]int64{}, d.Counts...)
			b.Distribution = &c
		case *MetricBoxDistribution:
			c := *d
			if d.Summary != nil {
				summary := *d.Summary
				summary.Outliers = append([]float64{}, d.Summary.Outliers...)
				c.Summary = &summary
			}
			b.Distribution = &c
		}
	}
	return m
}
