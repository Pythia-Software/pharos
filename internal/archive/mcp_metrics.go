package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

type metricDefinition struct {
	Name       string `json:"name"`
	Unit       string `json:"unit"`
	Definition string `json:"definition"`
}

var metricDefinitions = []metricDefinition{
	{"cost_usd", "USD", "Historical API list-price equivalent; not a bill. Complete requires priced usage; assumed/partial costs require available quality."},
	{"total_tokens", "tokens", "Reconciled consumption, including aggregate remainders; checkpoints and requests are not added twice."},
	{"input_tokens", "tokens", "Reconciled input including cached input; aggregate-only breakdowns remain partial."},
	{"output_tokens", "tokens", "Reconciled output including reported reasoning; reasoning is not added twice."},
	{"cache_read_input_tokens", "tokens", "Cache-read portion of input; absence of reported usage is unknown."},
	{"user_turn_count", "count", "Retained user messages of kind message, including harness-generated prompts."},
	{"tool_call_count", "count", "Classified ledger calls including delegations; compound commands count once."},
	{"tool_error_count", "count", "Classified ledger status error, not textual mentions or overall task outcome."},
	{"compaction_count", "count", "Attributed Claude boundaries and Codex triggers, excluding replayed events."},
	{"wall_clock_span_seconds", "seconds", "Recorded start-to-end span including idle time; unfinished intervals are unknown."},
}

var metricAliases = map[string]string{"token_count": "total_tokens", "turn_count": "user_turn_count", "tool_use_count": "tool_call_count", "wall_clock_span": "wall_clock_span_seconds"}

func metricName(name string) (string, error) {
	if alias := metricAliases[name]; alias != "" {
		name = alias
	}
	for _, d := range metricDefinitions {
		if d.Name == name {
			return name, nil
		}
	}
	names := []string{}
	for _, d := range metricDefinitions {
		names = append(names, d.Name)
	}
	return "", fmt.Errorf("unknown metric %q; supported: %s", name, strings.Join(names, ", "))
}

type statMetricValue struct {
	Value       *float64 `json:"value,omitempty"`
	Status      string   `json:"status"`
	PriceStatus string   `json:"price_status,omitempty"`
}

func metricNumber(v float64) statMetricValue { return statMetricValue{Value: &v, Status: "complete"} }
func (m statMetricValue) effective(quality string) any {
	if m.Value == nil || m.Status == "pending" || m.Status == "unavailable" || m.Status == "unattributable" {
		return nil
	}
	if m.Status != "complete" && quality != "available" {
		return nil
	}
	return *m.Value
}

type metricFilter struct {
	Name  string   `json:"name"`
	Op    string   `json:"op"`
	Value *float64 `json:"value,omitempty"`
}
type metricOrder struct {
	Metric    string `json:"metric"`
	Direction string `json:"direction"`
}
type metricAggregation struct {
	ID     string `json:"id"`
	Op     string `json:"op"`
	Metric string `json:"metric,omitempty"`
}
type statisticsRequest struct {
	Entity          string              `json:"entity"`
	Scope           string              `json:"metric_scope"`
	Quality         string              `json:"metric_quality"`
	Mode            string              `json:"result_mode"`
	Type            string              `json:"result_type"`
	Select          []string            `json:"select"`
	Filters         []metricFilter      `json:"metric_filters"`
	Order           *metricOrder        `json:"order_by,omitempty"`
	Aggregations    []metricAggregation `json:"aggregations,omitempty"`
	GroupBy         []string            `json:"group_by,omitempty"`
	WorkspaceIDs    []string            `json:"workspace_ids,omitempty"`
	Query           string              `json:"query,omitempty"`
	Repository      string              `json:"repository,omitempty"`
	Owner           string              `json:"owner,omitempty"`
	Flavor          string              `json:"flavor,omitempty"`
	Version         string              `json:"version,omitempty"`
	Outcome         string              `json:"outcome,omitempty"`
	Error           string              `json:"error,omitempty"`
	Source          string              `json:"source,omitempty"`
	Provider        string              `json:"provider,omitempty"`
	Model           string              `json:"model,omitempty"`
	Effort          string              `json:"effort,omitempty"`
	File            string              `json:"file,omitempty"`
	PR              *int                `json:"pr,omitempty"`
	From            string              `json:"from,omitempty"`
	To              string              `json:"to,omitempty"`
	Limit           int                 `json:"limit"`
	Budget          int                 `json:"max_output_tokens"`
	Offset          int                 `json:"offset,omitempty"`
	Cursor          string              `json:"cursor,omitempty"`
	SnapshotID      string              `json:"snapshot_id,omitempty"`
	RetainSnapshot  bool                `json:"retain_snapshot,omitempty"`
	ReleaseSnapshot bool                `json:"release_snapshot,omitempty"`
	Metrics         []string            `json:"-"`
}

