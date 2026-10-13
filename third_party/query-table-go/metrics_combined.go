package querytable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CombinedMetricPlan is an optional PostgreSQL single-statement batch. Each
// output row contains metric_index, buckets (a JSONB array of the original SQL
// bucket rows), and group_count. Indexes refer to Metrics in request order;
// empty grouped populations still return an envelope with [] and group_count=0.
// Original plans retain their fingerprints and metadata. Hosts still enforce
// population, group, memory, response, and execution budgets in their snapshot.
type CombinedMetricPlan struct {
	SQLPlan
	Metrics []MetricSQLPlan
}

// CompileCombinedMetrics shares exact input DAG stages and fuses compatible
// scalar reductions (at most 128 expressions). Separate scopes/groupings retain
// separate reductions; distributions retain their existing exact stages. Every
// metric and revision/token/snapshot envelope validates before any SQL is run.
func CompileCombinedMetrics(ctx context.Context, q MetricQuery, s Schema, o PlanOptions) (CombinedMetricPlan, error) {
	if len(q.Metrics) == 0 {
		return CombinedMetricPlan{}, diagnostic("query_required", "metric request required")
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if resolver := o.Resolver; resolver != nil {
		cache := map[string]ComputedColumn{}
		o.Resolver = DefinitionResolverFunc(func(ctx context.Context, id string) (ComputedColumn, error) {
			if c, ok := cache[id]; ok {
				return c, nil
			}
			c, err := resolver.ResolveComputed(ctx, id)
			if err == nil {
				cache[id] = c
			}
			return c, err
		})
	}
	batch, err := CompileMetrics(ctx, q, s, o)
	if err != nil {
		return CombinedMetricPlan{}, err
	}
	return combinePostgresMetrics(ctx, batch, q, len(o.SourceArgs))
}

type combinedReduction struct {
	from        string
	expressions []string
	aliases     map[string]string
	members     map[int][]SQLiteReductionColumn
	first       int
}

func combinePostgresMetrics(ctx context.Context, batch MetricBatchPlan, q MetricQuery, sourceArgs int) (CombinedMetricPlan, error) {
	out := CombinedMetricPlan{Metrics: batch.Metrics, SQLPlan: SQLPlan{Profile: ServerExpressionProfile, Scope: "batch", RequiresSnapshot: true, ResolvedRevisions: map[string]string{}}}
	if len(batch.Metrics) == 0 {
		return out, diagnostic("query_required", "metric request required")
	}
	out.Args = append([]any{}, batch.Metrics[0].Args[:sourceArgs]...)
	// Do not shadow a trusted source table or binding with a new CTE name.
	namespace := ""
	for {
		collision := false
		for _, metric := range batch.Metrics {
			collision = collision || strings.Contains(metric.SQL, namespace+"qtb") || strings.Contains(metric.SQL, namespace+"qtf")
		}
		if !collision {
			break
		}
		namespace += "_"
	}
	parameterPool := map[string]int{}
	nodePool := map[string]int{}
	nodes := []SQLStage{}
	finals := []string{}
	groups := map[string]*combinedReduction{}
	dependencies := map[string]bool{}
	for _, p := range batch.Metrics {
		if err := ctx.Err(); err != nil {
			return CombinedMetricPlan{}, err
		}
		parameters := make([]int, len(p.Args))
		for i, arg := range p.Args {
			if i < sourceArgs {
				parameters[i] = i + 1
				continue
			}
			// Compiler-created values are finite scalar literals with stable Go types.
			// Host SourceArgs retain their reserved positions and are never interned.
			key := fmt.Sprintf("%T\x00%#v", arg, arg)
			if n, ok := parameterPool[key]; ok {
				parameters[i] = n
			} else {
				out.Args = append(out.Args, arg)
				parameters[i] = len(out.Args)
				parameterPool[key] = len(out.Args)
			}
		}
		relations := map[string]string{}
		for i, stage := range p.Stages {
			sql, err := rewritePostgresSQL(stage.SQL, parameters, relations)
			if err != nil {
				return CombinedMetricPlan{}, err
			}
			key := fmt.Sprintf("%t\x00%s", stage.Materialized, sql)
			node, ok := nodePool[key]
			if !ok {
				node = len(nodes)
				nodePool[key] = node
				nodes = append(nodes, SQLStage{Name: fmt.Sprintf("%sqtb%d", namespace, node), SQL: sql, Materialized: stage.Materialized})
			}
			relations[stage.Name] = nodes[node].Name
			if p.reduction != nil && p.reduction.stage == i {
				from, err := rewritePostgresSQL(p.reduction.from, parameters, relations)
				if err != nil {
					return CombinedMetricPlan{}, err
				}
				groupKey := p.Scope + "\x00" + from
				group := groups[groupKey]
				if group == nil {
					group = &combinedReduction{from: from, aliases: map[string]string{}, members: map[int][]SQLiteReductionColumn{}, first: node}
					groups[groupKey] = group
				}
				columns := []SQLiteReductionColumn{}
				for _, column := range p.reduction.columns {
					expression, err := rewritePostgresSQL(column.SQL, parameters, relations)
					if err != nil {
						return CombinedMetricPlan{}, err
					}
					columns = append(columns, SQLiteReductionColumn{Name: column.Name, SQL: expression})
					if _, ok := group.aliases[expression]; !ok {
						group.aliases[expression] = fmt.Sprintf("f%d", len(group.expressions))
						group.expressions = append(group.expressions, expression)
					}
				}
				group.members[node] = columns
			}
		}
		prefix := sqlStageCTEs(p.Stages) + "\n"
		if !strings.HasPrefix(p.SQL, prefix) {
			return CombinedMetricPlan{}, fmt.Errorf("invalid PostgreSQL compiler stage envelope")
		}
		final, err := rewritePostgresSQL(strings.TrimPrefix(p.SQL, prefix), parameters, relations)
		if err != nil {
			return CombinedMetricPlan{}, err
		}
		finals = append(finals, final)
		for _, dependency := range p.Dependencies {
			dependencies[dependency] = true
		}
		for id, revision := range p.ResolvedRevisions {
			if previous, ok := out.ResolvedRevisions[id]; ok && previous != revision {
				return CombinedMetricPlan{}, diagnostic("definition_changed", "inconsistent batch revisions")
			}
			out.ResolvedRevisions[id] = revision
		}
	}
	// Add each fused reduction before its first consumer. Its input relation
	// already exists there, so the original DAG remains in dependency order.
	starts := map[int]SQLStage{}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fusionNumber := 0
	for _, key := range keys {
		group := groups[key]
		if len(group.members) < 2 || len(group.expressions) > 128 {
			continue
		}
		name := fmt.Sprintf("%sqtf%d", namespace, fusionNumber)
		fusionNumber++
		projections := []string{}
		for _, expression := range group.expressions {
			projections = append(projections, expression+" AS "+group.aliases[expression])
		}
		starts[group.first] = SQLStage{Name: name, SQL: "SELECT " + strings.Join(projections, ",") + group.from, Materialized: true}
		for node, columns := range group.members {
			projections := []string{}
			for _, column := range columns {
				projections = append(projections, group.aliases[column.SQL]+" AS "+column.Name)
			}
			nodes[node].SQL = "SELECT " + strings.Join(projections, ",") + " FROM " + name
		}
	}
	for i, node := range nodes {
		if fused, ok := starts[i]; ok {
			out.Stages = append(out.Stages, fused)
		}
		out.Stages = append(out.Stages, node)
	}
	envelopes := []string{}
	for i, final := range finals {
		spec := q.Metrics[i]
		order, err := metricOrder(spec, len(spec.GroupBy), spec.ExpressionY != "", spec.Distribution != nil)
		if err != nil {
			return CombinedMetricPlan{}, err
		}
		order = strings.TrimPrefix(order, " ORDER BY ")
		envelopes = append(envelopes, fmt.Sprintf("SELECT %d AS metric_index,COALESCE(jsonb_agg(to_jsonb(r) ORDER BY %s),'[]'::jsonb) AS buckets,COALESCE(MAX(group_count),0)::bigint AS group_count FROM (%s) AS r", i, order, final))
	}
	out.SQL = sqlStageCTEs(out.Stages) + "\n" + strings.Join(envelopes, "\nUNION ALL\n") + "\nORDER BY metric_index"
	for dependency := range dependencies {
		out.Dependencies = append(out.Dependencies, dependency)
	}
	sort.Strings(out.Dependencies)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%#v", out.Profile, out.SQL, out.Args)
	for _, p := range out.Metrics {
		fmt.Fprintf(h, "\x00%s\x00%s", p.ID, p.Fingerprint)
	}
	out.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return out, nil
}
