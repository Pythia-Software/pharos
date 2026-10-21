package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

// Machine Time needs a small set of dimensions and additive measures. Collapse
// the wide tool cube before computing command identities, once per generation,
// instead of invoking JSON/classification functions on every cube row for each
// of the dashboard's metrics and comparison windows.
var machineTimeDimensions = fieldSet("repository_name", "provider", "model", "model_family", "model_effort", "effort", "day", "week", "month", "status", "operation_key", "operation_name", "activity", "tool_name", "mcp_server", "program", "subcommand", "tool_category", "command_category")

const machineTimeIndexSQL = `CREATE INDEX IF NOT EXISTS tool_call_cube_machine_time_command_idx ON tool_call_cube(
 repository_name,provider,model,effort,day,status,tool_name,mcp_server,program,subcommand,
 tool_category,command_category,present_command,duration_ms_sum,duration_ms_n,call_count);
 DROP INDEX IF EXISTS tool_call_cube_machine_time_idx`

func (c *Catalog) machineTimeRowsIn(ctx context.Context, tx *sql.Tx) ([]map[string]any, error) {
	publication, err := readMeta(tx, ctx, "tool_rollup_publication")
	if err != nil {
		return nil, err
	}
	key := "machine-time:" + time.Local.String() + ":" + c.clock().Format("2006-01-02") + ":" + publication
	rows, err := cachedValue(ctx, c, key, func(ctx context.Context) ([]map[string]any, error) {
		rows, err := queryMapsContext(ctx, tx, `SELECT repository_name,provider,model,effort,day,status,
 tool_name,mcp_server,program,subcommand,tool_category,command_category,present_command,
 SUM(duration_ms_sum) duration_ms,SUM(duration_ms_n) machine_timed,SUM(call_count) machine_calls
 FROM tool_call_cube GROUP BY repository_name,provider,model,effort,day,status,
 tool_name,mcp_server,program,subcommand,tool_category,command_category,present_command`)
		if err != nil {
			return nil, err
		}
		type operation struct{ key, name, activity string }
		type operationInputs struct{ tool, server, program, subcommand, toolCategory, commandCategory string }
		operations := map[operationInputs]operation{}
		calendar := map[string][2]string{}
		for _, row := range rows {
			if integer(row["present_command"]) != 0 {
				row["command"] = "present"
			}
			// Match the SQLite functions' raw string inputs. firstString trims
			// whitespace and treats the literal "null" as missing, which would
			// merge distinct recorded command families.
			text := func(field string) string { value, _ := row[field].(string); return value }
			tool, server := text("tool_name"), text("mcp_server")
			program, subcommand := text("program"), text("subcommand")
			toolCategory, commandCategory := text("tool_category"), text("command_category")
			identity := operationInputs{tool, server, program, subcommand, toolCategory, commandCategory}
			op, ok := operations[identity]
			if !ok {
				op = operation{machineOperationKey(tool, server, program, subcommand), machineOperationName(tool, server, program, subcommand), machineActivity(toolCategory, commandCategory, tool, server, program)}
				operations[identity] = op
			}
			row["operation_key"], row["operation_name"], row["activity"] = op.key, op.name, op.activity
			model, effort := text("model"), text("effort")
			row["model_family"], row["model_effort"] = modelFamily(model), modelEffort(model, effort)
			if strings.TrimSpace(model) == "" {
				row["model_family"] = nil
			}
			dayKey := firstString(row["day"])
			buckets, known := calendar[dayKey]
			if !known {
				if day, err := time.Parse("2006-01-02", dayKey); err == nil {
					buckets = [2]string{day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)).Format("2006-01-02"), day.Format("2006-01")}
				}
				calendar[dayKey] = buckets
			}
			if buckets[0] != "" {
				row["week"], row["month"] = buckets[0], buckets[1]
			}
		}
		return rows, nil
	})
	return rows, err
}

func machineTimeDayRange(terms []querytable.WhereTerm) (lower, upper string, where []querytable.WhereTerm, valid bool) {
	where = make([]querytable.WhereTerm, 0, len(terms))
	lower, upper = "", ""
	for _, term := range terms {
		if term.Field != "started_at" {
			where = append(where, term)
			continue
		}
		at, valid := parseTime(term.Value)
		local := at.In(time.Local)
		if !valid || term.IsGroup() || term.Negated || (term.Op != ">=" && term.Op != "<") || local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 || local.Nanosecond() != 0 {
			return "", "", nil, false
		}
		day := local.Format("2006-01-02")
		if term.Op == ">=" {
			if lower == "" || day > lower {
				lower = day
			}
		} else if upper == "" || day < upper {
			upper = day
		}
	}
	return lower, upper, where, true
}