func metricCall(args map[string]any) bool {
	for _, key := range []string{"metric_filters", "metric_scope", "metric_quality", "order_by", "select", "result_mode", "cursor", "snapshot_id", "retain_snapshot", "workspace_ids"} {
		if _, ok := args[key]; ok {
			return true
		}
	}
	return false
}

func parseMetricRequest(tool string, args map[string]any) (statisticsRequest, error) {
	r := statisticsRequest{}
	if tool == "query_statistics" {
		for _, toolSchema := range mcpTools {
			if firstString(toolSchema["name"]) != tool {
				continue
			}
			properties := toolSchema["inputSchema"].(map[string]any)["properties"].(map[string]any)
			for key, value := range args {
				if properties[key] == nil {
					return r, fmt.Errorf("unsupported statistics argument %q", key)
				}
				if value == nil {
					return r, fmt.Errorf("%s must not be null", key)
				}
			}
		}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("invalid statistics arguments: %w", err)
	}
	for _, field := range []struct {
		key    string
		target any
	}{{"metric_filters", &r.Filters}, {"order_by", &r.Order}, {"aggregations", &r.Aggregations}} {
		if value, ok := args[field.key]; ok {
			payload, e := json.Marshal(value)
			if e != nil {
				return r, e
			}
			decoder := json.NewDecoder(bytes.NewReader(payload))
			decoder.DisallowUnknownFields()
			if e = decoder.Decode(field.target); e != nil {
				return r, fmt.Errorf("invalid %s: %w", field.key, e)
			}
		}
	}
	if tool == "search_conversations" {
		r.Entity = "conversation"
	} else if tool == "search_work" {
		r.Entity = "workspace"
	}
	if r.Entity == "" {
		r.Entity = "workspace"
	}
	if r.Entity != "conversation" && r.Entity != "workspace" {
		return r, fmt.Errorf("entity must be conversation or workspace")
	}
	if r.Scope == "" {
		r.Scope = r.Entity
	}
	if r.Scope != "conversation" && r.Scope != "workspace" || r.Entity == "workspace" && r.Scope != "workspace" {
		return r, fmt.Errorf("workspace entities require workspace metric_scope; conversation entities support conversation or workspace")
	}
	if r.Quality == "" {
		r.Quality = "complete"
	}
	if r.Quality != "complete" && r.Quality != "available" {
		return r, fmt.Errorf("metric_quality must be complete or available")
	}
	if r.Mode == "" {
		r.Mode = "cards"
	}
	if tool == "query_statistics" {
		if r.Mode != "metadata" && args["result_mode"] != nil {
			return r, fmt.Errorf("query_statistics always uses metadata mode")
		}
		r.Mode = "metadata"
	}
	if r.Mode != "cards" && r.Mode != "metadata" {
		return r, fmt.Errorf("result_mode must be cards or metadata")
	}
	if r.Type == "" {
		r.Type = "rows"
	}
	if r.Type != "rows" && r.Type != "aggregate" {
		return r, fmt.Errorf("result_type must be rows or aggregate")
	}
	if tool != "query_statistics" && r.Type != "rows" {
		return r, fmt.Errorf("use query_statistics for aggregates")
	}
	if r.Type == "aggregate" && r.Scope != r.Entity {
		return r, fmt.Errorf("aggregate workspace metrics with entity=workspace to avoid repeated workspace totals")
	}
	maxLimit, defaultLimit, maxBudget, defaultBudget := 500, 100, 8000, 4000
	if r.Mode == "cards" {
		maxLimit, defaultLimit, maxBudget, defaultBudget = 100, 8, 4000, 1200
		if r.Entity == "conversation" {
			maxLimit = 20
		} else {
			defaultLimit = 50
		}
	}
	if args["limit"] == nil {
		r.Limit = defaultLimit
	}
	if args["max_output_tokens"] == nil {
		r.Budget = defaultBudget
	}
	if r.Limit < 1 || r.Limit > maxLimit || r.Budget < 200 || r.Budget > maxBudget || r.Offset < 0 {
		return r, fmt.Errorf("limit must be 1..%d, max_output_tokens 200..%d, and offset nonnegative", maxLimit, maxBudget)
	}
	if r.Mode == "metadata" && args["offset"] != nil {
		return r, fmt.Errorf("metadata uses cursor paging, not offset")
	}
	if r.Mode == "cards" && (r.Cursor != "" || r.SnapshotID != "" || r.RetainSnapshot) {
		return r, fmt.Errorf("snapshot/cursor paging requires metadata mode")
	}
	if len(r.Filters) > 8 || len(r.Select) > 20 || len(r.Aggregations) > 8 || len(r.WorkspaceIDs) > 100 || len(r.GroupBy) > 2 {
		return r, fmt.Errorf("maximum: 8 filters, 20 selected fields, 8 aggregations, 100 workspace IDs, 2 group dimensions")
	}
	for _, id := range r.WorkspaceIDs {
		if id == "" {
			return r, fmt.Errorf("workspace_ids must contain nonempty handles")
		}
	}
	if args["metric"] != nil && len(r.Filters) > 0 {
		return r, fmt.Errorf("do not combine legacy metric bounds and metric_filters")
	}
	if args["metric"] != nil {
		name, e := metricName(firstString(args["metric"]))
		if e != nil {
			return r, e
		}
		for _, bound := range []struct{ key, op string }{{"minimum", "gte"}, {"maximum", "lte"}} {
			if args[bound.key] != nil {
				n, ok := number(args[bound.key])
				if !ok {
					return r, fmt.Errorf("%s must be numeric", bound.key)
				}
				r.Filters = append(r.Filters, metricFilter{name, bound.op, &n})
			}
		}
		if len(r.Filters) == 0 {
			r.Filters = append(r.Filters, metricFilter{Name: name, Op: "is_present"})
		}
	}
	if r.Filters == nil {
		r.Filters = []metricFilter{}
	}
	needed := map[string]bool{}
	for i := range r.Filters {
		f := &r.Filters[i]
		f.Name, err = metricName(f.Name)
		if err != nil {
			return r, err
		}
		needed[f.Name] = true
		switch f.Op {
		case "is_missing", "is_present":
			if f.Value != nil {
				return r, fmt.Errorf("presence predicates omit value")
			}
		case "gt", "gte", "lt", "lte", "eq":
			if f.Value == nil || math.IsNaN(*f.Value) || math.IsInf(*f.Value, 0) {
				return r, fmt.Errorf("numeric predicate requires a finite value")
			}
			if strings.HasSuffix(f.Name, "_count") && math.Trunc(*f.Value) != *f.Value {
				return r, fmt.Errorf("count thresholds must be integers")
			}
		default:
			return r, fmt.Errorf("unsupported metric operator %q", f.Op)
		}
	}
	if err = validateMetricRanges(r.Filters); err != nil {
		return r, err
	}
	if r.Order != nil {
		r.Order.Metric, err = metricName(r.Order.Metric)
		if err != nil {
			return r, err
		}
		needed[r.Order.Metric] = true
		if r.Order.Direction == "" {
			r.Order.Direction = "desc"
		}
		if r.Order.Direction != "asc" && r.Order.Direction != "desc" {
			return r, fmt.Errorf("direction must be asc or desc")
		}
	}
	selected := map[string]bool{}
	for _, field := range r.Select {
		if name, e := metricName(field); e == nil {
			selected[name] = true
			needed[name] = true
			continue
		}
		switch field {
		case "workspace_id", "conversation_id", "conversation_ids", "conversation_count", "linked_conversation_ids", "mirrored_workspace_ids", "repository", "source", "model", "provider", "effort", "started_at", "activity_at", "coverage":
			if r.Entity == "workspace" && (field == "conversation_id" || field == "linked_conversation_ids" || field == "model" || field == "provider" || field == "effort" || field == "started_at" || field == "coverage") {
				return r, fmt.Errorf("field %s requires conversation entities", field)
			}
			if r.Entity == "conversation" && (field == "conversation_ids" || field == "conversation_count" || field == "mirrored_workspace_ids") {
				return r, fmt.Errorf("field %s requires workspace entities", field)
			}
			selected[field] = true
		default:
			return r, fmt.Errorf("unsupported metadata field %q", field)
		}
	}
	ids := map[string]bool{}
	for i := range r.Aggregations {
		a := &r.Aggregations[i]
		if a.ID == "" || len(a.ID) > 80 || ids[a.ID] {
			return r, fmt.Errorf("aggregation IDs must be unique nonempty strings up to 80 characters")
		}
		ids[a.ID] = true
		switch a.Op {
		case "count", "sum", "avg", "min", "max":
		default:
			return r, fmt.Errorf("unsupported aggregate %q", a.Op)
		}
		if a.Metric == "" && a.Op != "count" {
			return r, fmt.Errorf("aggregate %s requires a metric", a.Op)
		}
		if a.Metric != "" {
			a.Metric, err = metricName(a.Metric)
			if err != nil {
				return r, err
			}
			needed[a.Metric] = true
		}
	}
	if r.Type == "aggregate" && len(r.Aggregations) == 0 {
		return r, fmt.Errorf("aggregations are required for aggregate results")
	}
	if r.Type == "rows" && (len(r.Aggregations) > 0 || len(r.GroupBy) > 0) {
		return r, fmt.Errorf("aggregations/group_by require result_type=aggregate")
	}
	groups := map[string]bool{}
	for _, g := range r.GroupBy {
		if (g != "repository" && g != "source") || groups[g] {
			return r, fmt.Errorf("group_by supports unique repository/source dimensions")
		}
		groups[g] = true
	}
	for _, f := range r.Filters {
		selected[f.Name] = true
	}
	if r.Order != nil {
		selected[r.Order.Metric] = true
	}
	r.Select = []string{}
	for k := range selected {
		r.Select = append(r.Select, k)
	}
	sort.Strings(r.Select)
	for k := range needed {
		r.Metrics = append(r.Metrics, k)
	}
	sort.Strings(r.Metrics)
	r.Query = strings.TrimSpace(r.Query)
	return r, nil
}

