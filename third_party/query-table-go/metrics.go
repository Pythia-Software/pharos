package querytable

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MetricDisplayHint retains only the paired-mode validation hint; all visual settings are ignored.
type MetricDisplayHint struct {
	Kind string `json:"kind"`
}
type MetricSort struct {
	Key   string `json:"key"`
	Dir   string `json:"dir"`
	Nulls string `json:"nulls,omitempty"`
}
type MetricDistribution struct {
	Kind     string `json:"kind"`
	Input    string `json:"input"`
	Whiskers string `json:"whiskers,omitempty"`
	Bins     int    `json:"bins,omitempty"`
}

// MetricQuery is the v2 computation-only request. Presentation is intentionally
// ignored by the compiler. Diagnostics/residual filters must never be discarded.
type MetricQuery struct {
	Profile           string            `json:"profile,omitempty"`
	ExpectedRevisions map[string]string `json:"expectedRevisions,omitempty"`
	PlanToken         string            `json:"planToken,omitempty"`
	Snapshot          string            `json:"snapshot,omitempty"`
	Version           int               `json:"version"`
	Where             []WhereTerm       `json:"where"`
	OrderBy           []OrderBy         `json:"orderBy"`
	Limit             int               `json:"limit"`
	Offset            int               `json:"offset"`
	Metrics           []AggSpec         `json:"metrics"`
	Diagnostics       []any             `json:"diagnostics,omitempty"`
}
type MetricSQLPlan struct {
	ID string
	SQLPlan
}
type MetricBatchPlan struct {
	Metrics                []MetricSQLPlan
	RequiresSharedSnapshot bool
}

