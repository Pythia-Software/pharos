package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// A broad NOT CONTAINS can keep nearly the entire ledger. Start with the
// exact nonempty-command population in the fresh cube and subtract the union
// of positive substring matches. The same snapshot supplies both populations.
// Restrict this identity to conjunctions of exact indexed text exclusions;
// nested ORs and unsupported dimensions retain the ordinary executor.
func textComplementWhere(table sqlDataset, terms []qt.WhereTerm) (base, excluded []qt.WhereTerm, ok bool) {
	var matches []qt.WhereClause
	for _, term := range terms {
		if !term.IsGroup() && term.Negated && table.queryText.term(term.Predicates()[0]) && term.Field == "command" {
			p := term.Predicates()[0]
			p.Negated = false
			matches = append(matches, p)
		} else {
			base = append(base, term)
		}
	}
	if len(matches) == 0 {
		return nil, nil, false
	}
	excluded = append(slices.Clone(base), qt.WhereTerm{Any: matches})
	base = append(base, qt.WhereTerm{Field: "command", Op: "is_not_null"})
	return base, excluded, true
}

func (c *Catalog) nativeTextComplementGroups(ctx context.Context, tx *sql.Tx, table sqlDataset, schema qt.Schema, where []qt.WhereTerm, keys []string, fields nativeFields, now time.Time) ([]nativeGroup, int64, bool, error) {
	if table.pageFrom != "FROM tool_calls t" || slices.ContainsFunc(fields.names, func(field string) bool { return field != "duration_ms" }) {
		return nil, 0, false, nil
	}
	base, excluded, ok := textComplementWhere(table, where)
	if !ok {
		return nil, 0, false, nil
	}
	q := qt.QueryRequestV3{Source: qt.WireQuery{Where: base}, Grouping: &qt.GroupingDefinition{Keys: keys}}
	groups, population, used, err := c.nativeMachineTimeGroups(ctx, tx, qt.QueryDatasetV3{Dataset: "pharos_tool_calls", Schema: schema}, q, fields)
	if err != nil || !used {
		return nil, 0, used, err
	}
	residual := table.residualWhere(excluded)
	inputs := append(append(slices.Clone(keys), fields.names...), whereFields(residual)...)
	source, args := table.project(inputs).v2WindowedSource(schema, excluded, now)
	filter, err := qt.CompileSQLiteWhere(residual, schema, qt.SQLiteOptions{Now: now})
	if err != nil {
		return nil, 0, false, nil
	}
	predicate := ""
	if filter.SQL != "" {
		predicate = " WHERE " + numberQueryParameters(filter.SQL, len(args))
	}
	counted := map[string]bool{}
	for _, field := range fields.names {
		counted[field] = true
	}
	removed, removedPopulation, safe, err := nativeGroups(ctx, tx, source, predicate, append(args, filter.Args...), keys, fields.names, counted, false)
	if err != nil || !safe {
		return nil, 0, safe, err
	}
	byKey := map[string]int{}
	for i, group := range groups {
		key, _ := json.Marshal(group.keys)
		byKey[string(key)] = i
	}
	for _, group := range removed {
		if group.rows == 0 {
			continue
		}
		key, _ := json.Marshal(group.keys)
		i, exists := byKey[string(key)]
		if !exists || groups[i].rows < group.rows {
			return nil, 0, false, nil
		}
		kept := &groups[i]
		kept.rows -= group.rows
		for j, value := range group.values {
			kept.values[j].count -= value.count
			if kept.values[j].count == 0 {
				kept.values[j].sum = nil
			} else {
				kept.values[j].sum = integer(kept.values[j].sum) - integer(value.sum)
			}
		}
	}
	if len(keys) != 0 {
		groups = slices.DeleteFunc(groups, func(group nativeGroup) bool { return group.rows == 0 })
	}
	return groups, population - removedPopulation, true, nil
}