func validateMetricRanges(filters []metricFilter) error {
	for _, d := range metricDefinitions {
		low, high := math.Inf(-1), math.Inf(1)
		openLow, openHigh, missing, present := false, false, false, false
		for _, f := range filters {
			if f.Name != d.Name {
				continue
			}
			switch f.Op {
			case "is_missing":
				missing = true
			case "is_present":
				present = true
			case "gt", "gte", "eq":
				v := *f.Value
				if v > low {
					low, openLow = v, f.Op == "gt"
				} else if v == low && f.Op == "gt" {
					openLow = true
				}
			}
			switch f.Op {
			case "lt", "lte", "eq":
				v := *f.Value
				if v < high {
					high, openHigh = v, f.Op == "lt"
				} else if v == high && f.Op == "lt" {
					openHigh = true
				}
			}
		}
		if low > high || low == high && (openLow || openHigh) || missing && (present || !math.IsInf(low, -1) || !math.IsInf(high, 1)) {
			return fmt.Errorf("contradictory predicates for %s", d.Name)
		}
	}
	return nil
}

func metricMatches(v statMetricValue, f metricFilter, quality string) bool {
	x := v.effective(quality)
	if f.Op == "is_missing" {
		return x == nil
	}
	if f.Op == "is_present" {
		return x != nil
	}
	if x == nil {
		return false
	}
	n := x.(float64)
	switch f.Op {
	case "gt":
		return n > *f.Value
	case "gte":
		return n >= *f.Value
	case "lt":
		return n < *f.Value
	case "lte":
		return n <= *f.Value
	case "eq":
		return n == *f.Value
	}
	return false
}

func listMetrics(args map[string]any) (any, error) {
	if scope := firstString(args["metric_scope"]); scope != "" && scope != "workspace" && scope != "conversation" {
		return nil, fmt.Errorf("metric_scope must be workspace or conversation")
	}
	return map[string]any{"items": metricDefinitions, "aliases": metricAliases, "scopes": []string{"conversation", "workspace"}, "operators": []string{"gt", "gte", "lt", "lte", "eq", "is_missing", "is_present"}, "version": "metrics-v1", "quality_modes": []string{"complete", "available"}, "note": "Own-stream conversation metrics exclude delegates. Workspace metrics include current members and agents. Presence and sorting use quality-qualified effective values; archive coverage is separate."}, nil
}
