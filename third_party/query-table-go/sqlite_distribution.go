package querytable

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
)

// MetricDistributionResult is the v2 wire union supported by both renderers.
type MetricDistributionResult interface{ metricDistributionResult() }
type MetricBoxSummary struct {
	N            int64     `json:"n"`
	Min          float64   `json:"min"`
	Q1           float64   `json:"q1"`
	Median       float64   `json:"median"`
	Q3           float64   `json:"q3"`
	Max          float64   `json:"max"`
	Mean         float64   `json:"mean"`
	Low          float64   `json:"low"`
	High         float64   `json:"high"`
	Outliers     []float64 `json:"outliers"`
	OutlierCount int64     `json:"outlierCount"`
	Whiskers     string    `json:"whiskers"`
	Method       string    `json:"method"`
}
type MetricBoxDistribution struct {
	Kind    string            `json:"kind"`
	Summary *MetricBoxSummary `json:"summary"`
}

func (*MetricBoxDistribution) metricDistributionResult() {}

type MetricHistogramDistribution struct {
	Kind   string    `json:"kind"`
	Edges  []float64 `json:"edges"`
	Counts []int64   `json:"counts"`
	N      int64     `json:"n"`
}

func (*MetricHistogramDistribution) metricDistributionResult() {}

// SQLiteMaxBoxSamples bounds exact box memory per group, including Tukey tails.
// Larger populations produce resource_limit, never sampled quartiles.
const SQLiteMaxBoxSamples = SQLiteMaxMedianSamples

type sqliteDistributionPayload struct {
	Value        *float64                 `json:"value"`
	Error        string                   `json:"error,omitempty"`
	Distribution MetricDistributionResult `json:"distribution,omitempty"`
}

func sqliteDistributionJSON(value *float64, code string, result MetricDistributionResult) (driver.Value, error) {
	returnJSON, err := json.Marshal(sqliteDistributionPayload{value, code, result})
	return string(returnJSON), err
}
func sqliteDistributionNumberError(n float64) string {
	if e := sqliteNumericError(n); e != "" {
		return e
	}
	if sqliteUnsafe(n) {
		return "unsafe_integer"
	}
	return ""
}

func sqliteDistributionFunctions() []SQLiteScalarFunction {
	return []SQLiteScalarFunction{{Name: "qt_v2_histogram_edges", Arity: 3, Call: func(a []driver.Value) (driver.Value, error) {
		if len(a) != 3 {
			return nil, fmt.Errorf("histogram edges expects three arguments")
		}
		bins, ok := a[2].(int64)
		if !ok || bins < 2 || bins > 30 {
			return nil, fmt.Errorf("histogram bins must be 2–30")
		}
		payload := sqliteHistogramEdges{Edges: []float64{}}
		if a[0] != nil || a[1] != nil {
			lo, lok := sqliteFloat(a[0])
			hi, hok := sqliteFloat(a[1])
			if !lok || !hok {
				return nil, fmt.Errorf("invalid histogram extent")
			}
			if payload.Error = sqliteDistributionNumberError(lo); payload.Error == "" {
				payload.Error = sqliteDistributionNumberError(hi)
			}
			if payload.Error == "" {
				if lo > hi {
					return nil, fmt.Errorf("reversed histogram extent")
				}
				if lo == hi {
					payload.Edges = []float64{lo, hi}
				} else {
					payload.Edges = make([]float64, int(bins)+1)
					for i := range payload.Edges {
						// Round the product before addition to match the frontend (no FMA).
						ratio := float64(i) / float64(bins)
						offset := float64(float64(hi-lo) * ratio)
						edge := lo + offset
						if i == 0 {
							edge = lo
						}
						if i == int(bins) {
							edge = hi
						}
						payload.Edges[i] = edge
						if e := sqliteDistributionNumberError(edge); e != "" {
							payload.Error = e
							break
						}
						if i > 0 && edge <= payload.Edges[i-1] {
							payload.Error = "histogram_precision"
							break
						}
					}
				}
			}
		}
		if payload.Error != "" {
			payload.Edges = []float64{}
		}
		data, e := json.Marshal(payload)
		return string(data), e
	}}}
}

