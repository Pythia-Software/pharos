package querytable

import (
	"fmt"
	"strconv"
	"strings"
)

func (b *planBuilder) distribution(spec AggSpec, groups []ValueSQL, scope string) (SQLPlan, error) {
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
	// Distribution is a distinct aggregate permission; a restricted field must
	// explicitly admit box/histogram, including through computed dependencies.
	if e = b.aggregatePolicy(n, d.Kind, map[string]bool{}); e != nil {
		return SQLPlan{}, e
	}
	v = b.metricNumber(v)
	sels := []string{}
	errs := []string{v.Error}
	for i, g := range groups {
		a := "group" + strconv.Itoa(i)
		sels = append(sels, g.Value+" AS "+a, g.Error+" AS "+a+"_error")
		errs = append(errs, g.Error)
	}
	sels = append(sels, "("+v.Value+")::double precision AS sample", "COALESCE("+strings.Join(append(errs, "NULL::text"), ",")+") AS sample_error")
	b.stage("SELECT " + strings.Join(sels, ",") + " FROM " + b.rel)
	rows := b.rel
	groupCols := groupOutput(len(groups))
	by := ""
	if len(groupCols) > 0 {
		by = " GROUP BY " + strings.Join(groupCols, ",")
	}
	stats := append(append([]string{}, groupCols...), "COUNT(*) AS count", "COUNT(sample) AS samples", "COUNT(*) FILTER (WHERE sample IS NULL AND sample_error IS NULL) AS null_count", "MIN(sample_error) AS error", "MIN(sample) AS min", "MAX(sample) AS max", "AVG(sample::text::numeric)::double precision AS mean")
	if d.Kind == "box" {
		// Retain the exact sorted population: percentile_cont uses a+(b-a)*f,
		// which can move a Tukey fence by one ULP relative to the browser.
		stats = append(stats, "array_agg(sample ORDER BY sample) FILTER (WHERE sample IS NOT NULL) AS sorted_samples")
	}
	b.stage("SELECT " + strings.Join(stats, ",") + " FROM " + rows + by)
	if d.Kind == "box" {
		// PostgreSQL arrays are one-based; the browser position is (n-1)*p.
		// Cast constants/positions explicitly so every operation is IEEE double,
		// including (1-f)*a+f*b. Clamp b for singleton/integer positions.
		quartiles := "CASE WHEN samples>0 THEN ARRAY(SELECT (1::double precision-fraction)*sorted_samples[lower_index+1]+fraction*sorted_samples[LEAST(lower_index+2,samples)::integer] FROM (SELECT floor(position)::integer AS lower_index,position-floor(position) AS fraction,p FROM (SELECT (samples-1)::double precision*p AS position,p FROM unnest(ARRAY[0.25,0.5,0.75]::double precision[]) p) positions) fractions ORDER BY p) END"
		fields := append(append([]string{}, groupCols...), "count", "samples", "null_count", "error", "min", "max", "mean", quartiles+" AS quartiles")
		// Tukey endpoints use the already partitioned population, avoiding
		// correlated scans of all rows for every group. Never return this array.
		if d.Whiskers == "tukey" {
			fields = append(fields, "sorted_samples")
		}
		b.stage("SELECT " + strings.Join(fields, ",") + " FROM " + b.rel)
		// Observed endpoints/outliers inherit the sample guard; check every
		// interpolated quartile and the mean before exposing the summary.
		resultError := "COALESCE(error,CASE WHEN EXISTS (SELECT 1 FROM unnest(ARRAY[min,max,mean] || quartiles) result WHERE " + b.unsafeMetricNumber("result") + ") THEN 'unsafe_integer'::text END)"
		fields = append(append([]string{}, groupCols...), "count", "samples", "null_count", resultError+" AS error", "min", "max", "mean", "quartiles")
		if d.Whiskers == "tukey" {
			fields = append(fields, "sorted_samples")
		}
		b.stage("SELECT " + strings.Join(fields, ",") + " FROM " + b.rel)
	}
	summary := b.rel
	if d.Kind == "box" {
		whiskers := d.Whiskers
		if whiskers == "" {
			whiskers = "minmax"
		}
		lo, hi := "s.min", "s.max"
		outliers, count := "'[]'::jsonb", "0"
		from := summary + " s"
		if whiskers == "tukey" {
			lowFence := "s.quartiles[1]-1.5*(s.quartiles[3]-s.quartiles[1])"
			highFence := "s.quartiles[3]+1.5*(s.quartiles[3]-s.quartiles[1])"
			inside := "sample >= " + lowFence + " AND sample <= " + highFence
			// Each group's sorted samples are visited once for endpoints and tail
			// counts. Ordinal lookup then retains at most 20 outliers, including
			// both tails of the outlier population, without another population scan.
			from += " CROSS JOIN LATERAL (SELECT MIN(sample) FILTER (WHERE " + inside + ") AS low, MAX(sample) FILTER (WHERE " + inside + ") AS high, COUNT(*) FILTER (WHERE sample < " + lowFence + ") AS low_n, COUNT(*) FILTER (WHERE sample > " + highFence + ") AS high_n FROM unnest(s.sorted_samples) sample) tails"
			lo, hi = "tails.low", "tails.high"
			count = "(tails.low_n+tails.high_n)"
			rank := "CASE WHEN i<=10 THEN i ELSE " + count + "-LEAST(" + count + ",20)+i END"
			index := "CASE WHEN rank<=tails.low_n THEN rank ELSE s.samples-tails.high_n+rank-tails.low_n END"
			outliers = "(SELECT COALESCE(jsonb_agg(s.sorted_samples[(" + index + ")::integer] ORDER BY rank),'[]'::jsonb) FROM (SELECT " + rank + " AS rank FROM generate_series(1,LEAST(" + count + ",20)) i) ranks)"
		}
		value := "CASE WHEN s.error IS NULL THEN s.quartiles[2] END"
		payload := "CASE WHEN s.error IS NULL THEN jsonb_build_object('kind','box','summary',CASE WHEN s.samples>0 THEN jsonb_build_object('n',s.samples,'min',s.min,'q1',s.quartiles[1],'median',s.quartiles[2],'q3',s.quartiles[3],'max',s.max,'mean',s.mean,'low'," + lo + ",'high'," + hi + ",'outliers'," + outliers + ",'outlierCount'," + count + ",'whiskers'," + b.param(whiskers, "text") + ",'method','exact-linear') END) END"
		fields := []string{}
		for _, g := range groupCols {
			fields = append(fields, "s."+g)
		}
		fields = append(fields, "s.count", "s.samples", "s.null_count", "s.error", "s.min", "s.max", "s.mean", "s.quartiles", value+" AS value", payload+" AS distribution")
		b.stage("SELECT " + strings.Join(fields, ",") + " FROM " + from)
	} else {
		// Global extent and error inventory precede any group sort/limit.
		b.stage("SELECT MIN(sample) AS lo,MAX(sample) AS hi,MIN(sample_error) AS input_error FROM " + rows)
		extent := b.rel
		// Match browser double arithmetic, including division before multiplication
		// and exact endpoints; rounded decimal edges can move boundary samples.
		binParam := b.param(bins, "integer")
		b.stage("SELECT *, CASE WHEN lo IS NULL THEN ARRAY[]::double precision[] WHEN lo=hi THEN ARRAY[lo,hi] ELSE ARRAY(SELECT CASE WHEN i=0 THEN lo WHEN i=" + binParam + " THEN hi ELSE lo+(hi-lo)*(i::double precision/" + binParam + ") END FROM generate_series(0," + binParam + ") i ORDER BY i) END AS edges FROM " + extent)
		edges := b.rel
		b.stage("SELECT *, COALESCE(input_error,CASE WHEN EXISTS (SELECT 1 FROM unnest(edges) edge WHERE " + b.unsafeMetricNumber("edge") + ") THEN 'unsafe_integer'::text END,CASE WHEN lo<>hi AND EXISTS (SELECT 1 FROM generate_subscripts(edges,1) i WHERE i>1 AND edges[i]<=edges[i-1]) THEN 'histogram_precision'::text END) AS extent_error FROM " + edges)
		extent = b.rel
		// Assign each sample once using the exact emitted edge array. The final
		// endpoint belongs to the final bin, matching the browser's half-open bins.
		// This avoids rescanning the entire scoped population for every group/bin.
		binFields, binBy, countMatch := []string{}, []string{}, []string{"c.bin=i"}
		for _, g := range groupCols {
			binFields = append(binFields, "r."+g)
			binBy = append(binBy, "r."+g)
			countMatch = append(countMatch, "c."+g+" IS NOT DISTINCT FROM s."+g)
		}
		binFields = append(binFields, "CASE WHEN x.extent_error IS NULL AND r.sample IS NOT NULL THEN LEAST(width_bucket(r.sample,x.edges),cardinality(x.edges)-1) END AS bin", "COUNT(*) AS n")
		binBy = append(binBy, "bin")
		b.stage("SELECT " + strings.Join(binFields, ",") + " FROM " + rows + " r CROSS JOIN " + extent + " x WHERE r.sample IS NOT NULL AND x.extent_error IS NULL GROUP BY " + strings.Join(binBy, ","))
		binCounts := b.rel
		counts := "(SELECT COALESCE(jsonb_agg(COALESCE(c.n,0) ORDER BY i),'[]'::jsonb) FROM generate_series(1,cardinality(x.edges)-1) i LEFT JOIN " + binCounts + " c ON " + strings.Join(countMatch, " AND ") + ")"
		payload := "CASE WHEN x.extent_error IS NULL THEN jsonb_build_object('kind','histogram','edges',to_jsonb(x.edges),'counts'," + counts + ",'n',s.samples) END"
		// value uses sample count as the meaningful scalar histogram sort measure.
		fields := []string{}
		for _, g := range groupCols {
			fields = append(fields, "s."+g)
		}
		fields = append(fields, "s.count", "s.samples", "s.null_count", "COALESCE(x.extent_error,s.error) AS error", "CASE WHEN x.extent_error IS NULL THEN s.samples END AS value", payload+" AS distribution")
		b.stage("SELECT " + strings.Join(fields, ",") + " FROM " + summary + " s CROSS JOIN " + extent + " x")
	}
	order, e := metricOrder(spec, len(groups), false, true)
	if e != nil {
		return SQLPlan{}, e
	}
	sql := "SELECT *,COUNT(*) OVER () AS group_count FROM " + b.rel + order
	if spec.GroupLimit > 0 {
		sql += " LIMIT " + b.param(spec.GroupLimit, "integer")
	}
	return b.finish(sql, scope, nil), nil
}

// DistributionPrecision is the protocol method emitted by the box compiler.
const DistributionPrecision = "exact-linear"

// SQLPlan.String deliberately omits bound values and SQL source (which may
// contain host authorization details). Use SQL and Args explicitly to execute.
func (p SQLPlan) String() string {
	return fmt.Sprintf("%s plan %s (%d stages)", p.Profile, p.Fingerprint, len(p.Stages))
}