func machineTimeWithinDay(day, lower, upper string) bool {
	if lower == "" && upper == "" {
		return true
	}
	return day != "" && (lower == "" || day >= lower) && (upper == "" || day < upper)
}

// machineTimeMaxGroups bounds the groups in one metric the Machine Time
// accelerator answers: enough for every repository × operation, or for
// repository × operation × day timelines over a month. Beyond it the metric
// fails rather than returning part of the cube. Tests lower it.
var machineTimeMaxGroups = 50_000

// machineTimeMetricsV2 answers a Tool calls /metrics request from the Machine
// Time projection, read in the request's own transaction, when every metric
// counts calls, counts timed calls or sums duration_ms, and every group and
// filter field is one of the projection's dimensions. Machine Time and its
// home cards ask for cubes like these over months of calls, which the generic
// executor would read call by call against its 250,000-call and 2,000-group
// budgets. It reports false for a request it cannot answer exactly as the
// generic executor would, which then runs there.
func (c *Catalog) machineTimeMetricsV2(ctx context.Context, tx *sql.Tx, d qt.SQLiteV2Dataset, q qt.MetricQuery, o qt.PlanOptions) (qt.SQLiteV2ExecutionResult, bool, error) {
	if d.Dataset != "pharos_tool_calls" || len(q.Metrics) == 0 {
		return qt.SQLiteV2ExecutionResult{}, false, nil
	}
	lower, upper, where, valid := machineTimeDayRange(q.Where)
	if !valid || !machineTimeFilterable(where, d.Schema) {
		return qt.SQLiteV2ExecutionResult{}, false, nil
	}
	for _, spec := range q.Metrics {
		if !machineTimeMetric(spec) {
			return qt.SQLiteV2ExecutionResult{}, false, nil
		}
	}
	if lower != "" || upper != "" {
		// A timestamp range otherwise reads raw calls: check in this snapshot
		// that the cube holds every call before answering from it.
		fresh, err := c.toolCubeFresh(ctx, tx)
		if err != nil || !fresh {
			return qt.SQLiteV2ExecutionResult{}, err != nil, err
		}
	}
	// Run the ordinary adapter over an empty source first, as countOnlyMetricsV2
	// does: it validates the request and its revisions, and returns each
	// metric's result envelope or error.
	validation := d
	validation.SourceSQL = (sqlDataset{}).v2Source(d.Schema) + " WHERE 0"
	validation.SourceArgs = nil
	result, err := validation.ExecuteV2In(ctx, tx, nil, &q, o)
	if err != nil {
		return result, true, err
	}
	rows, err := c.machineTimeRowsIn(ctx, tx)
	if err != nil {
		return result, true, err
	}
	kept := make([]map[string]any, 0, len(rows))
	var population int64
	for _, row := range rows {
		if !machineTimeWithinDay(firstString(row["day"]), lower, upper) {
			continue
		}
		if machineTimeMatches(row, where) {
			kept = append(kept, row)
			population += integer(row["machine_calls"])
		}
	}
	groupings := map[string][]*machineTimeGroup{}
	for i, spec := range q.Metrics {
		metric := &result.Metrics.Metrics[i]
		if metric.Error != "" {
			continue
		}
		signature, _ := json.Marshal(spec.GroupBy)
		groups, ok := groupings[string(signature)]
		if !ok {
			groups = machineTimeGroups(kept, spec.GroupBy)
			groupings[string(signature)] = groups
		}
		if len(groups) > machineTimeMaxGroups {
			*metric = qt.SQLiteMetricV2{ID: metric.ID, Scope: metric.Scope, Buckets: []qt.SQLiteMetricV2Bucket{}, Error: "group population exceeds the " + thousands(machineTimeMaxGroups) + "-group host budget"}
			continue
		}
		buckets := make([]qt.SQLiteMetricV2Bucket, 0, len(groups))
		for _, group := range groups {
			bucket := qt.SQLiteMetricV2Bucket{Keys: slices.Clone(group.keys), Count: group.calls}
			switch {
			case spec.Op == "count" && spec.Field == "":
				bucket.Value = float64(group.calls)
			case spec.Op == "count":
				bucket.Value = float64(group.timed)
			case group.durations && math.Abs(group.duration) > 1<<53-1:
				bucket.Error = "unsafe_integer"
			case group.durations:
				bucket.Value = group.duration
			}
			buckets = append(buckets, bucket)
		}
		sortMetricBuckets(buckets)
		metric.Buckets, metric.GroupCount, metric.ProcessedRows, metric.Coverage = buckets, int64(len(buckets)), population, "exact"
	}
	return result, true, nil
}

