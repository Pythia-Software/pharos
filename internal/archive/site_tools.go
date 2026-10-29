package archive

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
)

// browse_site and site_link: the site map (site_map.go) for agents, on the
// general MCP server. browse_site lists one level of the map, as a CLI's
// nested --help does; site_link checks a choice of page, options, filters
// and window against the map and returns the canonical link.

var siteMCPTools = []map[string]any{
	{"name": "browse_site", "description": "Browse the pages of the Pharos web UI, one level at a time, to find a view to link to: start with no path, then pass a path from items to drill down. A page lists its options (values, defaults and what each needs), and its children. A dashboard page has <page>/chart (its views, then each view's settings and which combinations it draws) and <page>/filters (its filterable fields; <page>/filters/<field> gives the operators and the most frequent current values). cards lists every prebuilt card, cards/<id> one card. query filters a long list by text. Then call site_link to build the link.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"path":              map[string]any{"type": "string", "description": "A path from a previous answer's items, such as analyze, analyze/tokens, analyze/tokens/chart/trend or analyze/tokens/filters/model_family. Empty for the top level."},
			"query":             map[string]any{"type": "string", "description": "Keep only the list items (pages, cards, fields, options or values) whose name, title or description contain this text."},
			"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "List items per answer; 50 by default."},
			"offset":            map[string]any{"type": "integer", "minimum": 0, "description": "Skip this many list items; an answer with more gives next_offset."},
			"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000}}}},
	{"name": "site_link", "description": "Build a link to a Pharos page from the choices browse_site lists, and check it: returns {url, title, summary}, where summary says in one sentence what the page will show. page is a path from browse_site. On a dashboard (analyze/<topic>), options are the chart's settings for one view, such as {view: \"trend\", metric: \"tokens\", split: \"model_family\", rollingPeriods: 7}; settings left out take the dashboard's defaults. filters are where terms on the page's dataset, such as [{field: \"provider\", op: \"=\", value: \"claude\"}, {field: \"model_family\", op: \"contains\", value: \"opus\"}], with {any: [terms]} for an OR group. days is the rolling window (\"all\" for all time; no window when left out). card starts from a prebuilt card: alone it gives the card's own link, and options, filters or days change it. An error names the argument at fault and lists the valid choices; correct it and call again. Given {url} alone, it checks a Pharos link and returns its canonical form and arguments, or says why the link cannot be expressed.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"max_output_tokens": map[string]any{"type": "integer", "minimum": 200, "maximum": 4000, "description": "Bounds an error's list of choices; a link itself is small."},
			"page":              map[string]any{"type": "string", "description": "A page path from browse_site, such as analyze/tokens, analyze/query/usage, findings or work."},
			"card":              map[string]any{"type": "string", "description": "A card id from browse_site's cards list."},
			"options":           map[string]any{"type": "object", "description": "The page's options by name, or on a dashboard the chart's settings (browse <page>/chart/<view>)."},
			"filters":           map[string]any{"type": "array", "description": "Where terms {field, op, value?, negated?}, or {any: [terms]}; browse <page>/filters for fields and operators. Times take ISO values or relative ones such as -30d.", "items": map[string]any{"type": "object"}},
			"days":              map[string]any{"description": "The rolling window in days, one of the page's windows, or \"all\" for all time.", "anyOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "string", "enum": []string{"all"}}}},
			"url":               map[string]any{"type": "string", "description": "A Pharos link to check and make canonical, given alone."}}}},
}

func init() { mcpTools = append(mcpTools, siteMCPTools...) }

// callSiteMCP answers the site tools; ok is false for other names.
func callSiteMCP(ctx context.Context, catalog *Catalog, name string, args map[string]any) (any, bool, error) {
	if name != "browse_site" && name != "site_link" {
		return nil, false, nil
	}
	site, err := loadedSiteMap()
	if err != nil {
		return nil, true, err
	}
	if name == "site_link" {
		value, err := site.linkTool(args)
		return value, true, err
	}
	value, err := site.browseTool(ctx, catalog, args)
	return value, true, err
}

func (site *siteMap) linkTool(args map[string]any) (map[string]any, error) {
	state, err := site.linkFromArgs(args)
	if err == nil {
		var link string
		if link, err = site.build(state); err == nil {
			return site.linkResult(args, state, link), nil
		}
	}
	return nil, boundChoices(err, mcpBudget(args, 1000))
}