type sqliteHistogramEdges struct {
	Edges []float64 `json:"edges"`
	Error string    `json:"error,omitempty"`
}

func sqliteDistributionAggregates() []SQLiteAggregateDescriptor {
	return []SQLiteAggregateDescriptor{
		{Name: "qt_v2_box", Arity: 2, New: func() SQLiteAggregateFunction { return &sqliteBoxAggregate{sum: new(big.Rat)} }},
		{Name: "qt_v2_histogram", Arity: 2, New: func() SQLiteAggregateFunction { return &sqliteHistogramAggregate{} }},
	}
}

type sqliteBoxAggregate struct {
	samples       []float64
	sum           *big.Rat
	whiskers, err string
}

func (a *sqliteBoxAggregate) Step(values []driver.Value) error {
	if len(values) != 2 {
		return fmt.Errorf("box expects two arguments")
	}
	mode, ok := values[1].(string)
	if !ok || (mode != "minmax" && mode != "tukey") {
		return fmt.Errorf("invalid whisker mode")
	}
	if a.whiskers != "" && a.whiskers != mode {
		return fmt.Errorf("inconsistent whisker mode")
	}
	a.whiskers = mode
	if values[0] == nil || a.err != "" {
		return nil
	}
	n, ok := sqliteFloat(values[0])
	if !ok {
		a.err = "storage_type"
		return nil
	}
	if a.err = sqliteDistributionNumberError(n); a.err != "" {
		return nil
	}
	if len(a.samples) >= SQLiteMaxBoxSamples {
		a.err = "resource_limit"
		a.samples = nil
		return nil
	}
	a.samples = append(a.samples, n)
	a.sum.Add(a.sum, sqliteRat(n))
	return nil
}
func sqlitePercentile(sorted []float64, p float64) (float64, string) {
	position := float64(len(sorted)-1) * p
	lower := int(math.Floor(position))
	fraction := position - float64(lower)
	l, r := sorted[lower], sorted[min(lower+1, len(sorted)-1)]
	// Explicit conversions force rounding of both products before addition (no FMA).
	raw := float64(float64(1-fraction)*l) + float64(fraction*r)
	if e := sqliteDistributionNumberError(raw); e != "" {
		return 0, e
	}
	return min(r, max(l, raw)), ""
}
func (a *sqliteBoxAggregate) Value() (driver.Value, error) {
	if a.err != "" {
		return sqliteDistributionJSON(nil, a.err, nil)
	}
	if len(a.samples) == 0 {
		return sqliteDistributionJSON(nil, "", &MetricBoxDistribution{Kind: "box"})
	}
	sort.Float64s(a.samples)
	n := len(a.samples)
	q := make([]float64, 3)
	for i, p := range []float64{0.25, 0.5, 0.75} {
		var e string
		q[i], e = sqlitePercentile(a.samples, p)
		if e != "" {
			return sqliteDistributionJSON(nil, e, nil)
		}
	}
	mean, _ := new(big.Rat).Quo(a.sum, big.NewRat(int64(n), 1)).Float64()
	if e := sqliteDistributionNumberError(mean); e != "" {
		return sqliteDistributionJSON(nil, e, nil)
	}
	mode := a.whiskers
	if mode == "" {
		mode = "minmax"
	}
	s := &MetricBoxSummary{N: int64(n), Min: a.samples[0], Q1: q[0], Median: q[1], Q3: q[2], Max: a.samples[n-1], Mean: min(a.samples[n-1], max(a.samples[0], mean)), Low: a.samples[0], High: a.samples[n-1], Outliers: []float64{}, Whiskers: mode, Method: DistributionPrecision}
	if mode == "tukey" {
		spread := float64(1.5 * float64(s.Q3-s.Q1))
		lowFence, highFence := s.Q1-spread, s.Q3+spread
		first := sort.Search(n, func(i int) bool { return a.samples[i] >= lowFence })
		end := sort.Search(n, func(i int) bool { return a.samples[i] > highFence })
		if first >= end {
			return sqliteDistributionJSON(nil, "numeric_range", nil)
		}
		s.Low = a.samples[first]
		s.High = a.samples[end-1]
		s.OutlierCount = int64(first + n - end)
		total := int(s.OutlierCount)
		for i := 0; i < min(total, 20); i++ {
			rank := i
			if total > 20 && i >= 10 {
				rank = total - 20 + i
			}
			index := rank
			if rank >= first {
				index = end + rank - first
			}
			s.Outliers = append(s.Outliers, a.samples[index])
		}
	}
	return sqliteDistributionJSON(&s.Median, "", &MetricBoxDistribution{Kind: "box", Summary: s})
}