// nativeGroupedRows reduces ordinary integer sums/counts before the adapter
// stages a source. The adapter still owns group identity, result schema, HAVING,
// ordering, paging, and metrics. Each compact row is one complete source group;
// MAX of its single precomputed summary preserves that summary exactly.
// Unsupported types, formulas, computed definitions, and representatives retain
// the general executor. Storage and safe-integer checks precede every reduction.
func (s *Server) nativeGroupedRows(ctx context.Context, table sqlDataset, d qt.QueryDatasetV3, q qt.QueryRequestV3) ([]map[string]any, qt.QueryDatasetV3, qt.QueryRequestV3, int64, bool, error) {
	if !nativeMetrics || q.Grouping == nil || q.Grouping.Representative != nil || len(q.ExpectedRevisions) != 0 || len(q.Grouping.Diagnostics) != 0 {
		return nil, d, q, 0, false, nil
	}
	fields := nativeFields{counted: map[string]bool{}}
	for _, key := range q.Grouping.Keys {
		f, ok := d.Schema.Fields[key]
		if !ok || key == d.Schema.IDField || f.Kind != qt.FieldText && f.Kind != qt.FieldEnum {
			return nil, d, q, 0, false, nil
		}
	}
	for _, term := range q.Source.Where {
		for _, p := range term.Predicates() {
			if _, ok := d.Schema.Fields[p.Field]; !ok {
				return nil, d, q, 0, false, nil
			}
		}
	}
	for _, summary := range q.Grouping.Summaries {
		if summary.Expression != "" || summary.Op != "count" && summary.Op != "sum" && summary.Op != "avg" {
			return nil, d, q, 0, false, nil
		}
		if summary.Field != "" {
			f, ok := d.Schema.Fields[summary.Field]
			if !ok || f.Kind != qt.FieldNumber || !f.ExpressionNumeric {
				return nil, d, q, 0, false, nil
			}
			if !slices.Contains(fields.names, summary.Field) {
				fields.names = append(fields.names, summary.Field)
			}
			fields.counted[summary.Field] = true
		} else if summary.Op != "count" {
			return nil, d, q, 0, false, nil
		}
	}
	tx, err := s.Catalog.reader().BeginTx(ctx, nil)
	if err != nil {
		return nil, d, q, 0, true, err
	}
	defer tx.Rollback()
	options := qt.PlanOptions{Identity: queryTableScope + ":" + d.Schema.Name}
	if err = authorizeQueryV3Computed(ctx, tx, &d); err != nil {
		return nil, d, q, 0, true, err
	}
	options.Resolver = qt.SQLiteComputedDefinitionResolver{Tx: tx, Scope: d.Scope, Dataset: d.Dataset}
	// Validate the original request and all result aliases before accelerating it.
	if _, err = d.SourcePlan(ctx, q, options); err != nil {
		return nil, d, q, 0, true, err
	}
	inputs := append(slices.Clone(q.Grouping.Keys), fields.names...)
	inputs = append(inputs, whereFields(table.residualWhere(q.Source.Where))...)
	projected, _ := table.project(inputs).preferCoveringScan(q.Source.Where)
	d.SourceSQL, d.SourceArgs = projected.v2WindowedSource(d.Schema, q.Source.Where, time.UnixMilli(q.Now))
	filter, err := qt.CompileSQLiteWhere(table.residualWhere(q.Source.Where), d.Schema, qt.SQLiteOptions{Now: time.UnixMilli(q.Now)})
	if err != nil {
		return nil, d, q, 0, true, err
	}
	predicate := ""
	if filter.SQL != "" {
		predicate = " WHERE " + numberQueryParameters(filter.SQL, len(d.SourceArgs))
	}
	args := append(slices.Clone(d.SourceArgs), filter.Args...)
	countSQL := "SELECT COUNT(*) FROM (" + d.SourceSQL + ") AS r" + predicate
	_, safe, err := s.Catalog.nativeTableBounds(ctx, tx, table, d.Schema, fields.names, d.MaxPopulation, countSQL, args)
	if err != nil || !safe {
		return nil, d, q, 0, err != nil, err
	}
	groups, population, safe, err := s.Catalog.nativeTextComplementGroups(ctx, tx, table, d.Schema, q.Source.Where, q.Grouping.Keys, fields, time.UnixMilli(q.Now))
	if err == nil && !safe {
		groups, population, safe, err = s.Catalog.nativeMachineTimeGroups(ctx, tx, d, q, fields)
	}
	if err == nil && !safe {
		groups, population, safe, err = nativeGroups(ctx, tx, d.SourceSQL, predicate, args, q.Grouping.Keys, fields.names, fields.counted, false)
	}
	if err != nil || !safe {
		return nil, d, q, 0, err != nil, err
	}
	if population > int64(d.MaxPopulation) || len(groups) > d.MaxGroups {
		return nil, d, q, 0, true, fmt.Errorf("resource_limit: grouped population exceeds host budget")
	}
	compact := make([]map[string]any, 0, len(groups))
	schema := d.Schema
	schema.Fields = maps.Clone(schema.Fields)
	schema.DefaultSort = nil
	schema.TiebreakSort = nil
	grouping := *q.Grouping
	grouping.Summaries = slices.Clone(grouping.Summaries)
	for i := range grouping.Summaries {
		name := fmt.Sprintf("__pharos_summary_%d", i)
		if _, exists := schema.Fields[name]; exists {
			return nil, d, q, 0, false, nil
		}
		schema.Fields[name] = qt.FieldSpec{Name: name, Kind: qt.FieldNumber, Expr: "r." + sqlIdentifier(name), ExpressionNumeric: true}
		grouping.Summaries[i].Op = "max"
		grouping.Summaries[i].Field = name
	}
	for i, group := range groups {
		row := map[string]any{schema.IDField: fmt.Sprint(i)}
		for j, key := range q.Grouping.Keys {
			row[key] = group.keys[j]
		}
		for j, summary := range q.Grouping.Summaries {
			var value any = group.rows
			if summary.Field != "" {
				v := group.values[slices.Index(fields.names, summary.Field)]
				switch summary.Op {
				case "count":
					value = v.count
				case "sum":
					value = v.sum
				case "avg":
					value = nil
					if v.count > 0 {
						value = float64(integer(v.sum)) / float64(v.count)
					}
				}
			}
			row[fmt.Sprintf("__pharos_summary_%d", j)] = value
		}
		compact = append(compact, row)
	}
	d.Schema = schema
	q.Source.Where = nil
	q.Grouping = &grouping
	// No caller-owned request slices or schema maps were mutated.
	d.SourceSQL = ""
	d.SourceArgs = nil
	return compact, d, q, population, true, ctx.Err()
}