// boundChoices sheds an error's choices from the end until it fits the
// output budget, saying how many it left out.
func boundChoices(err error, budget int) error {
	var linkErr *siteLinkError
	if !errors.As(err, &linkErr) || len(linkErr.Error()) <= budget*3 {
		return err
	}
	bounded := *linkErr
	all := linkErr.Choices
	for keep := len(all) - 1; keep >= 0 && len(bounded.Error()) > budget*3; keep-- {
		bounded.Choices = append(append([]string{}, all[:keep]...), fmt.Sprintf("… %d more", len(all)-keep))
	}
	return &bounded
}

func (site *siteMap) linkResult(args map[string]any, state siteState, link string) map[string]any {
	result := map[string]any{"url": link, "title": site.title(state), "summary": site.summary(state), "arguments": site.arguments(state)}
	// canonical: the given link already is the canonical one, comparing the
	// path and each parameter, query tokens by decoded state and JSON by value
	// (parameter order, deflate bytes and JSON spacing do not count). It is
	// false when reading the link changed something: older names, chart keys
	// its view does not draw, a default written out, or an unsorted value.
	if raw, ok := args["url"].(string); ok {
		given, _ := url.Parse(strings.TrimSpace(raw))
		built, _ := url.Parse(link)
		result["canonical"] = given != nil && given.Path == built.Path && sameSiteParams(given.Query(), built.Query())
	}
	if node := site.Nodes[state.Page]; node.Settings != "" {
		result["note"] = node.Settings
	}
	return result
}

// ----- browse_site -----------------------------------------------------------------

func (site *siteMap) browseTool(ctx context.Context, catalog *Catalog, args map[string]any) (map[string]any, error) {
	path := strings.Trim(firstString(args["path"]), "/ ")
	query := strings.ToLower(strings.TrimSpace(firstString(args["query"])))
	limit := 50
	if args["limit"] != nil {
		limit = clamp(int(integer(args["limit"])), 1, 200)
	}
	offset := max(int(integer(args["offset"])), 0)
	budget := mcpBudget(args, 3000)
	result, err := site.browse(ctx, catalog, path, query)
	if err != nil {
		return nil, err
	}
	items, listed := result["items"].([]map[string]any)
	if !listed {
		if len(jsonText(result)) > budget*3 {
			return nil, fmt.Errorf("max_output_tokens is too small for %s; increase the output budget", path)
		}
		return result, nil
	}
	if query != "" {
		kept := []map[string]any{}
		for _, item := range items {
			if strings.Contains(strings.ToLower(compactJSON(item)), query) {
				kept = append(kept, item)
			}
		}
		items = kept
		result["query"] = query
	}
	total := len(items)
	items = items[min(offset, total):min(offset+limit, total)]
	result["total"] = total
	// Shed list items from the end until the answer fits the budget.
	for {
		result["items"] = items
		delete(result, "next_offset")
		if offset+len(items) < total {
			result["next_offset"] = offset + len(items)
		}
		if len(jsonText(result)) <= budget*3 {
			break
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("max_output_tokens is too small for %s; increase the output budget", defaultString(path, "the top level"))
		}
		items = items[:len(items)-1]
	}
	result["returned_count"] = len(items)
	return result, nil
}

func (site *siteMap) browse(ctx context.Context, catalog *Catalog, path, query string) (map[string]any, error) {
	if path == "" {
		return site.browseRoot(), nil
	}
	if node := site.Nodes[path]; node != nil {
		return site.browseNode(node), nil
	}
	if id, ok := strings.CutPrefix(path, "cards/"); ok && site.cardByID[id] != nil {
		return site.browseCard(site.cardByID[id]), nil
	}
	// <node>/filters[/<field>] and <node>/chart[/<view>[/<option>]].
	for _, name := range site.nodeOrder {
		node := site.Nodes[name]
		rest, ok := strings.CutPrefix(path, name+"/")
		if !ok {
			continue
		}
		parts := strings.Split(rest, "/")
		switch {
		case parts[0] == "filters" && node.Dataset != "" && site.optionOfKind(node, "where") != nil:
			if len(parts) == 1 {
				return site.browseFields(node), nil
			}
			if len(parts) == 2 {
				return site.browseField(ctx, catalog, node, parts[1])
			}
		case parts[0] == "chart" && node.Dashboard != "":
			switch len(parts) {
			case 1:
				return site.browseChart(node), nil
			case 2:
				return site.browseView(node, parts[1])
			case 3:
				return site.browseChartOption(node, parts[1], parts[2])
			}
		}
	}
	parent := path
	for parent != "" {
		if index := strings.LastIndex(parent, "/"); index >= 0 {
			parent = parent[:index]
		} else {
			parent = ""
		}
		if parent == "" || site.Nodes[parent] != nil {
			break
		}
	}
	return nil, linkError("path", fmt.Sprintf("%q is not in the site map; browse %q for what is there.", path, parent), site.childPaths(parent)...)
}

