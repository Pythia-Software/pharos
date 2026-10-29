package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
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

// The published Machine Time projection already holds these exact additive
// facts. Its freshness is checked in this read snapshot before replacing raw
// calls with weighted groups. The caller has validated integer domains/bounds.
func (c *Catalog) nativeMachineTimeGroups(ctx context.Context, tx *sql.Tx, d qt.QueryDatasetV3, q qt.QueryRequestV3, fields nativeFields) ([]nativeGroup, int64, bool, error) {
	if d.Dataset != "pharos_tool_calls" || slices.ContainsFunc(fields.names, func(field string) bool { return field != "duration_ms" }) || len(q.Grouping.KeyIntervals) > 0 || slices.ContainsFunc(q.Grouping.Keys, func(field string) bool { return !machineTimeDimensions.has(field) }) {
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