type sqliteHistogramAggregate struct {
	initialized bool
	config      string
	edges       []float64
	counts      []int64
	n           int64
	err         string
}

func (a *sqliteHistogramAggregate) Step(values []driver.Value) error {
	if len(values) != 2 {
		return fmt.Errorf("histogram expects two arguments")
	}
	config, ok := values[1].(string)
	if !ok {
		return fmt.Errorf("histogram edges must be JSON text")
	}
	if !a.initialized {
		var p sqliteHistogramEdges
		if len(config) > 10000 || json.Unmarshal([]byte(config), &p) != nil {
			return fmt.Errorf("invalid histogram edges")
		}
		if len(p.Edges) > 31 || len(p.Edges) == 1 {
			return fmt.Errorf("invalid histogram edge count")
		}
		for i, e := range p.Edges {
			if code := sqliteDistributionNumberError(e); code != "" {
				return fmt.Errorf("invalid histogram edge: %s", code)
			}
			if i > 0 && (e < p.Edges[i-1] || e == p.Edges[i-1] && len(p.Edges) != 2) {
				return fmt.Errorf("unordered histogram edges")
			}
		}
		a.config = config
		a.edges = p.Edges
		if a.edges == nil {
			a.edges = []float64{}
		}
		a.counts = make([]int64, max(len(a.edges)-1, 0))
		a.err = p.Error
		a.initialized = true
	} else if a.config != config {
		return fmt.Errorf("inconsistent histogram edges")
	}
	if values[0] == nil || a.err != "" {
		return nil
	}
	v, ok := sqliteFloat(values[0])
	if !ok {
		a.err = "storage_type"
		return nil
	}
	if a.err = sqliteDistributionNumberError(v); a.err != "" {
		return nil
	}
	if len(a.edges) == 0 || v < a.edges[0] || v > a.edges[len(a.edges)-1] {
		return fmt.Errorf("sample falls outside shared histogram edges")
	}
	bin := sort.Search(len(a.edges)-2, func(i int) bool { return v < a.edges[i+1] })
	a.counts[bin]++
	a.n++
	return nil
}
func (a *sqliteHistogramAggregate) Value() (driver.Value, error) {
	if a.err != "" {
		return sqliteDistributionJSON(nil, a.err, nil)
	}
	edges, counts := a.edges, a.counts
	if edges == nil {
		edges = []float64{}
	}
	if counts == nil {
		counts = []int64{}
	}
	n := float64(a.n)
	return sqliteDistributionJSON(&n, "", &MetricHistogramDistribution{Kind: "histogram", Edges: edges, Counts: counts, N: a.n})
}