func (site *siteMap) childPaths(path string) []string {
	if path == "" {
		out := []string{}
		for _, name := range site.nodeOrder {
			if !strings.Contains(name, "/") {
				out = append(out, name)
			}
		}
		return out
	}
	out := []string{}
	for _, item := range site.browseNode(site.Nodes[path])["items"].([]map[string]any) {
		out = append(out, item["path"].(string))
	}
	return out
}

func (site *siteMap) browseRoot() map[string]any {
	items := []map[string]any{}
	for _, name := range site.nodeOrder {
		if node := site.Nodes[name]; !strings.Contains(name, "/") {
			items = append(items, map[string]any{"path": name, "title": node.Title, "description": node.Description})
		}
	}
	return map[string]any{
		"path":  "",
		"title": "Pharos",
		"about": "The pages of the Pharos web UI. Browse a path from items for its options and the pages below it; then call site_link with the page path and your choices. Analyze holds the dashboards (tokens and cost, tools, human words …) and query tables; cards holds every prebuilt question.",
		"items": items,
	}
}

func (site *siteMap) browseNode(node *siteNode) map[string]any {
	result := map[string]any{"path": node.Path, "title": node.Title, "description": node.Description, "route": node.Route}
	if node.Settings != "" {
		result["note"] = node.Settings
	}
	result["link"] = map[string]any{"page": node.Path}
	options := []map[string]any{}
	for _, option := range node.Options {
		options = append(options, site.optionSummary(node, option))
	}
	if len(options) > 0 {
		result["options"] = options
	}
	items := []map[string]any{}
	if node.Dashboard != "" {
		items = append(items, map[string]any{"path": node.Path + "/chart", "title": "Chart", "description": "The dashboard's views, and each view's settings: site_link's options."})
	}
	if node.Dataset != "" && site.optionOfKind(node, "where") != nil {
		dataset := site.Datasets[node.Dataset]
		items = append(items, map[string]any{"path": node.Path + "/filters", "title": "Filters", "description": fmt.Sprintf("The %d filterable fields of %s: site_link's filters.", len(dataset.Fields), dataset.Label)})
	}
	for _, child := range node.Children {
		items = append(items, map[string]any{"path": child, "title": site.Nodes[child].Title, "description": site.Nodes[child].Description})
	}
	if node.Cards {
		result["cards"] = "items lists every card; site_link({card}) gives a card's link, and with options, filters or days a changed copy of it."
		for _, card := range site.Cards.Items {
			days := 0
			if card.Window != nil {
				days = card.Window.DefaultDays[0]
			}
			items = append(items, map[string]any{"path": "cards/" + card.ID, "title": site.cardText(card.Title, days), "page": card.Page, "topic": card.Category, "kind": card.Kind})
		}
	}
	result["items"] = items
	return result
}

