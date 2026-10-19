package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
)

// Machine Time needs a small set of dimensions and additive measures. Collapse
// the wide tool cube before computing command identities, once per generation,
// instead of invoking JSON/classification functions on every cube row for each
// of the dashboard's metrics and comparison windows.
var machineTimeDimensions = fieldSet("repository_name", "provider", "model", "model_family", "model_effort", "effort", "day", "week", "month", "status", "operation_key", "operation_name", "activity", "tool_name", "mcp_server", "program", "subcommand", "tool_category", "command_category")

const machineTimeIndexSQL = `CREATE INDEX IF NOT EXISTS tool_call_cube_machine_time_idx ON tool_call_cube(
 repository_name,provider,model,effort,day,status,tool_name,mcp_server,program,subcommand,
 tool_category,command_category,duration_ms_sum,duration_ms_n,call_count)`

func (c *Catalog) machineTimeRows(ctx context.Context) ([]map[string]any, error) {
	rows, _, err := c.machineTimeQueryRows(ctx, false)
	return rows, err
}

func (c *Catalog) machineTimeQueryRows(ctx context.Context, requireFresh bool) ([]map[string]any, bool, error) {
	tx, release, err := beginQueryV2(ctx, c.reader())
	if err != nil {
		return nil, false, err
	}
	defer release()
	// Check freshness and read the publication in the same snapshot as the cube.
	// Timestamp queries previously read raw calls and must not reuse stale facts.
	if requireFresh {
		fresh, err := c.toolCubeFresh(ctx, tx)
		if err != nil || !fresh {
			return nil, false, err
		}
	}
	rows, err := c.machineTimeRowsIn(ctx, tx)
	return rows, true, err
}

func (c *Catalog) machineTimeRowsIn(ctx context.Context, tx *sql.Tx) ([]map[string]any, error) {
	publication, err := readMeta(tx, ctx, "tool_rollup_publication")
	if err != nil {
		return nil, err
	}
	key := "machine-time:" + time.Local.String() + ":" + c.clock().Format("2006-01-02") + ":" + publication
	rows, err := cachedValue(ctx, c, key, func(ctx context.Context) ([]map[string]any, error) {
		rows, err := queryMapsContext(ctx, tx, `SELECT repository_name,provider,model,effort,day,status,
 tool_name,mcp_server,program,subcommand,tool_category,command_category,
 SUM(duration_ms_sum) duration_ms,SUM(duration_ms_n) machine_timed,SUM(call_count) machine_calls
 FROM tool_call_cube GROUP BY repository_name,provider,model,effort,day,status,
 tool_name,mcp_server,program,subcommand,tool_category,command_category`)
		if err != nil {
			return nil, err
		}
		type operation struct{ key, name, activity string }
		type operationInputs struct{ tool, server, program, subcommand, toolCategory, commandCategory string }
		operations := map[operationInputs]operation{}
		calendar := map[string][2]string{}
		for _, row := range rows {
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
	// Only complete local calendar days can reuse additive cube facts.
	if (lower == "") != (upper == "") {
		return "", "", nil, false
	}
	return lower, upper, where, true
}

func (c *Catalog) machineTimeAggregations(ctx context.Context, request querytable.AggregationRequest, schema querytable.Schema) (querytable.AggregationResult, bool, error) {
	lower, upper, where, valid := machineTimeDayRange(request.Where)
	if !valid {
		return querytable.AggregationResult{}, false, nil
	}
	for _, field := range whereFields(where) {
		if !machineTimeDimensions.has(field) {
			return querytable.AggregationResult{}, false, nil
		}
	}
	for _, spec := range request.Aggregations {
		if !(spec.Op == "count" && (spec.Field == "" || spec.Field == "duration_ms") || spec.Op == "sum" && spec.Field == "duration_ms") {
			return querytable.AggregationResult{}, false, nil
		}
		for _, key := range spec.GroupBy {
			if !machineTimeDimensions.has(key) {
				return querytable.AggregationResult{}, false, nil
			}
		}
	}
	rows, handled, err := c.machineTimeQueryRows(ctx, lower != "")
	if err != nil {
		return querytable.AggregationResult{}, true, err
	}
	if !handled {
		return querytable.AggregationResult{}, false, nil
	}
	filtered, err := querytable.Filter(rows, where, schema)
	if err != nil {
		return querytable.AggregationResult{}, true, err
	}
	if lower != "" {
		kept := make([]map[string]any, 0, len(filtered))
		for _, row := range filtered {
			day := firstString(row["day"])
			if day >= lower && day < upper {
				kept = append(kept, row)
			}
		}
		filtered = kept
	}
	type total struct {
		keys         []any
		label        string
		calls, timed int64
		duration     float64
		hasDuration  bool
	}
	groupings := map[string][]*total{}
	out := querytable.AggregationResult{Metrics: []querytable.Metric{}}
	for _, spec := range request.Aggregations {
		if spec.ID == "" || len(spec.GroupBy) > 20 {
			return querytable.AggregationResult{}, true, fmt.Errorf("invalid aggregation ID or group fields")
		}
		signature, _ := json.Marshal(spec.GroupBy)
		groups, exists := groupings[string(signature)]
		if !exists {
			byKey := map[string]*total{}
			for _, row := range filtered {
				keys := make([]any, len(spec.GroupBy))
				for i, field := range spec.GroupBy {
					keys[i] = row[field]
				}
				encoded, _ := json.Marshal(keys)
				key := string(encoded)
				group := byKey[key]
				if group == nil {
					group = &total{keys: keys, label: fmt.Sprint(keys)}
					byKey[key] = group
					groups = append(groups, group)
				}
				group.calls += integer(row["machine_calls"])
				group.timed += integer(row["machine_timed"])
				if value, ok := number(row["duration_ms"]); ok {
					group.duration += value
					group.hasDuration = true
				}
			}
			if len(filtered) == 0 && len(spec.GroupBy) == 0 {
				groups = []*total{{keys: []any{}}}
			}
			groupings[string(signature)] = groups
		}
		valueFor := func(group *total) any {
			var value any
			if spec.Op == "count" {
				if spec.Field == "" {
					value = group.calls
				} else {
					value = group.timed
				}
			} else if group.hasDuration {
				value = group.duration
			}
			return value
		}
		ordered := slices.Clone(groups)
		sort.Slice(ordered, func(i, j int) bool {
			left, lok := number(valueFor(ordered[i]))
			right, rok := number(valueFor(ordered[j]))
			if lok != rok {
				return lok
			}
			if lok && rok && left != right {
				return left > right
			}
			return ordered[i].label < ordered[j].label
		})
		metric := querytable.Metric{ID: spec.ID, Buckets: make([]querytable.Bucket, 0, len(groups))}
		for _, group := range ordered {
			metric.Buckets = append(metric.Buckets, querytable.Bucket{Keys: slices.Clone(group.keys), Value: valueFor(group), Count: int(group.calls)})
		}
		if request.RequireComplete && len(metric.Buckets) > maxCompleteSQLBuckets {
			return querytable.AggregationResult{}, true, fmt.Errorf("complete aggregation exceeds %d groups; choose a shorter period or a single repository", maxCompleteSQLBuckets)
		}
		if !request.RequireComplete && len(metric.Buckets) > maxSQLBuckets {
			metric.Buckets = metric.Buckets[:maxSQLBuckets]
		}
		out.Metrics = append(out.Metrics, metric)
	}
	return out, true, nil
}
