package querytable

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Cache identical scalar reductions; fuse different reductions with exactly
// matching validated inputs and grouping into one population scan. Distributions
// retain their own streaming and sampling behavior.
// All plans and revision envelopes have already validated. The temporary tables
// contain bounded grouped results, belong to this execution, and are dropped
// before returning the caller's transaction. No cache crosses snapshots.
type sqliteReductionCache struct {
	shared  map[string]bool
	fusions map[string]*sqliteReductionFusion
	tables  map[string]string
}

var sqliteReductionSequence atomic.Uint64

func sqliteStageCTEs(stages []SQLStage) string {
	ctes := make([]string, 0, len(stages))
	for _, s := range stages {
		mode := "MATERIALIZED"
		if !s.Materialized {
			mode = "NOT MATERIALIZED"
		}
		ctes = append(ctes, s.Name+" AS "+mode+" ("+s.SQL+")")
	}
	return "WITH " + strings.Join(ctes, ",\n")
}

func sqliteReductionKey(p MetricSQLPlan) string { return sqliteStageKey(p, p.SQLiteReductionStage) }
func sqliteStageKey(p MetricSQLPlan, i int) string {
	if i <= 0 || i >= len(p.Stages) {
		return ""
	}
	prefix := sqliteStageCTEs(p.Stages[:i+1])
	// Prefixes use numbered parameters. Ignore literals and quoted identifiers;
	// later formula constants must not prevent reuse of an identical reduction.
	n := 0
	var quote byte
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(prefix) && prefix[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		if c != '?' {
			continue
		}
		index := 0
		for i+1 < len(prefix) && prefix[i+1] >= '0' && prefix[i+1] <= '9' {
			i++
			index = index*10 + int(prefix[i]-'0')
		}
		if index > n {
			n = index
		}
	}
	if n > len(p.Args) {
		return ""
	}
	return fmt.Sprintf("%s\x00%#v", prefix, p.Args[:n])
}

type sqliteReductionFusion struct {
	expressions []string
	aliases     map[string]string
	reductions  map[string]bool
}

func sqliteFusionKey(p MetricSQLPlan) string {
	rowKey := sqliteStageKey(p, p.SQLiteReductionStage-1)
	if rowKey == "" || len(p.SQLiteReductionColumns) == 0 {
		return ""
	}
	return rowKey + "\x00" + p.SQLiteReductionFrom
}
func newSQLiteReductionCache(batch MetricBatchPlan) *sqliteReductionCache {
	c := &sqliteReductionCache{shared: map[string]bool{}, fusions: map[string]*sqliteReductionFusion{}, tables: map[string]string{}}
	first := map[string]string{}
	for _, p := range batch.Metrics {
		key := sqliteReductionKey(p)
		if key == "" {
			continue
		}
		if previous, ok := first[key]; ok && previous != p.Fingerprint {
			c.shared[key] = true
		} else {
			first[key] = p.Fingerprint
		}
		fusionKey := sqliteFusionKey(p)
		if fusionKey == "" {
			continue
		}
		f := c.fusions[fusionKey]
		if f == nil {
			f = &sqliteReductionFusion{aliases: map[string]string{}, reductions: map[string]bool{}}
			c.fusions[fusionKey] = f
		}
		f.reductions[key] = true
		for _, col := range p.SQLiteReductionColumns {
			if _, ok := f.aliases[col.SQL]; !ok {
				f.aliases[col.SQL] = fmt.Sprintf("f%d", len(f.expressions))
				f.expressions = append(f.expressions, col.SQL)
			}
		}
	}
	return c
}
func (c *sqliteReductionCache) plan(ctx context.Context, tx *sql.Tx, p MetricSQLPlan) (MetricSQLPlan, error) {
	key := sqliteReductionKey(p)
	if key == "" {
		return p, nil
	}
	fusionKey := sqliteFusionKey(p)
	if f := c.fusions[fusionKey]; f != nil && len(f.reductions) > 1 && len(f.expressions) <= 128 {
		tableKey := "fusion\x00" + fusionKey
		table, ok := c.tables[tableKey]
		if !ok {
			table = fmt.Sprintf("_qt_reduction_%d", sqliteReductionSequence.Add(1))
			projections := make([]string, 0, len(f.expressions))
			for _, expr := range f.expressions {
				projections = append(projections, expr+" AS "+f.aliases[expr])
			}
			statement := "CREATE TEMP TABLE " + table + " AS " + sqliteStageCTEs(p.Stages[:p.SQLiteReductionStage]) + "\nSELECT " + strings.Join(projections, ",") + p.SQLiteReductionFrom
			if _, err := tx.ExecContext(ctx, statement, p.Args...); err != nil {
				return p, err
			}
			c.tables[tableKey] = table
		}
		projections := make([]string, 0, len(p.SQLiteReductionColumns))
		for _, col := range p.SQLiteReductionColumns {
			projections = append(projections, f.aliases[col.SQL]+" AS "+col.Name)
		}
		return sqliteReplaceStage(p, p.SQLiteReductionStage, "SELECT "+strings.Join(projections, ",")+" FROM "+table)
	}
	if c.shared[key] {
		return c.reuseStage(ctx, tx, p, p.SQLiteReductionStage, "reduction\x00"+key)
	}
	return p, nil
}
func (c *sqliteReductionCache) reuseStage(ctx context.Context, tx *sql.Tx, p MetricSQLPlan, i int, key string) (MetricSQLPlan, error) {
	table, ok := c.tables[key]
	if !ok {
		table = fmt.Sprintf("_qt_reduction_%d", sqliteReductionSequence.Add(1))
		statement := "CREATE TEMP TABLE " + table + " AS " + sqliteStageCTEs(p.Stages[:i+1]) + "\nSELECT * FROM " + p.Stages[i].Name
		if _, err := tx.ExecContext(ctx, statement, p.Args...); err != nil {
			return p, err
		}
		c.tables[key] = table
	}
	return sqliteReplaceStage(p, i, "SELECT * FROM "+table)
}
func sqliteReplaceStage(p MetricSQLPlan, i int, statement string) (MetricSQLPlan, error) {
	prefix := sqliteStageCTEs(p.Stages) + "\n"
	if !strings.HasPrefix(p.SQL, prefix) {
		return p, fmt.Errorf("invalid compiler stage envelope")
	}
	final := strings.TrimPrefix(p.SQL, prefix)
	p.Stages = append([]SQLStage{}, p.Stages...)
	p.Stages[i].SQL = statement
	// Unreferenced earlier CTEs preserve numbered bindings without evaluating
	// the source. SQLite permits unused positional arguments to these plans.
	p.SQL = sqliteStageCTEs(p.Stages) + "\n" + final
	return p, nil
}
func (c *sqliteReductionCache) close(tx *sql.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, table := range c.tables {
		_, _ = tx.ExecContext(ctx, "DROP TABLE "+table)
	}
}