// optionSummary is one option in plain terms: its values and labels, its
// default, and what it needs.
func (site *siteMap) optionSummary(node *siteNode, option *siteOption) map[string]any {
	summary := map[string]any{"name": option.Name, "kind": option.Kind}
	switch option.Kind {
	case "dashboard_chart":
		return map[string]any{"name": "chart settings", "kind": "chart", "description": "site_link's options are the chart's settings; browse " + node.Path + "/chart for the views and what each draws."}
	case "where":
		return map[string]any{"name": "filters", "kind": "filters", "description": fmt.Sprintf("site_link's filters: where terms on %s; browse %s/filters for the fields.", node.Dataset, node.Path)}
	case "window":
		values := []map[string]any{}
		for _, value := range option.Values {
			values = append(values, map[string]any{"value": value.Value, "label": value.Label})
		}
		return map[string]any{"name": "days", "kind": "window", "values": values, "description": "site_link's days: the rolling window, or \"all\" for all time."}
	case "text_search":
		summary["description"] = "The words to find: text, or a list of rules {kind, text, other?, distance?, exclude?}, all of which must match."
	default:
		if option.Description != "" {
			summary["description"] = option.Description
		}
	}
	if len(option.Values) > 0 && option.Kind != "boolean" {
		values := []map[string]any{}
		for _, value := range option.Values {
			item := map[string]any{"value": value.Value}
			if value.Label != "" {
				item["label"] = value.Label
			}
			if value.Description != "" {
				item["description"] = value.Description
			}
			values = append(values, item)
		}
		summary["values"] = values
	}
	if option.Kind == "boolean" {
		summary["values"] = []any{true, false}
	}
	if option.Kind == "range" {
		summary["value"] = "[start, end] in epoch milliseconds"
	}
	if option.Default != nil && option.Default != "" {
		summary["default"] = option.Default
	}
	needs := []string{}
	for _, need := range option.Requires {
		if len(need.Values) == 0 {
			needs = append(needs, "needs "+need.Option)
		} else {
			needs = append(needs, fmt.Sprintf("needs %s %s", need.Option, strings.Join(siteStrings(need.Values), " or ")))
		}
	}
	for _, conflict := range option.Conflicts {
		needs = append(needs, "not with "+conflict)
	}
	if len(needs) > 0 {
		summary["constraints"] = needs
	}
	if option.ID != "" {
		sources := []string{}
		for _, source := range site.IDSources[option.ID] {
			sources = append(sources, source.Tool+" ("+source.Field+")")
		}
		summary["id_from"] = sources
	}
	return summary
}

func (site *siteMap) browseCard(card *siteCard) map[string]any {
	days := 0
	if card.Window != nil {
		days = card.Window.DefaultDays[0]
	}
	result := map[string]any{"path": "cards/" + card.ID, "id": card.ID, "title": site.cardText(card.Title, days), "description": site.cardText(card.Description, days),
		"topic": card.Category, "dataset": card.Dataset, "kind": card.Kind, "page": card.Page, "link": map[string]any{"card": card.ID}}
	if card.Window != nil {
		result["days"] = card.Window.Days
		result["default_days"] = card.Window.DefaultDays[0]
	} else {
		result["days"] = "none: the card covers a fixed period"
	}
	if card.State != nil {
		chart := map[string]any{}
		for key, value := range card.State {
			if key != "v" {
				chart[key] = value
			}
		}
		result["chart"] = chart
		result["filters"] = card.Filters
		window := "it covers a fixed period"
		if card.Window != nil {
			window = "its window terms on " + card.Window.Field
		}
		result["changes"] = fmt.Sprintf("site_link({card, options, filters, days}) changes the card as its dashboard does: options change this chart, filters replace these, and days picks one of the card's windows. The link keeps the card's other parameters and %s, as the page keeps them when a reader changes the chart or window.", window)
	} else {
		result["changes"] = "Only days can change: the card opens a full query table."
	}
	return result
}

func (site *siteMap) browseFields(node *siteNode) map[string]any {
	dataset := site.Datasets[node.Dataset]
	items := []map[string]any{}
	for _, field := range dataset.Fields {
		item := map[string]any{"path": node.Path + "/filters/" + field.Name, "name": field.Name, "label": field.Label, "type": field.Type}
		if field.Description != "" {
			item["description"] = field.Description
		}
		items = append(items, item)
	}
	result := map[string]any{"path": node.Path + "/filters", "title": dataset.Label + " filters", "dataset": node.Dataset,
		"about": "site_link's filters are terms {field, op, value} on these fields, all of which must hold; {any: [terms]} is an OR group and negated: true inverts a term. Browse a field for its operators and values.",
		"items": items}
	if dataset.WindowField != "" {
		result["window_field"] = dataset.WindowField
	}
	return result
}

// siteValuesDeadline bounds the query for a field's frequent values.
var siteValuesDeadline = 1500 * time.Millisecond

const siteValuesLimit = 50