// CompileMetrics compiles every requested metric or returns an error. Each plan
// is one statement; execute the batch in one repeatable-read snapshot. X/Y are
// always reduced together using one grouped relation, never positionally joined.
func CompileMetrics(ctx context.Context, q MetricQuery, s Schema, o PlanOptions) (MetricBatchPlan, error) {
	return compileMetricsDialect(ctx, q, s, o, false)
}
func compileMetricsDialect(ctx context.Context, q MetricQuery, s Schema, o PlanOptions, sqlite bool) (MetricBatchPlan, error) {
	result := MetricBatchPlan{RequiresSharedSnapshot: true}
	var optionErr error
	o, optionErr = requestOptionsDialect(ctx, sqlite, q.Profile, q.PlanToken, q.Snapshot, q.ExpectedRevisions, o)
	if optionErr != nil {
		return result, optionErr
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if q.Version != 2 {
		return result, diagnostic("version", "metric protocol version must be 2")
	}
	if len(q.Diagnostics) > 0 {
		return result, diagnostic("residual_query", "query contains unresolved client diagnostics")
	}
	if err := (WireQuery{Where: q.Where, OrderBy: q.OrderBy, Limit: q.Limit, Offset: q.Offset, Aggregations: q.Metrics}).Validate(); err != nil {
		return result, err
	}
	ids := map[string]bool{}
	for _, spec := range q.Metrics {
		if ids[spec.ID] {
			return result, diagnostic("duplicate_metric", "duplicate metric ID")
		}
		ids[spec.ID] = true
		p, e := compileMetricDialect(ctx, q, spec, s, o, sqlite)
		if e != nil {
			return MetricBatchPlan{}, fmt.Errorf("metric %s: %w", spec.ID, e)
		}
		result.Metrics = append(result.Metrics, MetricSQLPlan{spec.ID, p})
	}
	return result, nil
}
func MetricExpression(spec AggSpec) string {
	if spec.Expression != "" {
		return spec.Expression
	}
	if spec.Field == "" && spec.Op == "count" {
		return "COUNT()"
	}
	return strings.ToUpper(spec.Op) + "([" + strings.ReplaceAll(spec.Field, "]", "]]") + "])"
}
func compileMetric(ctx context.Context, q MetricQuery, spec AggSpec, s Schema, o PlanOptions) (SQLPlan, error) {
	return compileMetricDialect(ctx, q, spec, s, o, false)
}
func compileMetricDialect(ctx context.Context, q MetricQuery, spec AggSpec, s Schema, o PlanOptions, sqlite bool) (SQLPlan, error) {
	if len(spec.Diagnostics) > 0 {
		return SQLPlan{}, diagnostic("invalid_metric", "metric contains unresolved diagnostics")
	}
	if spec.Display != nil && spec.Display.Kind == "scatter" && spec.ExpressionY == "" {
		return SQLPlan{}, diagnostic("paired_expression_required", "scatter requires both X and Y aggregate expressions")
	}
	b, e := newPlanDialect(ctx, s, o, sqlite)
	if e != nil {
		return SQLPlan{}, e
	}
	if e = b.filter(q.Where); e != nil {
		return SQLPlan{}, e
	}
	scope := spec.Scope
	if scope == "" {
		scope = "allMatching"
	}
	if scope != "allMatching" && scope != "shownRows" {
		return SQLPlan{}, diagnostic("scope", "invalid metric scope")
	}
	if spec.GroupLimit < 0 || spec.GroupLimit > 10000 || len(spec.Sort) > 20 {
		return SQLPlan{}, diagnostic("resource_limit", "invalid result limit/sort budget")
	}
	if scope == "shownRows" {
		if q.Limit <= 0 {
			return SQLPlan{}, diagnostic("window_required", "shownRows requires a positive limit")
		}
		order, err := b.order(q.OrderBy)
		if err != nil {
			return SQLPlan{}, err
		}
		b.stage("SELECT * FROM " + b.rel + " ORDER BY " + order + " LIMIT " + b.param(q.Limit, "bigint") + " OFFSET " + b.param(q.Offset, "bigint"))
	}
	groups := []ValueSQL{}
	for _, name := range spec.GroupBy {
		if strings.HasPrefix(name, "@computed/") && !o.ComputedGroupable[strings.TrimPrefix(name, "@computed/")] {
			return SQLPlan{}, diagnostic("group_disabled", "computed grouping requires host policy for "+name)
		}
		if f, ok := s.Fields[name]; ok && !fieldGroupable(f) {
			return SQLPlan{}, diagnostic("group_disabled", "grouping disabled for "+name)
		}
		v, err := b.field(name)
		if err != nil {
			return SQLPlan{}, err
		}
		groups = append(groups, b.key(v))
	}
	if spec.Distribution != nil {
		if b.sqlite {
			return b.sqliteDistribution(spec, groups, scope)
		}
		return b.distribution(spec, groups, scope)
	}
	n, e := parseExpression(MetricExpression(spec))
	if e != nil {
		return SQLPlan{}, e
	}
	var y *exprNode
	if spec.ExpressionY != "" {
		y, e = parseExpression(spec.ExpressionY)
		if e != nil {
			return SQLPlan{}, e
		}
	}
	leaves := map[*exprNode]ValueSQL{}
	reductionMemo := map[string]ValueSQL{}
	reductions := []string{}
	var collect func(*exprNode) error
	collect = func(n *exprNode) error {
		op := strings.ToLower(n.op)
		if aggOpKnown(op) || op == "median" {
			key := expressionKey(n)
			if cached, ok := reductionMemo[key]; ok {
				leaves[n] = cached
				return nil
			}
			if len(n.args) > 1 || (len(n.args) == 0 && op != "count") {
				return diagnostic("arity", "aggregate requires one row expression (COUNT also accepts none)")
			}
			raw := "*"
			errSQL := b.nullError()
			typ := "number"
			if len(n.args) == 1 {
				v, err := b.row(n.args[0])
				if err != nil {
					return err
				}
				if err = b.aggregatePolicy(n.args[0], op, map[string]bool{}); err != nil {
					return err
				}
				if op != "count" {
					v = b.metricNumber(v)
				}
				raw = v.Value
				errSQL = v.Error
				typ = v.Type
				if (op == "sum" || op == "avg" || op == "median") && typ != "number" && typ != "null" {
					return diagnostic("type_error", "numeric aggregate requires numeric input")
				}
				if (op == "min" || op == "max") && typ == "bool" {
					return diagnostic("unsupported_type", "boolean extrema are unsupported")
				}
			}
			value := ""
			errorExpr := ""
			if !b.sqlite {
				switch op {
				case "count":
					value = "COUNT(" + raw + ")::double precision"
					typ = "number"
				case "count_distinct":
					value = "COUNT(DISTINCT " + raw + ")::double precision"
					typ = "number"
				case "median":
					value = "percentile_cont(0.5) WITHIN GROUP (ORDER BY " + raw + ")"
					typ = "number"
				case "sum", "avg":
					value = strings.ToUpper(op) + "((" + raw + ")::text::numeric)"
					typ = "number"
				default:
					value = strings.ToUpper(op) + "(" + raw + ")"
				}
				errorExpr = "MIN(" + errSQL + ")"
				// A conservative absolute-sum bound declines populations whose
				// intermediate browser accumulation could exceed safe precision.
				if op == "sum" {
					errorExpr = "COALESCE(" + errorExpr + ",CASE WHEN SUM(abs((" + raw + ")::text::numeric))>" + b.param("9007199254740991", "numeric") + " THEN 'unsafe_integer'::text END)"
				}

				if typ == "number" {
					errorExpr = "COALESCE(" + errorExpr + ",CASE WHEN abs(" + value + ") > 1e100::numeric OR (abs(" + value + ")>0 AND abs(" + value + ")<1e-300::numeric) THEN 'numeric_range'::text END)"
					value = "CASE WHEN abs(" + value + ") <= 1e100::numeric AND ((" + value + ")=0 OR abs(" + value + ")>=1e-300::numeric) THEN (" + value + ")::double precision END"
				}
				if typ == "number" {
					errorExpr = "COALESCE(" + errorExpr + ",CASE WHEN " + b.unsafeMetricNumber(value) + " THEN 'unsafe_integer'::text END)"
				}

			}
			if b.sqlite {
				if op == "count" || op == "count_distinct" {
					typ = "number"
				}
				value, errorExpr = b.sqliteReduction(op, raw, errSQL, typ)
			}
			index := len(leaves)
			a := fmt.Sprintf("a%d", index)
			z := a + "_error"
			reductions = append(reductions, value+" AS "+a, errorExpr+" AS "+z)
			leaves[n] = ValueSQL{a, z, typ}
			reductionMemo[key] = leaves[n]
			return nil
		}
		if n.op == "field" {
			return diagnostic("aggregate_context", "row fields must occur inside an aggregate")
		}
		for _, a := range n.args {
			if err := collect(a); err != nil {
				return err
			}
		}
		return nil
	}
	if e = collect(n); e != nil {
		return SQLPlan{}, e
	}
	if len(leaves) == 0 {
		return SQLPlan{}, diagnostic("aggregate_required", "metric must contain an aggregate")
	}
	if y != nil {
		before := len(leaves)
		if e = collect(y); e != nil {
			return SQLPlan{}, e
		}
		if len(leaves) == before {
			return SQLPlan{}, diagnostic("aggregate_required", "paired expression must contain an aggregate")
		}
	}
	selectGroups, by := groupSQL(groups)
	reductions = append(selectGroups, append(reductions, "COUNT(*) AS count")...)
	reductionFrom := " FROM " + b.rel + by
	b.stage("SELECT " + strings.Join(reductions, ",") + reductionFrom)
	reductionStage := len(b.stages) - 1
	var lower func(*exprNode) (ValueSQL, error)
	lower = func(n *exprNode) (ValueSQL, error) {
		if v, ok := leaves[n]; ok {
			return b.emit("CASE WHEN "+v.Error+" IS NULL THEN "+v.Value+" END", v.Error, v.Type), nil
		}
		if n.op == "literal" {
			return b.row(n)
		}
		a := []ValueSQL{}
		for _, arg := range n.args {
			v, err := lower(arg)
			if err != nil {
				return v, err
			}
			a = append(a, v)
		}
		return b.operation(n.op, a)
	}
	v, e := lower(n)
	if e != nil {
		return SQLPlan{}, e
	}
	v = b.metricNumber(v)
	var yv ValueSQL
	if y != nil {
		yv, e = lower(y)
		if e != nil {
			return SQLPlan{}, e
		}
		yv = b.metricNumber(yv)
		if e = requireType(v, "number"); e != nil {
			return SQLPlan{}, e
		}
		if e = requireType(yv, "number"); e != nil {
			return SQLPlan{}, e
		}
	}
	groupErrors := []string{}
	for i := range groups {
		groupErrors = append(groupErrors, fmt.Sprintf("group%d_error", i))
	}
	if len(groupErrors) > 0 {
		ge := "COALESCE(" + strings.Join(append(groupErrors, b.nullError()), ",") + ")"
		v = b.emit("CASE WHEN "+ge+" IS NULL THEN "+v.Value+" END", "COALESCE("+ge+","+v.Error+")", v.Type)
		if y != nil {
			yv = b.emit("CASE WHEN "+ge+" IS NULL THEN "+yv.Value+" END", "COALESCE("+ge+","+yv.Error+")", yv.Type)
		}
	}
	fields := groupOutput(len(groups))
	fields = append(fields, v.Value+" AS value", v.Error+" AS error", "count")
	if y != nil {
		fields = append(fields, yv.Value+" AS y", yv.Error+" AS y_error")
	}
	b.stage("SELECT " + strings.Join(fields, ",") + " FROM " + b.rel)
	order, e := metricOrder(spec, len(groups), y != nil, false)
	if e != nil {
		return SQLPlan{}, e
	}
	sql := "SELECT *, COUNT(*) OVER () AS group_count FROM " + b.rel + order
	if spec.GroupLimit > 0 {
		sql += " LIMIT " + b.param(spec.GroupLimit, "integer")
	}
	cols := []OutputColumn{}
	if b.sqlite {
		for i, g := range groups {
			cols = append(cols, OutputColumn{Field: spec.GroupBy[i], ValueAlias: fmt.Sprintf("group%d", i), Type: g.Type})
		}
		cols = append(cols, OutputColumn{ValueAlias: "value", Type: v.Type})
		if y != nil {
			cols = append(cols, OutputColumn{ValueAlias: "y", Type: yv.Type})
		}
	}
	plan := b.finish(sql, scope, cols)
	if b.sqlite {
		plan.SQLiteReductionStage = reductionStage
		plan.SQLiteReductionFrom = reductionFrom
		for _, projection := range reductions {
			split := strings.LastIndex(projection, " AS ") // Every output has a compiler-owned alias.
			plan.SQLiteReductionColumns = append(plan.SQLiteReductionColumns, SQLiteReductionColumn{Name: projection[split+4:], SQL: projection[:split]})
		}
	}
	return plan, nil
}
func groupSQL(groups []ValueSQL) ([]string, string) {
	sels := []string{}
	by := []string{}
	for i, v := range groups {
		a := "group" + strconv.Itoa(i)
		sels = append(sels, v.Value+" AS "+a, v.Error+" AS "+a+"_error")
		by = append(by, v.Value, v.Error)
	}
	if len(by) == 0 {
		return sels, ""
	}
	return sels, " GROUP BY " + strings.Join(by, ",")
}
func groupOutput(n int) []string {
	out := []string{}
	for i := 0; i < n; i++ {
		a := "group" + strconv.Itoa(i)
		out = append(out, a, a+"_error")
	}
	return out
}
func metricOrder(spec AggSpec, groups int, paired, distribution bool) (string, error) {
	terms := []string{"(error IS NOT NULL) ASC"}
	if paired {
		terms[0] = "(error IS NOT NULL OR y_error IS NOT NULL) ASC"
	}
	if len(spec.Sort) == 0 {
		spec.Sort = []MetricSort{{Key: "value", Dir: "desc"}}
	}
	seen := map[string]bool{}
	for _, s := range spec.Sort {
		valid := s.Key == "value" || s.Key == "count" || s.Key == "y" && paired || s.Key == "samples" && distribution
		for i := 0; i < groups; i++ {
			valid = valid || s.Key == "group"+strconv.Itoa(i)
		}
		if !valid || s.Dir != "asc" && s.Dir != "desc" {
			return "", diagnostic("metric_sort", "invalid metric sort key or direction")
		}
		t, e := orderTerm(s.Key, s.Dir, s.Nulls)
		if e != nil {
			return "", e
		}
		terms = append(terms, t)
		seen[s.Key] = true
	}
	for i := 0; i < groups; i++ {
		k := "group" + strconv.Itoa(i)
		if !seen[k] {
			terms = append(terms, k+" ASC NULLS LAST")
		}
		terms = append(terms, k+"_error ASC NULLS LAST")
	}
	if len(terms) == 0 {
		return "", nil
	}
	return " ORDER BY " + strings.Join(terms, ","), nil
}
func (b *planBuilder) aggregatePolicy(n *exprNode, op string, seen map[string]bool) error {
	if n.op == "field" {
		if strings.HasPrefix(n.text, "@computed/") {
			id := strings.TrimPrefix(n.text, "@computed/")
			if seen[id] {
				return nil
			}
			seen[id] = true
			c, ok := b.definitions[id]
			if !ok {
				return diagnostic("definition_invalid", "unresolved computed dependency")
			}
			parsed, e := parseExpression(c.Expression.Source)
			if e != nil {
				return e
			}
			return b.aggregatePolicy(parsed, op, seen)
		}
		f, ok := b.schema.Fields[n.text]
		if !ok {
			return diagnostic("unknown_field", n.text)
		}
		if f.AggregateOps != nil {
			if (op == "box" || op == "histogram") && len(f.AggregateOps) > 0 {
				return nil
			}
			allowed := false
			for _, a := range f.AggregateOps {
				allowed = allowed || a == op
			}
			if !allowed {
				return diagnostic("aggregate_disabled", "aggregate "+op+" disabled for "+n.text)
			}
		}
	}
	for _, a := range n.args {
		if e := b.aggregatePolicy(a, op, seen); e != nil {
			return e
		}
	}
	return nil
}

func fieldGroupable(f FieldSpec) bool {
	if f.Groupable != nil {
		return *f.Groupable
	}
	return f.Kind == FieldText || f.Kind == FieldEnum || f.Kind == FieldBool
}

// unsafeMetricNumber checks the IEEE double exposed to the client, not a
// decimal rendering (which can round near MAX_SAFE_INTEGER). Callers supply
// values already bounded by the PostgreSQL arithmetic profile.
func (b *planBuilder) unsafeMetricNumber(value string) string {
	if b.sqlite {
		return "qt_v2_unsafe(" + value + ")"
	}
	v := "(" + value + ")::double precision"
	return "(trunc(" + v + ")=" + v + " AND abs(" + v + ")>" + b.param(float64(9007199254740991), "double precision") + ")"
}

// metricNumber is deliberately separate from row arithmetic: COUNT observes
// presence, and only consumed metric sample/reduction/result boundaries reject
// unsafe integers. Keeping an error column preserves lazy group expressions.
func (b *planBuilder) metricNumber(v ValueSQL) ValueSQL {
	if v.Type != "number" {
		return v
	}
	err := "COALESCE(" + v.Error + ",CASE WHEN " + b.unsafeMetricNumber(v.Value) + " THEN " + b.errorLiteral("unsafe_integer") + " END)"
	return b.emit("CASE WHEN "+err+" IS NULL THEN "+v.Value+" END", err, v.Type)
}
