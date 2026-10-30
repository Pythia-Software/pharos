package archive

// Validation here binds the published Query Table link representation to the
// page's schema. The dependency still owns normalization and the wire codec.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func nativeQueryShapeError(err error, page string) error {
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) {
		field := strings.TrimPrefix(mismatch.Field, "LinkQuery.")
		expected := "the shape in " + page + "/query/types/QueryState"
		if strings.HasPrefix(field, "select") {
			expected = `an array of column objects, for example [{"field":"model"}]`
		}
		return linkError("query."+field, "needs "+expected+"; browse "+page+"/query")
	}
	return linkError("query", "invalid query shape; "+err.Error()+"; browse "+page+"/query")
}

func queryDiagnostics(value any, path string) error {
	switch v := value.(type) {
	case map[string]any:
		keys := []string{}
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item := v[key]
			if key == "diagnostics" {
				if list, ok := item.([]any); ok && len(list) > 0 {
					return linkError(path, "Query Table rejected this state: "+compactJSON(list))
				}
			}
			if key == "unsupportedOp" && item != "" && item != nil {
				return linkError(path+".op", "unsupported operation "+siteString(item), "count", "count_distinct", "sum", "avg", "min", "max")
			}
			// Host state is opaque host data, not Query Table diagnostic metadata.
			if key != "hostState" {
				if err := queryDiagnostics(item, path+"."+key); err != nil {
					return err
				}
			}
		}
	case []any:
		for i, item := range v {
			if err := queryDiagnostics(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (site *siteMap) checkNativeQuery(page string, query qt.LinkQuery) error {
	node := site.Nodes[page]
	if node == nil || node.Dataset == "" || site.optionOfKind(node, "where") == nil {
		return linkError("query", "needs a query-table page")
	}
	var raw any
	data, _ := json.Marshal(query)
	_ = json.Unmarshal(data, &raw)
	if err := queryDiagnostics(raw, "query"); err != nil {
		return err
	}
	_, err := site.checkQueryLevel(node, site.Datasets[node.Dataset], query, "query", 0)
	return err
}

func (site *siteMap) checkQueryLevel(node *siteNode, dataset *siteDataset, query qt.LinkQuery, path string, depth int) (*siteDataset, error) {
	if depth > 8 {
		return nil, linkError(path, "grouping is too deep")
	}
	current := dataset
	if query.Grouping != nil {
		var group struct {
			Source         qt.LinkQuery      `json:"source"`
			Keys           []json.RawMessage `json:"keys"`
			Summaries      []json.RawMessage `json:"summaries"`
			Representative *struct {
				OrderBy []qt.OrderBy `json:"orderBy"`
			} `json:"representative"`
		}
		if err := json.Unmarshal(query.Grouping, &group); err != nil {
			return nil, nativeQueryShapeError(err, node.Path)
		}
		source, err := site.checkQueryLevel(node, dataset, group.Source, path+".grouping.source", depth+1)
		if err != nil {
			return nil, err
		}
		groupKeys := map[string]bool{}
		for i, raw := range group.Keys {
			var key string
			if json.Unmarshal(raw, &key) != nil {
				var k struct {
					Field string `json:"field"`
				}
				_ = json.Unmarshal(raw, &k)
				key = k.Field
			}
			groupKeys[key] = true
			if err := site.checkQueryField(node, source, key, fmt.Sprintf("%s.grouping.keys[%d]", path, i)); err != nil {
				return nil, err
			}
		}
		if err := site.checkQueryMetrics(node, source, group.Summaries, path+".grouping.summaries"); err != nil {
			return nil, err
		}
		if group.Representative != nil {
			for i, order := range group.Representative.OrderBy {
				if err := site.checkQueryField(node, source, order.Field, fmt.Sprintf("%s.grouping.representative.orderBy[%d].field", path, i)); err != nil {
					return nil, err
				}
			}
		}
		result := *source
		result.Columns = nil
		result.Fields = nil
		for _, column := range source.Columns {
			if groupKeys[siteString(column["name"])] {
				result.Columns = append(result.Columns, column)
			}
		}
		for _, field := range source.Fields {
			if groupKeys[field.Name] {
				result.Fields = append(result.Fields, field)
			}
		}
		for _, raw := range group.Summaries {
			var summary struct {
				ID    string `json:"id"`
				Op    string `json:"op"`
				Field string `json:"field"`
			}
			_ = json.Unmarshal(raw, &summary)
			kind, ops := "number", site.numericOps()
			if summary.Op == "row" || summary.Op == "min" || summary.Op == "max" {
				for _, field := range source.Fields {
					if field.Name == summary.Field {
						kind, ops = field.Type, field.Ops
					}
				}
			}
			result.Columns = append(result.Columns, map[string]any{"name": summary.ID, "type": kind})
			if summary.Op != "link" {
				result.Fields = append(result.Fields, siteField{Name: summary.ID, Type: kind, Ops: ops})
			}
		}
		result.Columns = append(result.Columns, map[string]any{"name": qt.GroupIdentityField, "type": "text"})
		current = &result
	}
	for i, column := range query.Select {
		if err := site.checkQueryField(node, current, column.Field, fmt.Sprintf("%s.select[%d].field", path, i)); err != nil {
			return nil, err
		}
	}
	for i, order := range query.OrderBy {
		if err := site.checkQueryField(node, current, order.Field, fmt.Sprintf("%s.orderBy[%d].field", path, i)); err != nil {
			return nil, err
		}
	}
	for i, term := range query.Where {
		for j, clause := range term.Predicates() {
			if err := site.checkClause(node, current, clause, fmt.Sprintf("%s.where[%d].term[%d]", path, i, j)); err != nil {
				return nil, err
			}
		}
	}
	if err := site.checkQueryMetrics(node, current, query.Aggregations, path+".aggregations"); err != nil {
		return nil, err
	}
	return current, nil
}

// Derived numeric columns share the same operator set as source numbers.
func (site *siteMap) numericOps() string {
	for _, dataset := range site.Datasets {
		for _, field := range dataset.Fields {
			if field.Type == "number" {
				return field.Ops
			}
		}
	}
	return ""
}
func (site *siteMap) checkQueryField(node *siteNode, dataset *siteDataset, name, path string) error {
	if name == "__qt_item_details__" && strings.Contains(path, ".select[") {
		return nil
	}
	choices := []string{}
	for _, column := range dataset.Columns {
		field := siteString(column["name"])
		if field == name {
			return nil
		}
		choices = append(choices, field)
	}
	sort.Strings(choices)
	return linkError(path, fmt.Sprintf("%q is not a field of %s; browse %s/query", name, node.Dataset, node.Path), choices...)
}
func (site *siteMap) checkQueryMetrics(node *siteNode, dataset *siteDataset, metrics []json.RawMessage, path string) error {
	for i, raw := range metrics {
		var metric struct {
			Op         string         `json:"op"`
			Expression string         `json:"expression"`
			Field      string         `json:"field"`
			GroupBy    []string       `json:"groupBy"`
			Where      []qt.WhereTerm `json:"where"`
		}
		if err := json.Unmarshal(raw, &metric); err != nil {
			return nativeQueryShapeError(err, node.Path)
		}
		name := fmt.Sprintf("%s[%d]", path, i)
		ops := []string{"count", "count_distinct", "sum", "avg", "min", "max"}
		if strings.HasSuffix(path, ".summaries") {
			ops = append(ops, "row", "formula", "link")
		}
		if !slices.Contains(ops, metric.Op) {
			return linkError(name+".op", "unsupported metric operation", ops...)
		}
		if metric.Field != "" {
			if err := site.checkQueryField(node, dataset, metric.Field, name+".field"); err != nil {
				return err
			}
		}
		for j, key := range metric.GroupBy {
			if err := site.checkQueryField(node, dataset, key, fmt.Sprintf("%s.groupBy[%d]", name, j)); err != nil {
				return err
			}
		}
		for j, term := range metric.Where {
			for k, clause := range term.Predicates() {
				if err := site.checkClause(node, dataset, clause, fmt.Sprintf("%s.where[%d].term[%d]", name, j, k)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// days composes with a full query only when it has no explicit source time
// bounds. Existing bounds require an explicit edit rather than silent removal.
func (site *siteMap) addNativeWindow(state *siteState, query *qt.LinkQuery) error {
	node := site.Nodes[state.Page]
	dataset := site.Datasets[node.Dataset]
	var add func(*qt.LinkQuery) error
	add = func(q *qt.LinkQuery) error {
		if q.Grouping != nil {
			var group map[string]json.RawMessage
			_ = json.Unmarshal(q.Grouping, &group)
			var source qt.LinkQuery
			_ = json.Unmarshal(group["source"], &source)
			if err := add(&source); err != nil {
				return err
			}
			group["source"], _ = json.Marshal(source)
			q.Grouping, _ = json.Marshal(group)
			return nil
		}
		for _, term := range q.Where {
			for _, clause := range term.Predicates() {
				for _, column := range dataset.Columns {
					if column["name"] == clause.Field && column["type"] == "datetime" {
						return linkError("days", "query already has source time predicates; remove days or edit those bounds explicitly; browse "+node.Path+"/query")
					}
				}
			}
		}
		if state.Days == 0 {
			return nil
		}
		terms := site.fillTerms(site.Windows.QueryTerms, state.Days, map[string]string{"{field}": dataset.WindowField})
		if state.TodayFrom != "" {
			for i := range terms {
				if terms[i].Op == ">=" {
					terms[i].Value = state.TodayFrom
				}
			}
		}
		q.Where = append(q.Where, terms...)
		return nil
	}
	if err := add(query); err != nil {
		return err
	}
	if state.Days > 0 {
		if state.View == nil {
			state.View = map[string]any{}
		}
		wanted := map[string]any{"window_mode": "relative", "window_days": strconv.Itoa(state.Days), "window_field": dataset.WindowField, "window_dataset": node.Dataset}
		if state.TodayFrom != "" {
			wanted["window_mode"] = "today"
			if state.TodayTimezone != "" {
				wanted["window_timezone"] = state.TodayTimezone
			}
		}
		for key, value := range wanted {
			if old, exists := state.View[key]; exists && old != value {
				return linkError("view."+key, "disagrees with days")
			}
			state.View[key] = value
		}
	}
	return nil
}

func (site *siteMap) completeViewArguments(input map[string]any) map[string]any {
	out := map[string]any{}
	for name, value := range input {
		field := site.ViewContract.Fields[name]
		if field.Kind == "enum" || slices.Contains([]string{"reader_page", "delegation_page", "sidebar_width"}, name) {
			if n, ok := value.(float64); ok && n == float64(int64(n)) {
				value = strconv.FormatInt(int64(n), 10)
			}
			if n, ok := value.(int); ok {
				value = strconv.Itoa(n)
			}
		}
		if name == "search_state" || name == "reader_search" {
			if draft, ok := value.(map[string]any); ok && len(draft) > 0 {
				value = site.completeSearchDraft(name, draft)
			}
		}
		if name == "record_keyword" {
			if clause, ok := value.(map[string]any); ok {
				copy := map[string]any{}
				for k, v := range clause {
					copy[k] = v
				}
				if draft, ok := clause["draft"].(map[string]any); ok {
					copy["draft"] = site.completeSearchDraft("search_state", draft)
				}
				value = copy
			}
		}
		out[name] = value
	}
	return out
}
func (site *siteMap) completeSearchDraft(name string, draft map[string]any) map[string]any {
	details, _ := site.ViewContract.Fields[name].Details.(map[string]any)
	template, _ := details["template"].(map[string]any)
	return mergeViewDefaults(template, draft)
}
func mergeViewDefaults(template, value map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range template {
		out[k] = v
	}
	for k, v := range value {
		if child, ok := v.(map[string]any); ok {
			base, _ := template[k].(map[string]any)
			out[k] = mergeViewDefaults(base, child)
		} else {
			out[k] = v
		}
	}
	return out
}

func (site *siteMap) nativeQueryDefault(node *siteNode) any {
	if node.QueryDefault != nil {
		return json.RawMessage(node.QueryDefault)
	}
	if node.BaseQuery != nil {
		return json.RawMessage(node.BaseQuery)
	}
	return json.RawMessage(site.EmptyQuery)
}
func (site *siteMap) nativeWindowHelp(node *siteNode) map[string]any {
	example := map[string]any{"page": node.Path, "query": site.nativeQueryDefault(node)}
	windows := site.windowDays(node)
	if len(windows) == 0 {
		return map[string]any{"supported": false, "example": example, "semantics": "This dataset has no moving time-window control. Use explicit timestamp predicates when the schema provides them."}
	}
	days := append(intStrings(windows), "all")
	if site.supportsToday(siteState{Page: node.Path}) {
		days = append(days, "today")
	}
	example["days"] = windows[0]
	if slices.Contains(windows, 7) {
		example["days"] = 7
	}
	return map[string]any{"supported": true, "timestamp_field": site.Datasets[node.Dataset].WindowField, "days": days, "example": example, "semantics": "days adds relative >= -Nd and < +0s source predicates and matching window_* view metadata. Today renews at the viewer's local midnight; window_timezone may pin a named timezone. Existing time predicates conflict with days; edit them explicitly instead. Grouped queries receive the bounds on their source."}
}

func (site *siteMap) attentionIDSources(pattern string) any {
	switch pattern {
	case "message:<id>":
		return site.IDSources["message_id"]
	case "call:<id>":
		return []map[string]string{{"tool": "get_conversation_messages", "field": "items[].call_id", "note": "Use the call_id of a tool call, not its message_id."}}
	case "session:<id>":
		return []map[string]string{{"endpoint": "GET /api/work/{work_id}/delegation", "field": "rows[].id", "note": "Stable delegation session identity from this endpoint, rather than a conversation ID."}}
	case "row:<id>":
		return "ID column from the page's dataset/query result; include id in select to obtain it."
	case "detail:<json>":
		return "The typed ID column from this page's query results. Property names, schema_name and the inspection template are in <page>/query. JSONPaths use bracket notation; exact detail loading never changes the result population."
	case "card:<id>":
		return "browse_site({path:cards}); each cards/<id> entry names the definition."
	}
	return nil
}
func (site *siteMap) summary(state siteState) string {
	text := strings.TrimSuffix(site.baseSummary(state), ".")
	if state.Query != nil {
		node := site.Nodes[state.Page]
		q := *state.Query
		parts := []string{site.Datasets[node.Dataset].Label + " custom query"}
		if len(q.Select) > 0 {
			names := []string{}
			for _, column := range q.Select {
				names = append(names, column.Field)
			}
			parts = append(parts, "columns "+strings.Join(names, ", "))
		}
		if len(q.OrderBy) > 0 {
			orders := []string{}
			for _, order := range q.OrderBy {
				orders = append(orders, order.Field+" "+order.Dir)
			}
			parts = append(parts, "sorted by "+strings.Join(orders, ", "))
		}
		if q.Grouping != nil {
			var group struct {
				Source    qt.LinkQuery `json:"source"`
				Keys      []any        `json:"keys"`
				Summaries []struct {
					ID    string `json:"id"`
					Op    string `json:"op"`
					Field string `json:"field"`
				} `json:"summaries"`
			}
			_ = json.Unmarshal(q.Grouping, &group)
			parts = append(parts, "grouped by "+compactJSON(group.Keys))
			for _, summary := range group.Summaries {
				parts = append(parts, summary.ID+" = "+summary.Op+"("+summary.Field+")")
			}
			if len(q.Where) > 0 {
				parts = append(parts, "result filters "+site.filterPhrase(site.Datasets[node.Dataset], q.Where))
			}
			q = group.Source
		}
		if len(q.Where) > 0 {
			parts = append(parts, "where "+site.filterPhrase(site.Datasets[node.Dataset], q.Where))
		}
		for _, raw := range state.Query.Aggregations {
			var metric struct {
				ID      string   `json:"id"`
				Op      string   `json:"op"`
				Field   string   `json:"field"`
				GroupBy []string `json:"groupBy"`
			}
			_ = json.Unmarshal(raw, &metric)
			parts = append(parts, metric.ID+" = "+metric.Op+"("+metric.Field+") by "+strings.Join(metric.GroupBy, ", "))
		}
		parts = append(parts, fmt.Sprintf("limit %d, offset %d", state.Query.Limit, state.Query.Offset))
		var details struct {
			JSONInspections []qt.ScopedJSONInspectionState `json:"jsonInspections"`
		}
		_ = json.Unmarshal(state.Query.ItemDetails, &details)
		if len(details.JSONInspections) > 0 {
			parts = append(parts, fmt.Sprintf("%d retained JSON inspections", len(details.JSONInspections)))
			for i, inspection := range details.JSONInspections {
				if i >= 3 {
					break
				}
				if inspection.State.Search != "" {
					parts = append(parts, inspection.Field+" "+inspection.State.SearchMode+" "+strconv.Quote(inspection.State.Search))
				}
			}
		}
		text = strings.Join(parts, ", ")
	}
	keys := []string{}
	for name := range state.View {
		if !slices.Contains(site.ViewContract.Fields[name].Pages, "*") || valueIsMeaningful(state.View[name]) {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := compactJSON(state.View[key])
		if len(value) > 160 {
			value = value[:160] + "…"
		}
		text += ", " + key + "=" + value
	}
	if len(state.Highlight) > 0 {
		text += ", highlight " + strings.Join(state.Highlight, ", ")
	}
	if state.Reveal != "" {
		text += ", reveal " + state.Reveal
	}
	if state.TodayTimezone != "" && !strings.Contains(text, state.TodayTimezone) {
		text += ", Today in " + state.TodayTimezone
	}
	return sentence(text)
}

// Saved computed fields are library entities, not arbitrary spelling escapes.
// Enrich a request-local schema copy so validation and browse see the same IDs.
func (site *siteMap) withCatalogFields(ctx context.Context, catalog *Catalog) (*siteMap, error) {
	if catalog == nil {
		return site, nil
	}
	copy := *site
	copy.Datasets = map[string]*siteDataset{}
	copy.OpSets = map[string][]string{}
	all := []string{}
	for key, ops := range site.OpSets {
		copy.OpSets[key] = ops
		all = append(all, ops...)
	}
	sort.Strings(all)
	copy.OpSets["saved-computed"] = slices.Compact(all)
	for name, dataset := range site.Datasets {
		fields, err := (qt.SQLiteComputedColumnStore{DB: catalog.DB}).List(ctx, queryTableScope, "pharos_"+name)
		if err != nil {
			return nil, err
		}
		enriched := *dataset
		enriched.Columns = slices.Clone(dataset.Columns)
		enriched.Fields = slices.Clone(dataset.Fields)
		for _, field := range fields {
			name := "@computed/" + field.ID
			enriched.Columns = append(enriched.Columns, map[string]any{"name": name, "label": field.Label, "type": "computed"})
			enriched.Fields = append(enriched.Fields, siteField{Name: name, Label: field.Label, Type: "computed", Ops: "saved-computed"})
		}
		copy.Datasets[name] = &enriched
	}
	return &copy, nil
}

func valueIsMeaningful(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v != "" && v != "none"
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	case []string:
		return len(v) > 0
	}
	return false
}