func (site *siteMap) browseField(ctx context.Context, catalog *Catalog, node *siteNode, name string) (map[string]any, error) {
	dataset := site.Datasets[node.Dataset]
	var field *siteField
	for index := range dataset.Fields {
		if dataset.Fields[index].Name == name {
			field = &dataset.Fields[index]
		}
	}
	if field == nil {
		return nil, linkError("path", fmt.Sprintf("%q is not a filterable field of %s.", name, node.Dataset), site.similarFields(dataset, name)...)
	}
	result := map[string]any{"path": node.Path + "/filters/" + field.Name, "name": field.Name, "label": field.Label, "type": field.Type, "operators": site.OpSets[field.Ops],
		"value_format": map[string]string{"number": "a number, as text or a number", "datetime": "an ISO time, or relative to now: -30d, -12h, +0s (s, m, h, d, w)", "bool": "true or false", "enum": "one of values", "textarray": "one element", "text": "text; contains, starts_with and ends_with ignore case"}[field.Type] + "; is_null and is_not_null take none.",
		"example":      map[string]any{"field": field.Name, "op": site.OpSets[field.Ops][0], "value": "…"}}
	if field.Description != "" {
		result["description"] = field.Description
	}
	if len(field.Values) > 0 {
		items := []map[string]any{}
		for _, value := range field.Values {
			item := map[string]any{"value": value.Value}
			if value.Label != "" {
				item["label"] = value.Label
			}
			items = append(items, item)
		}
		result["items"] = items
		result["values"] = "every value the field takes"
		return result, nil
	}
	if field.Type != "text" && field.Type != "enum" {
		return result, nil
	}
	values, note := catalog.frequentFieldValues(ctx, node.Dataset, field.Name)
	items := []map[string]any{}
	for _, value := range values {
		items = append(items, map[string]any{"value": value.Value, "count": value.Count})
	}
	result["items"] = items
	result["values"] = note
	return result, nil
}

// frequentFieldValues counts a field's most frequent current values without
// computing anything a page has not: SQL datasets in one bounded read on the
// reader, others only from rows the catalog already holds warm (warmOnly). It
// never prepares a derived table or starts a rebuild, so it never writes. A
// read past the deadline, or rows not yet computed, list none and say so.
func (c *Catalog) frequentFieldValues(parent context.Context, dataset, field string) ([]fieldFrequency, string) {
	if c == nil {
		return nil, "No catalog is open, so there are no current values."
	}
	ctx, cancel := context.WithTimeout(parent, siteValuesDeadline)
	defer cancel()
	slow := fmt.Sprintf("Counting this field's values would take longer than %s on this library, so none are listed; filter with contains instead.", siteValuesDeadline)
	if table, ok := sqlDatasetFor(dataset); ok {
		document, err := querySchemaDocument(dataset)
		if err != nil {
			return nil, err.Error()
		}
		schema, err := querytable.LoadSchema(document)
		if err != nil {
			return nil, err.Error()
		}
		values, err := table.FrequentValues(ctx, c.reader(), field, siteValuesLimit, schema)
		if err != nil {
			if ctx.Err() != nil {
				return nil, slow
			}
			return nil, "The values could not be read: " + err.Error()
		}
		return values, frequentNote(len(values))
	}
	var rows []map[string]any
	var err error
	cold := warmOnly(ctx)
	switch dataset {
	case "usage", "conversation_activity", "tl1_attempts", "parallelism":
		rows, _, err = c.datasetRows(cold, dataset, libraryFields{field: true})
	case "writing":
		// Without ensureAuthorship, which may start a rebuild.
		rows, _, err = c.cachedWritingData(cold)
	case "mcp_calls":
		return c.storedFrequentValues(ctx, "mcp_calls", field, slow)
	default:
		return nil, "Current values are not listed for this dataset; for repositories, list_repositories gives the names."
	}
	if errors.Is(err, errDerivedCold) {
		return nil, "This library has not computed these rows since it last changed, so no values are listed yet; opening the page computes them. Filter with contains instead."
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return nil, slow
		}
		return nil, "The values could not be read: " + err.Error()
	}
	counts := map[string]int64{}
	for _, row := range rows {
		if value := firstString(row[field]); value != "" {
			counts[value]++
		}
	}
	values := make([]fieldFrequency, 0, len(counts))
	for value, count := range counts {
		values = append(values, fieldFrequency{Value: value, Count: count})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Count != values[j].Count {
			return values[i].Count > values[j].Count
		}
		return values[i].Value < values[j].Value
	})
	if len(values) > siteValuesLimit {
		values = values[:siteValuesLimit]
	}
	return values, frequentNote(len(values))
}

