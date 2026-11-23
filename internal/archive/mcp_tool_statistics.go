package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Tool-call reports aggregate the raw ledger, rather than conversation-level
// metrics: a conversation can span models and days. SQL reduces calls to
// histogram cells before they enter memory. Canonical conversation selection
// keeps native-only child conversations while suppressing linked copies.
func (c *Catalog) queryToolStatistics(args map[string]any) (any, error) {
	allowed := map[string]bool{}
	for _, key := range []string{"entity", "result_type", "group_by", "provider", "executor", "model", "project", "from", "to", "workspace_ids", "limit", "max_output_tokens"} {
		allowed[key] = true
	}
	for key, value := range args {
		if !allowed[key] {
			return nil, fmt.Errorf("tool_call statistics does not support %q", key)
		}
		if value == nil {
			return nil, fmt.Errorf("%s must not be null", key)
		}
	}
	if value := firstString(args["result_type"]); value != "" && value != "aggregate" {
		return nil, fmt.Errorf("tool_call statistics requires result_type=aggregate")
	}
	for _, key := range []string{"entity", "result_type", "provider", "executor", "model", "project", "from", "to"} {
		if args[key] != nil {
			if _, ok := args[key].(string); !ok {
				return nil, fmt.Errorf("%s must be a string", key)
			}
		}
	}
	limit, budget := 100, 4000
	for _, bound := range []struct {
		key    string
		target *int
	}{{"limit", &limit}, {"max_output_tokens", &budget}} {
		if raw := args[bound.key]; raw != nil {
			value, ok := number(raw)
			if !ok || value != float64(int(value)) {
				return nil, fmt.Errorf("%s must be an integer", bound.key)
			}
			*bound.target = int(value)
		}
	}
	if limit < 1 || limit > 500 || budget < 200 || budget > 8000 {
		return nil, fmt.Errorf("limit must be 1..500 and max_output_tokens 200..8000")
	}
	dimensions := map[string]string{"provider": "t.provider", "executor": "CASE WHEN t.provider='antigravity' THEN 'agy' ELSE t.provider END", "model": "NULLIF(t.model,'')", "project": projectNameSQL("w"), "day": "date(t.started_at,'localtime')"}
	groupBy := []string{}
	if raw := args["group_by"]; raw != nil {
		var items []any
		if err := json.Unmarshal([]byte(jsonText(raw)), &items); err != nil {
			return nil, fmt.Errorf("group_by must be an array")
		}
		for _, value := range items {
			name, ok := value.(string)
			if !ok || dimensions[name] == "" || containsString(groupBy, name) {
				return nil, fmt.Errorf("group_by requires distinct provider/executor, model, project or day dimensions")
			}
			groupBy = append(groupBy, name)
		}
		if len(groupBy) > 4 {
			return nil, fmt.Errorf("tool_call group_by supports at most four dimensions")
		}
	}
	where, values := []string{currentGroupConversation("c")}, []any{}
	for _, key := range []string{"provider", "executor", "model"} {
		if value := firstString(args[key]); value != "" {
			where = append(where, dimensions[key]+"=?")
			values = append(values, value)
		}
	}
	if project := firstString(args["project"]); project != "" {
		where = append(where, projectPredicate("w"))
		values = append(values, projectArgs(project)...)
	}
	for _, bound := range []struct {
		key, op string
		end     bool
	}{{"from", ">=", false}, {"to", "<=", true}} {
		if value := firstString(args[bound.key]); value != "" {
			if _, ok := parseTime(timeBound(value, bound.end)); !ok {
				return nil, fmt.Errorf("invalid %s time", bound.key)
			}
			where = append(where, "t.started_at"+bound.op+"?")
			values = append(values, timeBound(value, bound.end))
		}
	}
	if raw := args["workspace_ids"]; raw != nil {
		var ids []any
		if err := json.Unmarshal([]byte(jsonText(raw)), &ids); err != nil {
			return nil, fmt.Errorf("workspace_ids must be an array")
		}
		if len(ids) == 0 || len(ids) > 100 {
			return nil, fmt.Errorf("workspace_ids requires 1..100 IDs")
		}
		for _, id := range ids {
			if s, ok := id.(string); !ok || s == "" {
				return nil, fmt.Errorf("workspace_ids requires nonempty strings")
			}
		}
		where = append(where, "t.workspace_id IN (SELECT value FROM json_each(?))")
		values = append(values, jsonText(ids))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := c.reader().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	from := ` FROM tool_calls t JOIN conversations c ON c.id=t.conversation_id JOIN workspaces w ON w.id=t.workspace_id ` + projectJoin("w") + ` LEFT JOIN tool_ledger_state s ON s.conversation_id=c.id WHERE ` + strings.Join(where, " AND ")
	current := `s.version=` + toolLedgerVersionSQL("c", "w")
	// Choose canonical conversations before applying per-call model/date filters.
	// Suppressing whole mirrored workspaces would also erase native-only children.
	populationWhere, populationValues := []string{currentGroupConversation("c")}, []any{}
	for _, key := range []string{"provider", "executor"} {
		if value := firstString(args[key]); value != "" {
			if key == "executor" && value == "agy" {
				value = "antigravity"
			}
			populationWhere = append(populationWhere, "c.provider=?")
			populationValues = append(populationValues, value)
		}
	}
	if project := firstString(args["project"]); project != "" {
		populationWhere = append(populationWhere, projectPredicate("w"))
		populationValues = append(populationValues, projectArgs(project)...)
	}
	if args["workspace_ids"] != nil {
		populationWhere = append(populationWhere, "c.workspace_id IN (SELECT value FROM json_each(?))")
		populationValues = append(populationValues, jsonText(args["workspace_ids"]))
	}
	population, err := queryMapsContext(ctx, tx, `SELECT c.id,c.workspace_id,w.source_kind source,c.coverage,CASE WHEN `+current+` THEN 0 ELSE 1 END pending FROM conversations c JOIN workspaces w ON w.id=c.workspace_id `+projectJoin("w")+` LEFT JOIN tool_ledger_state s ON s.conversation_id=c.id WHERE `+strings.Join(populationWhere, " AND ")+` LIMIT ?`, append(populationValues, statisticsRowCap+1)...)
	if err != nil {
		return nil, err
	}
	if len(population) > statisticsRowCap {
		return nil, fmt.Errorf("tool statistics population exceeds %d conversations; narrow filters", statisticsRowCap)
	}
	candidates := make([]statisticsRow, 0, len(population))
	for _, row := range population {
		candidates = append(candidates, statisticsRow{ID: firstString(row["id"]), Workspace: firstString(row["workspace_id"]), Meta: row, Metrics: map[string]statMetricValue{}})
	}
	links, err := queryMapsContext(ctx, tx, `SELECT left_id,right_id FROM conversation_identity_links`)
	if err != nil {
		return nil, err
	}
	canonical := canonicalStatisticsRows(candidates, links, "conversation")
	ids := make([]string, 0, len(canonical))
	pending, partial := 0, 0
	for _, row := range canonical {
		ids = append(ids, row.ID)
		if integer(row.Meta["pending"]) != 0 {
			pending++
		}
		if row.Meta["coverage"] != "complete" {
			partial++
		}
	}
	from += ` AND c.id IN (SELECT value FROM json_each(?))`
	values = append(values, jsonText(ids))
	selects, groups := []string{}, []string{}
	for _, name := range groupBy {
		selects = append(selects, dimensions[name]+` AS "`+name+`"`)
		groups = append(groups, dimensions[name])
		if name == "project" {
			groups = append(groups, projectIDSQL("w"))
			selects = append(selects, projectIDSQL("w")+` AS project_id`)
		}
	}
	selects = append(selects, `t.exit_code,COUNT(*) tool_call_count,SUM(t.status='error') failed_tool_call_count,SUM(t.status='no_result') no_result_tool_call_count`)
	groups = append(groups, "t.exit_code")
	rows, err := queryMapsContext(ctx, tx, `SELECT `+strings.Join(selects, ",")+from+` AND `+current+` GROUP BY `+strings.Join(groups, ",")+` LIMIT 10001`, values...)
	if err != nil {
		return nil, err
	}
	if len(rows) > 10000 {
		return nil, fmt.Errorf("tool histogram exceeds 10000 cells; narrow filters")
	}
	type summary struct {
		key                              map[string]any
		calls, failed, unknown, noResult int64
		histogram                        []map[string]any
	}
	byKey := map[string]*summary{}
	for _, row := range rows {
		key := map[string]any{}
		for _, name := range groupBy {
			key[name] = row[name]
			if name == "project" {
				key["project_id"] = row["project_id"]
			}
		}
		encoded := jsonText(key)
		value := byKey[encoded]
		if value == nil {
			value = &summary{key: key, histogram: []map[string]any{}}
			byKey[encoded] = value
		}
		calls, failed := integer(row["tool_call_count"]), integer(row["failed_tool_call_count"])
		value.calls += calls
		value.failed += failed
		value.noResult += integer(row["no_result_tool_call_count"])
		if row["exit_code"] == nil {
			value.unknown += calls
		}
		value.histogram = append(value.histogram, map[string]any{"exit_code": row["exit_code"], "tool_call_count": calls, "failed_tool_call_count": failed})
	}
	if len(rows) == 0 && len(groupBy) == 0 {
		byKey["{}"] = &summary{key: map[string]any{}, histogram: []map[string]any{}}
	}
	if len(byKey) > limit {
		return nil, fmt.Errorf("tool statistics has %d groups, exceeding limit=%d; raise limit (up to 500) or narrow filters", len(byKey), limit)
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	output := []map[string]any{}
	for _, key := range keys {
		v := byKey[key]
		sort.Slice(v.histogram, func(i, j int) bool {
			a, b := v.histogram[i]["exit_code"], v.histogram[j]["exit_code"]
			if a == nil {
				return b != nil
			}
			if b == nil {
				return false
			}
			return integer(a) < integer(b)
		})
		output = append(output, map[string]any{"key": v.key, "tool_call_count": v.calls, "failed_tool_call_count": v.failed, "unknown_exit_code_count": v.unknown, "no_result_tool_call_count": v.noResult, "exit_code_histogram": v.histogram})
	}
	result := map[string]any{"entity": "tool_call", "groups": output, "total_groups": len(output), "population_complete": pending == 0, "coverage": map[string]any{"pending_conversations": pending, "partial_conversations": partial, "note": "Current retained tool calls only; linked conversation copies counted once; native-only children retained. Null exit codes are unknown, including successful non-command tools. Stale/missing ledgers are excluded (pending count conservatively ignores date/model filters); missing source history is not recoverable here. Day uses the call timestamp in local time; untimed calls have null day. Executor agy corresponds to provider antigravity."}}
	if len(jsonText(result)) > budget*3 {
		return nil, fmt.Errorf("tool statistics exceeds max_output_tokens; increase it or narrow filters")
	}
	return result, nil
}