// The published Machine Time projection already holds these exact additive
// facts. Its freshness is checked in this read snapshot before replacing raw
// calls with weighted groups. The caller has validated integer domains/bounds.
func (c *Catalog) nativeMachineTimeGroups(ctx context.Context, tx *sql.Tx, d qt.QueryDatasetV3, q qt.QueryRequestV3, fields nativeFields) ([]nativeGroup, int64, bool, error) {
	if d.Dataset != "pharos_tool_calls" || slices.ContainsFunc(fields.names, func(field string) bool { return field != "duration_ms" }) || slices.ContainsFunc(q.Grouping.Keys, func(field string) bool { return !machineTimeDimensions.has(field) }) {
		return nil, 0, false, nil
	}
	lower, upper, where, valid := machineTimeDayRange(q.Source.Where)
	if !valid || !machineTimeFilterable(where, d.Schema) {
		return nil, 0, false, nil
	}
	fresh, err := c.toolCubeFresh(ctx, tx)
	if err != nil || !fresh {
		return nil, 0, false, err
	}
	rows, err := c.machineTimeRowsIn(ctx, tx)
	if err != nil {
		return nil, 0, false, err
	}
	kept := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		day := firstString(row["day"])
		if machineTimeWithinDay(day, lower, upper) && machineTimeMatches(row, where) {
			kept = append(kept, row)
		}
	}
	groups := []nativeGroup{}
	var population int64
	for _, group := range machineTimeGroups(kept, q.Grouping.Keys) {
		native := nativeGroup{keys: group.keys, rows: group.calls}
		for range fields.names {
			var sum any
			if group.durations {
				sum = int64(group.duration)
			}
			native.values = append(native.values, nativeValue{count: group.timed, sum: sum})
		}
		groups = append(groups, native)
		population += group.calls
	}
	return groups, population, true, nil
}

// Empty/populated synthetic sources use the same typed projection as other map
// datasets. All original group keys and summary aliases remain adapter-owned.
func nativeGroupFields(q qt.QueryRequestV3, id string) []string {
	fields := append([]string{id}, q.Grouping.Keys...)
	for _, summary := range q.Grouping.Summaries {
		fields = append(fields, summary.Field)
	}
	return slices.DeleteFunc(fields, func(name string) bool { return strings.TrimSpace(name) == "" })
}