// storedFrequentValues counts a field stored as a column of table, in one
// bounded GROUP BY on the reader. A field computed from other columns lists
// none.
func (c *Catalog) storedFrequentValues(ctx context.Context, table, field, slow string) ([]fieldFrequency, string) {
	columns, err := queryMapsContext(ctx, c.reader(), "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		if ctx.Err() != nil {
			return nil, slow
		}
		return nil, "The values could not be read: " + err.Error()
	}
	stored := false
	for _, column := range columns {
		stored = stored || firstString(column["name"]) == field
	}
	if !stored {
		return nil, "This field is computed from others, so its current values are not listed; filter with contains instead."
	}
	// The column name is one the table itself reported.
	column := sqlIdentifier(field)
	rows, err := queryMapsContext(ctx, c.reader(), fmt.Sprintf("SELECT CAST(%s AS TEXT) value, COUNT(*) n FROM %s WHERE %s IS NOT NULL AND %s <> '' GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT ?", column, sqlIdentifier(table), column, column), siteValuesLimit)
	if err != nil {
		if ctx.Err() != nil {
			return nil, slow
		}
		return nil, "The values could not be read: " + err.Error()
	}
	values := make([]fieldFrequency, 0, len(rows))
	for _, row := range rows {
		values = append(values, fieldFrequency{Value: firstString(row["value"]), Count: integer(row["n"])})
	}
	return values, frequentNote(len(values))
}

func frequentNote(count int) string {
	if count == 0 {
		return "This library holds no values for the field yet."
	}
	return fmt.Sprintf("The %d most frequent current values, most frequent first, with how many records hold each. Others may exist.", count)
}

func (site *siteMap) browseChart(node *siteNode) map[string]any {
	dashboard := site.Analyze.Dashboards[node.Dashboard]
	items := []map[string]any{}
	for _, name := range dashboard.viewOrder {
		view := dashboard.Views[name]
		items = append(items, map[string]any{"path": node.Path + "/chart/" + name, "view": name, "label": view.Label, "settings": viewOptionNames(view)})
	}
	defaults := map[string]any{}
	for key, value := range dashboard.Defaults {
		if key != "v" {
			defaults[key] = value
		}
	}
	result := map[string]any{"path": node.Path + "/chart", "title": node.Title + " chart", "dataset": dashboard.Dataset,
		"about":    "Pick a view, then browse it for its settings. site_link's options are one view's settings, with view naming it; settings left out take the dashboard's defaults.",
		"defaults": defaults, "items": items}
	if len(dashboard.UnavailableViews) > 0 {
		result["unavailable_views"] = dashboard.UnavailableViews
	}
	if node.Settings != "" {
		result["note"] = node.Settings
	}
	return result
}

// valueLabels names a chart setting's values where the dashboard does.
func (site *siteMap) valueLabels(dashboard *siteDashboard, option string) map[string]string {
	labels := map[string]string{}
	switch option {
	case "metric", "metricY":
		for id, measure := range dashboard.Measures {
			labels[id] = measure.Label
		}
	case "split":
		labels["none"] = "No breakdown"
		for id, breakdown := range dashboard.Breakdowns {
			labels[id] = breakdown.Label
		}
		for id, component := range dashboard.Components {
			labels[id] = component.Label
		}
	case "unit":
		for id, unit := range dashboard.Units {
			labels[id] = unit.Label
		}
	case "reduce":
		labels = site.Analyze.Labels.Reduce
	case "view":
		labels = site.Analyze.Labels.View
	}
	return labels
}

func (site *siteMap) viewNamed(node *siteNode, name string) (*siteDashboard, *siteView, error) {
	dashboard := site.Analyze.Dashboards[node.Dashboard]
	view := dashboard.Views[name]
	if view == nil {
		message := fmt.Sprintf("%q is not a view of %s.", name, node.Path)
		if reason := dashboard.UnavailableViews[name]; reason != "" {
			message = fmt.Sprintf("%s cannot show %s: %s", node.Path, name, reason)
		}
		return nil, nil, linkError("path", message, dashboard.viewOrder...)
	}
	return dashboard, view, nil
}