func (b *planBuilder) sqliteDistribution(spec AggSpec, groups []ValueSQL, scope string) (SQLPlan, error) {
	d := spec.Distribution
	if d.Kind != "box" && d.Kind != "histogram" {
		return SQLPlan{}, diagnostic("distribution", "unknown distribution kind")
	}
	if d.Kind == "histogram" && len(groups) > 1 {
		return SQLPlan{}, diagnostic("distribution", "histograms support at most one grouping key")
	}
	if d.Whiskers != "" && d.Whiskers != "minmax" && d.Whiskers != "tukey" {
		return SQLPlan{}, diagnostic("distribution", "unknown whisker mode")
	}
	bins := d.Bins
	if bins == 0 {
		bins = 10
	}
	if bins < 2 || bins > 30 {
		return SQLPlan{}, diagnostic("distribution", "histogram bins must be 2–30")
	}
	if spec.ExpressionY != "" || spec.Display != nil && spec.Display.Kind == "scatter" {
		return SQLPlan{}, diagnostic("distribution", "distributions cannot be paired metrics")
	}
	n, e := parseExpression(d.Input)
	if e != nil {
		return SQLPlan{}, e
	}
	v, e := b.row(n)
	if e != nil {
		return SQLPlan{}, e
	}
	if e = requireType(v, "number"); e != nil {
		return SQLPlan{}, e
	}
	if e = b.aggregatePolicy(n, d.Kind, map[string]bool{}); e != nil {
		return SQLPlan{}, e
	}
	v = b.metricNumber(v)
	selects := []string{}
	errors := []string{v.Error}
	cols := []OutputColumn{}
	for i, g := range groups {
		name := fmt.Sprintf("group%d", i)
		selects = append(selects, g.Value+" AS "+name, g.Error+" AS "+name+"_error")
		errors = append(errors, g.Error)
		cols = append(cols, OutputColumn{Field: spec.GroupBy[i], ValueAlias: name, Type: g.Type})
	}
	selects = append(selects, v.Value+" AS sample", "COALESCE("+strings.Join(append(errors, "NULL"), ",")+") AS sample_error")
	b.stage("SELECT " + strings.Join(selects, ",") + " FROM " + b.rel)
	population := b.rel
	groupCols := groupOutput(len(groups))
	by := ""
	if len(groupCols) > 0 {
		by = " GROUP BY " + strings.Join(groupCols, ",")
	}
	aggregate := ""
	globalError := "NULL"
	from := population
	if d.Kind == "box" {
		mode := d.Whiskers
		if mode == "" {
			mode = "minmax"
		}
		aggregate = "qt_v2_box(sample," + b.param(mode, "text") + ")"
	} else {
		b.stage("SELECT qt_v2_histogram_edges(MIN(sample),MAX(sample)," + b.param(bins, "integer") + ") AS edges,MIN(sample_error) AS input_error FROM " + population)
		b.stage("SELECT edges,COALESCE(input_error,json_extract(edges,'$.error')) AS extent_error FROM " + b.rel)
		extent := b.rel
		from += " CROSS JOIN " + extent
		aggregate = "qt_v2_histogram(sample,edges)"
		globalError = "MIN(extent_error)"
	}
	selects = append(append([]string{}, groupCols...), "COUNT(*) AS count", "COUNT(sample) AS samples", "COUNT(*) FILTER (WHERE sample IS NULL AND sample_error IS NULL) AS null_count", "COUNT(*) FILTER (WHERE sample_error IS NOT NULL) AS input_error_count", "COALESCE("+globalError+",MIN(sample_error)) AS input_error", aggregate+" AS payload")
	b.stage("SELECT " + strings.Join(selects, ",") + " FROM " + from + by)
	b.stage("SELECT *,COALESCE(input_error,json_extract(payload,'$.error')) AS error FROM " + b.rel)
	selects = append(append([]string{}, groupCols...), "count", "samples", "null_count", "input_error_count", "error", "CASE WHEN error IS NULL THEN json_extract(payload,'$.value') END AS value", "CASE WHEN error IS NULL THEN json_extract(payload,'$.distribution') END AS distribution")
	b.stage("SELECT " + strings.Join(selects, ",") + " FROM " + b.rel)
	order, e := metricOrder(spec, len(groups), false, true)
	if e != nil {
		return SQLPlan{}, e
	}
	sql := "SELECT *,COUNT(*) OVER () AS group_count FROM " + b.rel + order
	if spec.GroupLimit > 0 {
		sql += " LIMIT " + b.param(spec.GroupLimit, "integer")
	}
	cols = append(cols, OutputColumn{ValueAlias: "value", Type: "number"})
	return b.finish(sql, scope, cols), nil
}