// machineTimeMetric reports whether the accelerator answers spec: a plain
// count of calls, count of timed calls or sum of durations over all matching
// calls, grouped by projection dimensions.
func machineTimeMetric(spec qt.AggSpec) bool {
	plain := spec.Op == "count" && (spec.Field == "" || spec.Field == "duration_ms") || spec.Op == "sum" && spec.Field == "duration_ms"
	if !plain || spec.Expression != "" || spec.ExpressionY != "" || spec.Distribution != nil || spec.DateGrouping != nil || len(spec.Sort) > 0 ||
		spec.GroupLimit != 0 || spec.MinCount != 0 || len(spec.Where) > 0 || len(spec.Diagnostics) > 0 || spec.Scope != "" && spec.Scope != "allMatching" {
		return false
	}
	for _, field := range spec.GroupBy {
		if !machineTimeDimensions.has(field) {
			return false
		}
	}
	return true
}

// machineTimeFilterable reports whether machineTimeMatches evaluates every
// predicate exactly as the SQLite compiler does: equality, inequality and
// nullity on a text or enum dimension.
func machineTimeFilterable(where []qt.WhereTerm, schema qt.Schema) bool {
	for _, term := range where {
		for _, clause := range term.Predicates() {
			field, ok := schema.Fields[clause.Field]
			presence := clause.Field == "command" && (clause.Op == "is_null" || clause.Op == "is_not_null")
			if !ok || !machineTimeDimensions.has(clause.Field) && !presence || field.Kind != qt.FieldText && field.Kind != qt.FieldEnum ||
				!slices.Contains([]string{"=", "!=", "is_null", "is_not_null"}, clause.Op) {
				return false
			}
		}
	}
	return true
}

// machineTimeMatches applies where with the SQLite compiler's text semantics:
// = and != read NULL as the empty string, is_null also matches the empty
// string, and a negated predicate never matches a null value.
func machineTimeMatches(row map[string]any, where []qt.WhereTerm) bool {
	for _, term := range where {
		matched := false
		for _, clause := range term.Predicates() {
			text, _ := row[clause.Field].(string)
			null := text == ""
			var match bool
			switch clause.Op {
			case "=":
				match = text == clause.Value
			case "!=":
				match = text != clause.Value
			case "is_null":
				match = null
			case "is_not_null":
				match = !null
			}
			if clause.Negated {
				match = !match && !null
			}
			matched = matched || match
		}
		if !matched {
			return false
		}
	}
	return true
}

type machineTimeGroup struct {
	keys         []any
	calls, timed int64
	duration     float64
	durations    bool // some call in the group has a duration
}

// machineTimeGroups sums the projection's rows into groups by fields. With no
// fields there is one group, however few rows match.
func machineTimeGroups(rows []map[string]any, fields []string) []*machineTimeGroup {
	groups := []*machineTimeGroup{}
	byKey := map[string]*machineTimeGroup{}
	if len(fields) == 0 {
		groups = append(groups, &machineTimeGroup{keys: []any{}})
		byKey["[]"] = groups[0]
	}
	for _, row := range rows {
		keys := make([]any, len(fields))
		for i, field := range fields {
			keys[i] = row[field]
		}
		encoded, _ := json.Marshal(keys)
		group := byKey[string(encoded)]
		if group == nil {
			group = &machineTimeGroup{keys: keys}
			byKey[string(encoded)] = group
			groups = append(groups, group)
		}
		group.calls += integer(row["machine_calls"])
		group.timed += integer(row["machine_timed"])
		if value, ok := number(row["duration_ms"]); ok {
			group.duration += value
			group.durations = true
		}
	}
	return groups
}