func (site *siteMap) browseView(node *siteNode, name string) (map[string]any, error) {
	dashboard, view, err := site.viewNamed(node, name)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for _, key := range viewOptionNames(view) {
		if key == "view" {
			continue
		}
		items = append(items, site.chartOptionSummary(node, dashboard, view, name, key, false))
	}
	defaults := map[string]any{}
	for key, value := range view.Defaults {
		if key != "v" {
			defaults[key] = value
		}
	}
	return map[string]any{"path": node.Path + "/chart/" + name, "view": name, "label": view.Label, "defaults": defaults,
		"about":   "Each setting with its values, its default, and constraints: which values the view draws only with some other settings. A chart outside them is an error from site_link that lists the choices.",
		"example": map[string]any{"page": node.Path, "options": map[string]any{"view": name}, "days": 30},
		"items":   items}, nil
}

func (site *siteMap) browseChartOption(node *siteNode, viewName, key string) (map[string]any, error) {
	dashboard, view, err := site.viewNamed(node, viewName)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(viewOptionNames(view), key) || key == "view" {
		return nil, linkError("path", fmt.Sprintf("%q is not a setting of the %s view.", key, viewName), viewOptionNames(view)...)
	}
	summary := site.chartOptionSummary(node, dashboard, view, viewName, key, true)
	summary["path"] = node.Path + "/chart/" + viewName + "/" + key
	if values, ok := summary["values"]; ok {
		summary["items"] = values
		delete(summary, "values")
	}
	return summary, nil
}

// chartOptionSummary describes one chart setting of a view: its values with
// labels (and notes, in detail), default, range, and in plain terms the
// combinations its rule allows.
func (site *siteMap) chartOptionSummary(node *siteNode, dashboard *siteDashboard, view *siteView, viewName, key string, detail bool) map[string]any {
	summary := map[string]any{"name": key}
	if !detail {
		summary["path"] = node.Path + "/chart/" + viewName + "/" + key
	}
	if value, ok := view.Defaults[key]; ok {
		summary["default"] = value
	}
	if key == "selection" {
		limit := 0
		if numeric := view.Numeric["selection"]; numeric.MaxValues != nil {
			limit = int(*numeric.MaxValues)
		}
		summary["kind"] = "legend selection"
		summary["description"] = fmt.Sprintf("{values: [series names], excluded: false} shows only those series; excluded: true hides them. Up to %d values.", limit)
		return summary
	}
	if numeric, ok := view.Numeric[key]; ok && numeric.Min != nil {
		summary["kind"] = "number"
		summary["values"] = numeric.describe()
		return summary
	}
	labels := site.valueLabels(dashboard, key)
	values := []map[string]any{}
	for _, value := range view.Domains[key] {
		item := map[string]any{"value": value}
		if label := labels[siteString(value)]; label != "" {
			item["label"] = label
		}
		if detail {
			if measure, ok := dashboard.Measures[siteString(value)]; ok && (key == "metric" || key == "metricY") {
				item["unit"], item["note"] = measure.Unit, measure.Note
			}
			if unit, ok := dashboard.Units[siteString(value)]; ok && key == "unit" && unit.Note != "" {
				item["note"] = unit.Note
			}
		}
		values = append(values, item)
	}
	summary["kind"] = "choice"
	summary["values"] = values
	for _, rule := range view.Rules {
		if rule.Option != key {
			continue
		}
		constraints := site.describeRule(view, rule)
		if len(constraints) > 0 {
			summary["constraints"] = constraints
		}
		if detail && rule.Fallback != nil && len(rule.Fallback.Given) == 0 {
			summary["otherwise"] = fmt.Sprintf("Outside its constraints the page draws %s instead.", siteString(rule.Fallback.Values))
		}
	}
	return summary
}

// describeRule says in plain terms which values a rule draws only with some
// values of other settings, e.g. "type only with metric tokens".
func (site *siteMap) describeRule(view *siteView, rule siteRule) []string {
	out := []string{}
	for _, constraint := range rule.Constraints {
		groups := map[string][]string{}
		order := []string{}
		for _, value := range view.Domains[rule.Option] {
			phrase := site.allowedPhrase(constraint.Given, constraint.Allowed, value)
			if phrase == "" {
				continue
			}
			if _, seen := groups[phrase]; !seen {
				order = append(order, phrase)
			}
			groups[phrase] = append(groups[phrase], siteString(value))
		}
		for _, phrase := range order {
			out = append(out, strings.Join(groups[phrase], ", ")+" "+phrase)
		}
	}
	return out
}

