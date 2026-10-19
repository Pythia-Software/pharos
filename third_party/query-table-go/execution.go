package querytable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// ExecutionPlan binds the union of row and metric definition revisions. The
// caller must submit identical filters/order/windows in Rows and Metrics when
// they represent one table view. Different SQL statements still require a
// shared database snapshot; Fingerprint is an identity, not an authorization.
type ExecutionPlan struct {
	Rows              SQLPlan
	Metrics           MetricBatchPlan
	ResolvedRevisions map[string]string
	Fingerprint       string
}

func CompileExecution(ctx context.Context, rows WireQuery, metrics MetricQuery, s Schema, o PlanOptions) (ExecutionPlan, error) {
	return compileExecutionDialect(ctx, rows, metrics, s, o, false)
}

// CompileSQLiteExecution shares one clock and resolver cache across row/metric
// plans. The host executes them in the resolver's SQLite transaction; use
// SQLiteV2Dataset.ExecuteV2In for bounded execution and wire-format results.
func CompileSQLiteExecution(ctx context.Context, rows WireQuery, metrics MetricQuery, s Schema, o PlanOptions) (ExecutionPlan, error) {
	return compileExecutionDialect(ctx, rows, metrics, s, o, true)
}
func compileExecutionDialect(ctx context.Context, rows WireQuery, metrics MetricQuery, s Schema, o PlanOptions, sqlite bool) (ExecutionPlan, error) {
	if sqlite && metrics.ExpectedRevisions != nil {
		o.ExpectedRevisions = metrics.ExpectedRevisions
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	original := o.Resolver
	cache := map[string]ComputedColumn{}
	if original != nil {
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
	}
	var out ExecutionPlan
	var err error
	out.Rows, err = compileComputedRowsDialect(ctx, rows, s, o, sqlite)
	if err != nil {
		return ExecutionPlan{}, err
	}
	out.Metrics, err = compileMetricsDialect(ctx, metrics, s, o, sqlite)
	if err != nil {
		return ExecutionPlan{}, err
	}
	out.ResolvedRevisions = map[string]string{}
	for id, c := range cache {
		out.ResolvedRevisions[id] = c.Revision
	}
	h := sha256.New()
	fmt.Fprint(h, out.Rows.Fingerprint)
	for _, p := range out.Metrics.Metrics {
		fmt.Fprint(h, "\x00", p.ID, "\x00", p.Fingerprint)
	}
	out.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return out, nil
}