// allowedPhrase describes where one value is allowed by a constraint's
// table: "" when everywhere, else "only with …" or "not with …".
func (site *siteMap) allowedPhrase(given []string, allowed any, value any) string {
	if len(given) == 0 {
		if listHolds(allowed, value) {
			return ""
		}
		return "is not drawn in this view"
	}
	contexts := siteContexts(given, allowed)
	var yes, no [][]string
	for _, context := range contexts {
		if listHolds(lookupTable(allowed, context), value) {
			yes = append(yes, context)
		} else {
			no = append(no, context)
		}
	}
	if len(no) == 0 {
		return ""
	}
	if len(yes) == 0 {
		return "is not drawn in this view"
	}
	word, list := "only with ", yes
	if len(yes) > len(no) {
		word, list = "not with ", no
	}
	// One given setting: name it once, then its values.
	if len(given) == 1 && given[0] != "multiple_periods" {
		values := []string{}
		for _, context := range list {
			values = append(values, site.contextText(given, context))
		}
		for index := range values {
			values[index] = strings.TrimPrefix(values[index], given[0]+" ")
		}
		return word + given[0] + " " + strings.Join(clip(values, 12), ", ")
	}
	if len(given) == 2 {
		return word + site.pairPhrase(given, contexts, list)
	}
	phrases := []string{}
	for _, context := range list {
		phrases = append(phrases, site.contextText(given, context))
	}
	return word + strings.Join(clip(phrases, 8), "; ")
}

// pairPhrase names contexts of two settings, grouped by the setting with
// fewer values: "unit records and metric tokens, cost; unit conversation".
func (site *siteMap) pairPhrase(given []string, contexts, list [][]string) string {
	distinct := func(index int) []string {
		values := []string{}
		for _, context := range contexts {
			if !slices.Contains(values, context[index]) {
				values = append(values, context[index])
			}
		}
		return values
	}
	outer, inner := 0, 1
	if len(distinct(1)) < len(distinct(0)) {
		outer, inner = 1, 0
	}
	all := len(distinct(inner))
	groups, order := map[string][]string{}, []string{}
	for _, context := range list {
		if _, seen := groups[context[outer]]; !seen {
			order = append(order, context[outer])
		}
		groups[context[outer]] = append(groups[context[outer]], context[inner])
	}
	// Outer values with the same inner values share a phrase.
	byInner, innerOrder := map[string][]string{}, []string{}
	for _, value := range order {
		key := ""
		if values := groups[value]; len(values) < all {
			key = given[inner] + " " + strings.Join(clip(values, 12), ", ")
		}
		if _, seen := byInner[key]; !seen {
			innerOrder = append(innerOrder, key)
		}
		byInner[key] = append(byInner[key], value)
	}
	phrases := []string{}
	for _, key := range innerOrder {
		values := byInner[key]
		phrase := site.contextText([]string{given[outer]}, values[:1])
		if len(values) > 1 {
			phrase = given[outer] + " " + strings.Join(values, ", ")
		}
		if key != "" {
			phrase += " and " + key
		}
		phrases = append(phrases, phrase)
	}
	return strings.Join(phrases, "; ")
}

func clip(items []string, limit int) []string {
	if len(items) <= limit {
		return items
	}
	return append(items[:limit:limit], fmt.Sprintf("… %d more", len(items)-limit))
}

func (site *siteMap) contextText(given, context []string) string {
	parts := make([]string, len(given))
	for index, name := range given {
		value := context[index]
		switch {
		case name == "multiple_periods" && value == "true":
			parts[index] = "periods of several hours or days"
		case name == "multiple_periods":
			parts[index] = "single-unit periods"
		case value == site.Analyze.Absent:
			parts[index] = name + " unset"
		default:
			parts[index] = name + " " + value
		}
	}
	return strings.Join(parts, " and ")
}

// siteContexts lists the paths through a nested allowed table, in sorted
// order.
func siteContexts(given []string, table any) [][]string {
	if len(given) == 0 {
		return [][]string{{}}
	}
	object, ok := table.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := [][]string{}
	for _, key := range keys {
		for _, rest := range siteContexts(given[1:], object[key]) {
			out = append(out, append([]string{key}, rest...))
		}
	}
	return out
}
