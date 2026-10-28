package archive

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// A site link's state: the page, and what a link to it chooses. Building
// is deterministic, and siteLinkFromURL reads a link back into the same
// state, so build(parse(url)) == url for every link Go builds.
type siteState struct {
	Page string
	// Card is set for a card's own link, which keeps the card's parameters.
	Card string
	// Changed marks a card link whose chart and filters are Chart and
	// Filters rather than the card's own, as the page writes it when a reader
	// changes them on the card's dashboard. The card's other parameters and
	// its window terms stay.
	Changed bool
	// Options are a page's options by name: strings, true for a boolean,
	// [start, end] for a range, and []siteTextRule for a text search.
	Options map[string]any
	// Chart is a dashboard's written chart (viewChart).
	Chart   orderedObject
	Filters []qt.WhereTerm
	// Days is the rolling window; 0 is none (all time).
	Days int
}

type siteTextRule struct {
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Other    string `json:"other,omitempty"`
	Distance string `json:"distance,omitempty"`
	Exclude  bool   `json:"exclude,omitempty"`
}

// Parameters the site map names only in its descriptions: the dashboard's
// analyze_days, and a query table's rolling window (queryURL).
const (
	siteAnalyzeDays   = "analyze_days"
	siteWindowMode    = "window_mode"
	siteWindowDays    = "window_days"
	siteWindowField   = "window_field"
	siteWindowDataset = "window_dataset"
)

// siteLinkError names the argument at fault and the valid choices, so an
// agent can correct itself.
type siteLinkError struct {
	Arg     string
	Message string
	Choices []string
}

func (e *siteLinkError) Error() string {
	text := e.Message
	if e.Arg != "" {
		text = e.Arg + ": " + text
	}
	if len(e.Choices) > 0 {
		text += " Choices: " + strings.Join(e.Choices, ", ") + "."
	}
	return text
}

func linkError(arg, message string, choices ...string) error {
	return &siteLinkError{Arg: arg, Message: message, Choices: choices}
}

func (site *siteMap) pagePaths() []string {
	return append([]string(nil), site.nodeOrder...)
}

func (site *siteMap) option(node *siteNode, name string) *siteOption {
	for _, option := range node.Options {
		if option.Name == name {
			return option
		}
	}
	return nil
}

func (site *siteMap) optionOfKind(node *siteNode, kind string) *siteOption {
	for _, option := range node.Options {
		if option.Kind == kind {
			return option
		}
	}
	return nil
}

func optionValues(option *siteOption) []string {
	out := make([]string, len(option.Values))
	for index, value := range option.Values {
		out[index] = siteString(value.Value)
	}
	return out
}

func (site *siteMap) windowDays(node *siteNode) []int {
	option := site.optionOfKind(node, "window")
	if option == nil {
		return nil
	}
	days := []int{}
	for _, value := range option.Values {
		days = append(days, int(jsNumber(value.Value)))
	}
	return days
}

func intStrings(values []int) []string {
	out := make([]string, len(values))
	for index, value := range values {
		out[index] = strconv.Itoa(value)
	}
	return out
}

// ----- Building ----------------------------------------------------------------

func (site *siteMap) build(state siteState) (string, error) {
	node := site.Nodes[state.Page]
	if node == nil {
		return "", linkError("page", fmt.Sprintf("%q is not a page", state.Page), site.pagePaths()...)
	}
	if state.Card != "" {
		card := site.cardByID[state.Card]
		if card == nil {
			return "", linkError("card", fmt.Sprintf("%q is not a card", state.Card))
		}
		if state.Changed {
			return site.changedCardURL(node, card, state)
		}
		return site.cardURL(card, state.Days)
	}
	if node.Dashboard != "" {
		return site.dashboardURL(node, state)
	}
	return site.pageURL(node, state)
}

// dashboardURL follows docs/site-map.md, "Canonical dashboard links": the
// page's fixed parameters, analyze_days, the chart, the filters with the
// window's terms last, and Parallelism's ribbon.
func (site *siteMap) dashboardURL(node *siteNode, state siteState) (string, error) {
	dashboard := site.Analyze.Dashboards[node.Dashboard]
	var params siteParams
	for _, key := range node.Fixed.keys {
		value, _ := node.Fixed.get(key)
		params.add(key, siteString(value))
	}
	days := site.Windows.AnalyzeDaysDefault
	if slices.Contains(site.Windows.AnalyzeDays, state.Days) {
		days = state.Days
	}
	params.add(siteAnalyzeDays, strconv.Itoa(days))
	chart, err := state.Chart.MarshalJSON()
	if err != nil {
		return "", err
	}
	params.add(site.option(node, "chart").Param, string(chart))
	where := append([]qt.WhereTerm{}, state.Filters...)
	if state.Days > 0 {
		where = append(where, site.fillTerms(site.Windows.DashboardTerms, state.Days, map[string]string{"{first}": dashboard.Time.First, "{last}": dashboard.Time.Last})...)
	}
	token, err := site.queryToken(nil, where)
	if err != nil {
		return "", linkError("filters", err.Error())
	}
	params.add(site.optionOfKind(node, "where").Param, token)
	if node.Ribbon != nil {
		ribbon, err := site.ribbon(node, state.Days)
		if err != nil {
			return "", err
		}
		params.add(node.Ribbon.Param, ribbon)
	}
	return siteURL(node.Route, params), nil
}

func (site *siteMap) windowKey(days int) string {
	if days > 0 {
		return strconv.Itoa(days)
	}
	return "all"
}

func (site *siteMap) ribbon(node *siteNode, days int) (string, error) {
	ribbon := node.Ribbon.JSON.clone()
	for name, table := range node.Ribbon.Lookup {
		ribbon.set(name, site.Windows.Tables[table][site.windowKey(days)])
	}
	text, err := ribbon.MarshalJSON()
	return string(text), err
}

// fillTerms fills a term template's placeholders, in its JSON text as the
// site map's templates are written.
func (site *siteMap) fillTerms(template []qt.WhereTerm, days int, names map[string]string) []qt.WhereTerm {
	text := compactJSON(template)
	text = strings.ReplaceAll(text, "{window_days}", strconv.Itoa(days))
	for name, value := range names {
		text = strings.ReplaceAll(text, name, value)
	}
	var out []qt.WhereTerm
	_ = json.Unmarshal([]byte(text), &out)
	return out
}

// pageURL builds every other page: its route, its where terms first (as
// queryURL and searchURL write them), its fixed parameters, the window, then
// its options in the map's order.
func (site *siteMap) pageURL(node *siteNode, state siteState) (string, error) {
	path := node.Route
	for _, option := range node.Options {
		if strings.HasPrefix(option.Param, "{") {
			value, _ := state.Options[option.Name].(string)
			if value == "" {
				return "", linkError("options."+option.Name, "is required for "+node.Path+".")
			}
			path = strings.ReplaceAll(path, option.Param, url.PathEscape(value))
		}
	}
	var params siteParams
	// A query table without filters or a window is the bare page; the
	// Library's search always writes its table query, as searchURL does.
	if where := site.optionOfKind(node, "where"); where != nil && (len(state.Filters) > 0 || state.Days > 0 || node.BaseQuery != nil) {
		terms := append([]qt.WhereTerm{}, state.Filters...)
		if state.Days > 0 {
			terms = append(terms, site.fillTerms(site.Windows.QueryTerms, state.Days, map[string]string{"{field}": site.Datasets[node.Dataset].WindowField})...)
		}
		token, err := site.queryToken(node.BaseQuery, terms)
		if err != nil {
			return "", linkError("filters", err.Error())
		}
		params.add(where.Param, token)
	}
	for _, key := range node.Fixed.keys {
		value, _ := node.Fixed.get(key)
		params.add(key, siteString(value))
	}
	if state.Days > 0 {
		params.add(siteWindowMode, "relative")
		params.add(siteWindowDays, strconv.Itoa(state.Days))
		params.add(siteWindowField, site.Datasets[node.Dataset].WindowField)
		params.add(siteWindowDataset, node.Dataset)
	}
	for _, option := range node.Options {
		value, given := state.Options[option.Name]
		if !given || option.Key != "" || strings.HasPrefix(option.Param, "{") {
			continue
		}
		switch option.Kind {
		case "where", "window":
		case "text_search":
			text, err := site.textBuilder(node, state.Options)
			if err != nil {
				return "", err
			}
			params.add(option.Param, text)
		case "boolean":
			if value == true && len(option.Values) > 0 {
				params.add(option.Param, siteString(option.Values[0].Value))
			}
		case "range":
			bounds := value.([2]int64)
			params.add(option.Param, strconv.FormatInt(bounds[0], 10))
			params.add(option.EndParam, strconv.FormatInt(bounds[1], 10))
		default:
			if text := siteString(value); text != "" {
				params.add(option.Param, text)
			}
		}
	}
	return siteURL(path, params), nil
}

// textBuilder writes the Library's text_builder: the template with one rule
// per condition and the scope's surface and roles (chooseScope).
func (site *siteMap) textBuilder(node *siteNode, options map[string]any) (string, error) {
	rules, _ := options["text"].([]siteTextRule)
	draft := node.TextBuilderTemplate.clone()
	var root orderedObject
	rawRoot := draft.values["root"]
	if err := json.Unmarshal(rawRoot, &root); err != nil {
		return "", err
	}
	ruleID := strings.TrimRight(siteString(mustGet(node.RuleTemplate, "id")), "0123456789")
	children := make([]json.RawMessage, len(rules))
	for index, rule := range rules {
		item := node.RuleTemplate.clone()
		item.set("id", ruleID+strconv.Itoa(index+1))
		item.set("kind", rule.Kind)
		item.set("text", rule.Text)
		if rule.Other != "" {
			item.set("other", rule.Other)
		}
		if rule.Distance != "" {
			item.set("distance", rule.Distance)
		}
		if rule.Exclude {
			item.set("exclude", true)
		}
		raw, _ := item.MarshalJSON()
		children[index] = raw
	}
	root.set("children", children)
	draft.set("root", root)
	scopeName, _ := options["scope"].(string)
	if scopeName == "" {
		scopeName = siteString(site.option(node, "scope").Default)
	}
	var scope orderedObject
	if err := json.Unmarshal(draft.values["scope"], &scope); err != nil {
		return "", err
	}
	surface := node.ScopeSurfaces[scopeName]
	scope.set("surface", surface.Surface)
	scope.set("roles", append([]string{}, surface.Roles...))
	scope.set("kinds", []string{})
	draft.set("scope", scope)
	text, err := draft.MarshalJSON()
	return string(text), err
}

func mustGet(object orderedObject, key string) any {
	value, _ := object.get(key)
	return value
}

// cardURL rebuilds a card's own link at a window from its template
// (docs/site-map.md, "Cards").
func (site *siteMap) cardURL(card *siteCard, days int) (string, error) {
	var params siteParams
	for _, param := range card.URL.Params {
		spec := param.Spec
		switch {
		case param.Literal != nil:
			params.add(param.Name, *param.Literal)
		case spec.Days:
			params.add(param.Name, strconv.Itoa(days))
		case spec.Query != nil:
			raw := []byte(spec.Query)
			if days > 0 {
				raw = fillWindowJSON(raw, days)
			}
			token, err := site.queryToken(raw, nil)
			if err != nil {
				return "", err
			}
			params.add(param.Name, token)
		case spec.Card != "":
			params.add(param.Name, map[string]string{"id": card.ID, "title": card.Title, "description": card.Description, "dataset": card.Dataset}[spec.Card])
		case spec.CardFilters:
			where := append([]qt.WhereTerm{}, card.Filters...)
			if days > 0 && card.Window != nil {
				where = append(where, site.fillTerms(card.Window.Terms, days, nil)...)
			}
			token, err := site.queryToken(nil, where)
			if err != nil {
				return "", err
			}
			params.add(param.Name, token)
		case spec.CardChart:
			chart := card.Chart.clone()
			if days > 0 && card.Window != nil {
				for _, name := range card.Window.ChartDays {
					chart.set(name, days)
				}
			}
			text, _ := chart.MarshalJSON()
			params.add(param.Name, string(text))
		default:
			object := spec.JSON.clone()
			for name, table := range spec.Lookup {
				object.set(name, site.Windows.Tables[table][site.windowKey(days)])
			}
			text, _ := object.MarshalJSON()
			params.add(param.Name, string(text))
		}
	}
	return siteURL(card.URL.Path, params), nil
}

// changedCardURL is a card's link with another chart or filters, as the
// page writes it when a reader changes them on the card's dashboard: only
// analyze_chart and analyze_filters change, and the filters keep the card's
// window terms (updateQueryWindowURL keeps them too when the window changes).
func (site *siteMap) changedCardURL(node *siteNode, card *siteCard, state siteState) (string, error) {
	link, err := site.cardURL(card, state.Days)
	if err != nil {
		return "", err
	}
	parsed, _ := url.Parse(link)
	chartParam, whereParam := site.option(node, "chart").Param, site.optionOfKind(node, "where").Param
	chart, err := state.Chart.MarshalJSON()
	if err != nil {
		return "", err
	}
	token, err := site.queryToken(nil, append(append([]qt.WhereTerm{}, state.Filters...), site.cardWindowTerms(card, state.Days)...))
	if err != nil {
		return "", linkError("filters", err.Error())
	}
	var params siteParams
	for _, pair := range strings.Split(parsed.RawQuery, "&") {
		key, value, _ := strings.Cut(pair, "=")
		name, _ := url.QueryUnescape(key)
		switch name {
		case chartParam:
			params.add(name, string(chart))
		case whereParam:
			params.add(name, token)
		default:
			text, _ := url.QueryUnescape(value)
			params.add(name, text)
		}
	}
	return siteURL(parsed.Path, params), nil
}

func (site *siteMap) cardWindowTerms(card *siteCard, days int) []qt.WhereTerm {
	if days == 0 || card.Window == nil {
		return nil
	}
	return site.fillTerms(card.Window.Terms, days, nil)
}

// cardChartBase is the chart a changed card starts from: the card's own
// chart, as its dashboard holds it, with the settings that follow the window
// (chart_days) at the window. Written in current names, it draws as the
// card's state does.
func cardChartBase(card *siteCard, days int) map[string]any {
	base := card.Chart.toMap()
	if card.Window != nil && days > 0 {
		for _, key := range card.Window.ChartDays {
			base[key] = float64(days)
		}
	}
	return base
}

// ----- From site_link's arguments ------------------------------------------------

// linkFromArgs reads site_link's arguments into a state, checking each
// against the site map.
func (site *siteMap) linkFromArgs(args map[string]any) (siteState, error) {
	for key := range args {
		if !slices.Contains([]string{"page", "card", "options", "filters", "days", "url", "max_output_tokens"}, key) {
			return siteState{}, linkError(key, "is not an argument of site_link.", "page", "card", "options", "filters", "days", "url")
		}
	}
	if raw, ok := args["url"]; ok {
		text, _ := raw.(string)
		if _, budget := args["max_output_tokens"]; len(args) > 1 && !(budget && len(args) == 2) {
			return siteState{}, linkError("url", "is given alone; the other arguments describe a new link instead.")
		}
		return site.parseURL(text)
	}
	page, _ := args["page"].(string)
	page = strings.Trim(page, "/")
	options := map[string]any{}
	if raw, ok := args["options"]; ok && raw != nil {
		object, ok := raw.(map[string]any)
		if !ok {
			return siteState{}, linkError("options", "must be an object of option names and values.")
		}
		options = object
	}
	filters, err := site.argFilters(args["filters"])
	if err != nil {
		return siteState{}, err
	}
	_, filtersGiven := args["filters"]
	days, daysGiven, err := argDays(args["days"])
	if err != nil {
		return siteState{}, err
	}
	var base map[string]any
	if raw, ok := args["card"]; ok && raw != nil {
		id, _ := raw.(string)
		card := site.cardByID[id]
		if card == nil {
			return siteState{}, linkError("card", fmt.Sprintf("%q is not a card; browse_site({path: \"cards\", query: ...}) lists them.", id), site.similarCards(id)...)
		}
		if page != "" && page != card.Page {
			return siteState{}, linkError("page", fmt.Sprintf("card %s opens %s, not %s.", card.ID, card.Page, page))
		}
		page = card.Page
		if !daysGiven && card.Window != nil {
			days = card.Window.DefaultDays[0]
		}
		if len(options) == 0 && !filtersGiven {
			if card.Window == nil && days != 0 {
				return siteState{}, linkError("days", fmt.Sprintf("card %s has no window.", card.ID))
			}
			if card.Window != nil && !slices.Contains(card.Window.Days, days) {
				return siteState{}, linkError("days", fmt.Sprintf("%d is not a window of card %s.", days, card.ID), intStrings(card.Window.Days)...)
			}
			return siteState{Page: page, Card: card.ID, Days: days}, nil
		}
		if card.State == nil {
			return siteState{}, linkError("card", fmt.Sprintf("card %s opens %s; a link that changes it can only change days. Build a link to that page instead.", card.ID, card.Page))
		}
		if card.Window != nil && !slices.Contains(card.Window.Days, days) {
			return siteState{}, linkError("days", fmt.Sprintf("%d is not a window of card %s.", days, card.ID), intStrings(card.Window.Days)...)
		}
		if card.Window == nil && days != 0 {
			return siteState{}, linkError("days", fmt.Sprintf("card %s has no window.", card.ID))
		}
		if !filtersGiven {
			filters = card.Filters
		}
		node := site.Nodes[page]
		chart, err := site.argChart(node, cardChartBase(card, days), options)
		if err != nil {
			return siteState{}, err
		}
		state := siteState{Page: page, Card: card.ID, Changed: true, Chart: chart, Filters: filters, Days: days}
		if len(state.Filters) > 0 {
			if err := site.checkFilters(node, state.Filters, "filters"); err != nil {
				return siteState{}, err
			}
		}
		// Changes that leave the card as it is give the card's own link, which
		// is how that link reads back.
		own := siteState{Page: page, Card: card.ID, Days: days}
		ownLink, ownErr := site.build(own)
		changedLink, changedErr := site.build(state)
		if ownErr == nil && changedErr == nil {
			left, _ := url.Parse(ownLink)
			right, _ := url.Parse(changedLink)
			if left.Path == right.Path && sameSiteParams(left.Query(), right.Query()) {
				return own, nil
			}
		}
		return state, nil
	}
	if page == "" {
		return siteState{}, linkError("page", "is required: a page path from browse_site.", site.pagePaths()...)
	}
	node := site.Nodes[page]
	if node == nil {
		return siteState{}, linkError("page", fmt.Sprintf("%q is not a page; browse_site lists them.", page), site.pagePaths()...)
	}
	state := siteState{Page: page, Filters: filters, Days: days}
	if node.Dashboard != "" {
		chart, err := site.argChart(node, base, options)
		if err != nil {
			return siteState{}, err
		}
		state.Chart = chart
	} else {
		state.Options, err = site.argOptions(node, options)
		if err != nil {
			return siteState{}, err
		}
	}
	return state, site.validate(node, state, "")
}

func argDays(raw any) (int, bool, error) {
	if raw == nil {
		return 0, false, nil
	}
	if text, ok := raw.(string); ok {
		if text == "all" || text == "" {
			return 0, true, nil
		}
		value, err := strconv.Atoi(text)
		if err != nil {
			return 0, true, linkError("days", "must be a number of days, or \"all\" for all time.")
		}
		return value, true, nil
	}
	value, ok := scalar(raw).(float64)
	if !ok || value != float64(int(value)) || value < 0 {
		return 0, true, linkError("days", "must be a number of days, or \"all\" for all time.")
	}
	return int(value), true, nil
}

func (site *siteMap) similarCards(id string) []string {
	out := []string{}
	for _, card := range site.Cards.Items {
		if id != "" && (strings.Contains(card.ID, id) || strings.Contains(id, card.ID)) && len(out) < 10 {
			out = append(out, card.ID)
		}
	}
	return out
}

// argFilters reads where terms: {field, op, value?, negated?} or
// {any: [terms]}. Values are text, as in a query; numbers and booleans are
// written as text.
func (site *siteMap) argFilters(raw any) ([]qt.WhereTerm, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, linkError("filters", "must be a list of {field, op, value} terms, or {any: [terms]} for an OR group.")
	}
	terms := []qt.WhereTerm{}
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, linkError(fmt.Sprintf("filters[%d]", index), "must be {field, op, value} or {any: [terms]}.")
		}
		term := qt.WhereTerm{}
		if members, ok := object["any"]; ok {
			list, ok := members.([]any)
			if !ok || len(list) == 0 {
				return nil, linkError(fmt.Sprintf("filters[%d].any", index), "must be a non-empty list of {field, op, value} terms.")
			}
			term.Any = []qt.WhereClause{}
			for member, value := range list {
				clause, err := argClause(value, fmt.Sprintf("filters[%d].any[%d]", index, member))
				if err != nil {
					return nil, err
				}
				term.Any = append(term.Any, clause)
			}
		} else {
			clause, err := argClause(object, fmt.Sprintf("filters[%d]", index))
			if err != nil {
				return nil, err
			}
			term = qt.WhereTerm{Field: clause.Field, Op: clause.Op, Value: clause.Value, Negated: clause.Negated}
		}
		terms = append(terms, term)
	}
	return terms, nil
}

func argClause(raw any, name string) (qt.WhereClause, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return qt.WhereClause{}, linkError(name, "must be {field, op, value}.")
	}
	for key := range object {
		if !slices.Contains([]string{"field", "op", "value", "negated"}, key) {
			return qt.WhereClause{}, linkError(name+"."+key, "is not part of a term.", "field", "op", "value", "negated")
		}
	}
	clause := qt.WhereClause{Field: firstString(object["field"]), Op: firstString(object["op"])}
	switch value := scalar(object["value"]).(type) {
	case nil:
	case string:
		clause.Value = value
	case float64, bool:
		clause.Value = siteString(value)
	default:
		return qt.WhereClause{}, linkError(name+".value", "must be text, a number or a boolean.")
	}
	if negated, ok := object["negated"]; ok {
		flag, ok := negated.(bool)
		if !ok {
			return qt.WhereClause{}, linkError(name+".negated", "must be true or false.")
		}
		clause.Negated = flag
	}
	return clause, nil
}

// argChart builds a dashboard's chart from a card's state and the options.
func (site *siteMap) argChart(node *siteNode, base, options map[string]any) (orderedObject, error) {
	dashboard := site.Analyze.Dashboards[node.Dashboard]
	viewName := "trend"
	for _, source := range []map[string]any{dashboard.MergeDefaults, base, options} {
		if view, ok := source["view"]; ok && view != nil {
			viewName = siteString(view)
		}
	}
	view := dashboard.Views[viewName]
	if view == nil {
		message := fmt.Sprintf("%q is not a view of %s.", viewName, node.Path)
		if reason := dashboard.UnavailableViews[viewName]; reason != "" {
			message = fmt.Sprintf("%s cannot show %s: %s.", node.Path, viewName, strings.TrimSuffix(reason, "."))
		}
		return orderedObject{}, linkError("options.view", message, dashboard.viewOrder...)
	}
	for key := range options {
		if key == "v" || key == "selection" || slices.Contains(view.Keys, key) {
			continue
		}
		return orderedObject{}, linkError("options."+key, fmt.Sprintf("is not an option of the %s view; browse_site({path: %q}) lists them.", viewName, node.Path+"/chart/"+viewName), viewOptionNames(view)...)
	}
	chart := mergeChart(base, options)
	chart["v"] = float64(site.Analyze.Version)
	chart["view"] = viewName
	written := dashboard.viewChart(chart)
	if err := site.checkChart(node, written.toMap(), "options."); err != nil {
		return orderedObject{}, err
	}
	return written, nil
}

func viewOptionNames(view *siteView) []string {
	out := []string{}
	for _, key := range view.Keys {
		if key != "v" {
			out = append(out, key)
		}
	}
	return append(out, "selection")
}

// checkChart requires a chart the page draws as given, and explains why
// another is not: the key, and the choices that key has here.
func (site *siteMap) checkChart(node *siteNode, chart map[string]any, prefix string) error {
	dashboard := site.Analyze.Dashboards[node.Dashboard]
	verdict, problem := site.judge(dashboard, chart)
	if verdict == verdictValid {
		return nil
	}
	merged, _ := dashboard.readChart(chart)
	view := dashboard.Views[dashboard.viewName(merged)]
	if verdict == verdictInvalid {
		var choices []string
		if view != nil {
			choices = siteStrings(view.Domains[problem.Key])
		} else {
			choices = dashboard.viewOrder
		}
		return linkError(prefix+problem.Key, problem.Reason+".", choices...)
	}
	if problem.Key == "v" || !problem.drawn {
		return linkError(prefix+problem.Key, problem.Reason+".")
	}
	allowed, givens := site.allowedHere(view, merged, problem.Key)
	where := ""
	if len(givens) > 0 {
		where = " when " + strings.Join(givens, " and ")
	}
	drawn := "nothing"
	if problem.Drawn != nil {
		drawn = siteString(problem.Drawn)
	}
	return linkError(prefix+problem.Key, fmt.Sprintf("the %s view does not draw %s %s%s; the page would draw %s instead.", dashboard.viewName(merged), problem.Key, compactJSON(chart[problem.Key]), where, drawn), siteStrings(allowed)...)
}

func siteStrings(values []any) []string {
	out := make([]string, len(values))
	for index, value := range values {
		out[index] = siteString(value)
	}
	return out
}

// argOptions reads a page's options into their state types.
func (site *siteMap) argOptions(node *siteNode, options map[string]any) (map[string]any, error) {
	out := map[string]any{}
	names := []string{}
	for _, option := range node.Options {
		if option.Kind != "where" && option.Kind != "window" {
			names = append(names, option.Name)
		}
	}
	for name, raw := range options {
		option := site.option(node, name)
		if option == nil || option.Kind == "where" || option.Kind == "window" {
			hint := "is not an option of " + node.Path + "."
			if option != nil {
				hint = "is given as the " + map[string]string{"where": "filters", "window": "days"}[option.Kind] + " argument, not an option."
			}
			return nil, linkError("options."+name, hint, names...)
		}
		arg := "options." + name
		switch option.Kind {
		case "text_search":
			rules, err := argTextRules(raw, options["rule"], node, site, arg)
			if err != nil {
				return nil, err
			}
			out[name] = rules
		case "boolean":
			flag, ok := raw.(bool)
			if !ok {
				if text := siteString(raw); option.Values != nil && text == siteString(option.Values[0].Value) {
					flag, ok = true, true
				}
			}
			if !ok {
				return nil, linkError(arg, "must be true or false.")
			}
			if flag {
				out[name] = true
			}
		case "range":
			list, ok := raw.([]any)
			if !ok || len(list) != 2 {
				return nil, linkError(arg, "must be [start, end] in epoch milliseconds.")
			}
			bounds := [2]int64{int64(jsNumber(list[0])), int64(jsNumber(list[1]))}
			out[name] = bounds
		default:
			value, ok := scalar(raw).(string)
			if !ok {
				if number, isNumber := scalar(raw).(float64); isNumber {
					value, ok = siteString(number), true
				}
			}
			if !ok {
				return nil, linkError(arg, "must be text.")
			}
			if value != "" {
				out[name] = value
			}
		}
	}
	return out, nil
}

func argTextRules(raw, defaultKind any, node *siteNode, site *siteMap, arg string) ([]siteTextRule, error) {
	kind := siteString(site.option(node, "rule").Default)
	if text, ok := defaultKind.(string); ok && text != "" {
		kind = text
	}
	var items []any
	switch value := raw.(type) {
	case string:
		items = []any{value}
	case []any:
		items = value
	default:
		return nil, linkError(arg, "must be the words to search for, or a list of rules {kind, text, other?, distance?, exclude?}.")
	}
	rules := []siteTextRule{}
	for index, item := range items {
		rule := siteTextRule{Kind: kind}
		switch value := item.(type) {
		case string:
			rule.Text = value
		case map[string]any:
			for key := range value {
				if !slices.Contains([]string{"kind", "text", "other", "distance", "exclude"}, key) {
					return nil, linkError(fmt.Sprintf("%s[%d].%s", arg, index, key), "is not part of a rule.", "kind", "text", "other", "distance", "exclude")
				}
			}
			if text := firstString(value["kind"]); text != "" {
				rule.Kind = text
			}
			rule.Text, rule.Other = asString(value["text"]), asString(value["other"])
			if distance := value["distance"]; distance != nil {
				rule.Distance = siteString(distance)
			}
			rule.Exclude = value["exclude"] == true
		default:
			return nil, linkError(fmt.Sprintf("%s[%d]", arg, index), "must be text or a rule {kind, text}.")
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// ----- Validation ----------------------------------------------------------------

// validate checks a state against its page: each option's value, the
// combinations the page draws, the window and the filters. prefix names the
// arguments for a parsed URL ("" for site_link's own).
func (site *siteMap) validate(node *siteNode, state siteState, prefix string) error {
	if state.Card != "" {
		return nil
	}
	if state.Days != 0 {
		days := site.windowDays(node)
		if days == nil {
			return linkError(prefix+"days", node.Path+" has no window.")
		}
		if !slices.Contains(days, state.Days) {
			return linkError(prefix+"days", fmt.Sprintf("%d is not a window of %s; use \"all\" for all time.", state.Days, node.Path), intStrings(days)...)
		}
	}
	if len(state.Filters) > 0 {
		if site.optionOfKind(node, "where") == nil {
			return linkError(prefix+"filters", node.Path+" takes no filters.")
		}
		if err := site.checkFilters(node, state.Filters, prefix+"filters"); err != nil {
			return err
		}
	}
	for name, value := range state.Options {
		option := site.option(node, name)
		arg := prefix + "options." + name
		switch option.Kind {
		case "enum":
			if !slices.Contains(optionValues(option), siteString(value)) {
				return linkError(arg, fmt.Sprintf("%q is not a choice.", siteString(value)), optionValues(option)...)
			}
		case "datetime":
			if _, ok := parseTime(siteString(value)); !ok {
				return linkError(arg, "must be an ISO time, such as 2026-01-01T12:00:00Z.")
			}
		case "range":
			bounds := value.([2]int64)
			if bounds[1] <= bounds[0] || bounds[0] < 0 {
				return linkError(arg, "must be [start, end] in epoch milliseconds, with end after start.")
			}
		case "text_search":
			rules := value.([]siteTextRule)
			if len(rules) == 0 {
				return linkError(arg, "needs at least one rule.")
			}
			kinds := optionValues(site.option(node, "rule"))
			for index, rule := range rules {
				ruleArg := fmt.Sprintf("%s[%d]", arg, index)
				if !slices.Contains(kinds, rule.Kind) {
					return linkError(ruleArg+".kind", fmt.Sprintf("%q is not a rule.", rule.Kind), kinds...)
				}
				if strings.TrimSpace(rule.Text) == "" {
					return linkError(ruleArg+".text", "needs at least one word.")
				}
				if (rule.Kind == "near" || rule.Kind == "pre") && strings.TrimSpace(rule.Other) == "" {
					return linkError(ruleArg+".other", rule.Kind+" needs the second phrase in other.")
				}
				if rule.Distance != "" {
					if distance, err := strconv.Atoi(rule.Distance); err != nil || distance < 1 {
						return linkError(ruleArg+".distance", "must be a whole number of words.")
					}
				}
			}
		}
		for _, need := range option.Requires {
			other, given := state.Options[need.Option]
			if len(need.Values) == 0 {
				if !given {
					return linkError(arg, fmt.Sprintf("needs options.%s.", need.Option))
				}
				continue
			}
			if !given {
				other = site.option(node, need.Option).Default
			}
			if !listHolds(any(need.Values), other) {
				return linkError(arg, fmt.Sprintf("needs options.%s to be %s.", need.Option, strings.Join(siteStrings(need.Values), " or ")))
			}
		}
		for _, conflict := range option.Conflicts {
			if _, given := state.Options[conflict]; given {
				return linkError(arg, fmt.Sprintf("cannot be given with options.%s.", conflict))
			}
		}
	}
	return nil
}

var siteFieldTypeChecks = map[string]func(string) bool{
	"number": func(value string) bool { _, err := strconv.ParseFloat(value, 64); return err == nil },
	"datetime": func(value string) bool {
		if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
			_, err := qt.ResolveRelativeDatetime(value, time.Unix(0, 0))
			return err == nil
		}
		_, ok := parseTime(value)
		return ok
	},
	"bool": func(value string) bool { return value == "true" || value == "false" },
}

// checkFilters checks each term against the dataset's filterable fields:
// the field, an operator its type takes, and a value of that type.
func (site *siteMap) checkFilters(node *siteNode, terms []qt.WhereTerm, arg string) error {
	dataset := site.Datasets[node.Dataset]
	for index, term := range terms {
		clauses := term.Predicates()
		for member, clause := range clauses {
			name := fmt.Sprintf("%s[%d]", arg, index)
			if term.IsGroup() {
				name = fmt.Sprintf("%s[%d].any[%d]", arg, index, member)
			}
			if err := site.checkClause(node, dataset, clause, name); err != nil {
				return err
			}
		}
		if term.IsGroup() && len(clauses) == 0 {
			return linkError(fmt.Sprintf("%s[%d].any", arg, index), "must hold at least one term.")
		}
	}
	return nil
}

func (site *siteMap) checkClause(node *siteNode, dataset *siteDataset, clause qt.WhereClause, name string) error {
	var field *siteField
	for index := range dataset.Fields {
		if dataset.Fields[index].Name == clause.Field {
			field = &dataset.Fields[index]
		}
	}
	if field == nil {
		return linkError(name+".field", fmt.Sprintf("%q is not a filterable field of %s; browse_site({path: %q}) lists them.", clause.Field, node.Dataset, node.Path+"/filters"), site.similarFields(dataset, clause.Field)...)
	}
	ops := site.OpSets[field.Ops]
	if !slices.Contains(ops, clause.Op) {
		return linkError(name+".op", fmt.Sprintf("%q is not an operator for %s (%s).", clause.Op, field.Name, field.Type), ops...)
	}
	switch clause.Op {
	case "is_null", "is_not_null":
		if clause.Value != "" {
			return linkError(name+".value", clause.Op+" takes no value.")
		}
		return nil
	case "length_gt", "length_lt", "length_eq":
		if length, err := strconv.Atoi(clause.Value); err != nil || length < 0 {
			return linkError(name+".value", clause.Op+" takes a whole number of characters.")
		}
		return nil
	case "matches_regex", "not_matches_regex":
		if _, err := regexp.Compile(clause.Value); err != nil || clause.Value == "" {
			return linkError(name+".value", "must be a regular expression.")
		}
		return nil
	}
	if check := siteFieldTypeChecks[field.Type]; check != nil && !check(clause.Value) {
		hint := map[string]string{"number": "a number", "datetime": "an ISO time, or a relative time such as -30d or +0s", "bool": "true or false"}[field.Type]
		return linkError(name+".value", fmt.Sprintf("%q is not %s.", clause.Value, hint))
	}
	if field.Type == "enum" && len(field.Values) > 0 && (clause.Op == "=" || clause.Op == "!=") {
		values := make([]string, len(field.Values))
		for index, value := range field.Values {
			values[index] = siteString(value.Value)
		}
		if !slices.Contains(values, clause.Value) {
			return linkError(name+".value", fmt.Sprintf("%q is not a value of %s.", clause.Value, field.Name), values...)
		}
	}
	if clause.Value == "" && clause.Op != "=" && clause.Op != "!=" {
		return linkError(name+".value", clause.Op+" needs a value.")
	}
	return nil
}

func (site *siteMap) similarFields(dataset *siteDataset, name string) []string {
	out := []string{}
	lower := strings.ToLower(name)
	for _, field := range dataset.Fields {
		if lower != "" && (strings.Contains(field.Name, lower) || strings.Contains(strings.ToLower(field.Label), lower)) {
			out = append(out, field.Name)
		}
	}
	if len(out) == 0 && len(dataset.Fields) <= 40 {
		for _, field := range dataset.Fields {
			out = append(out, field.Name)
		}
	}
	return out
}

// ----- Parsing -------------------------------------------------------------------

// siteLinkFromURL reads a site URL back into a state, rejecting anything the
// site map cannot express: another page or origin, unknown, private or
// token parameters, invalid combinations, and states the page would draw
// otherwise.
func siteLinkFromURL(raw string) (siteState, error) {
	site, err := loadedSiteMap()
	if err != nil {
		return siteState{}, err
	}
	return site.parseURL(raw)
}

func (site *siteMap) parseURL(raw string) (siteState, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || raw == "" {
		return siteState{}, linkError("url", "is not a URL.")
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		host := parsed.Hostname()
		if parsed.Scheme != "http" && parsed.Scheme != "https" || !(host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
			return siteState{}, linkError("url", "is not a Pharos page: give a path such as /analyze?…, or a link on this computer.")
		}
	}
	if parsed.Fragment != "" || parsed.Opaque != "" {
		return siteState{}, linkError("url", "carries a fragment, which a site link does not.")
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return siteState{}, linkError("url", "has a malformed query string.")
	}
	for key, list := range values {
		if len(list) > 1 {
			return siteState{}, linkError("url", fmt.Sprintf("repeats the parameter %s.", key))
		}
		if slices.Contains(site.Excluded.Params, key) {
			return siteState{}, linkError("url", fmt.Sprintf("carries %s, a private or sign-in parameter that a site link never holds.", key))
		}
		if strings.HasPrefix(key, "q_") && strings.HasPrefix(list[0], "@") {
			return siteState{}, linkError("url", fmt.Sprintf("%s refers to a saved snapshot, which is private to this browser.", key))
		}
	}
	path := parsed.Path
	if target, ok := site.Excluded.LegacyRoutes[path]; ok {
		return siteState{}, linkError("url", fmt.Sprintf("%s is an older alias; link to %s instead.", path, target))
	}
	node, pathValues := site.matchNode(path, values)
	if node == nil {
		return siteState{}, linkError("url", fmt.Sprintf("%s is not a page of the site.", path), site.pageRoutes()...)
	}
	consumed := map[string]bool{}
	for _, key := range node.Fixed.keys {
		consumed[key] = true
	}
	if values.Has("card_id") {
		return site.parseCard(parsed.Path, values)
	}
	state, err := site.parsePageLink(node, values, pathValues, consumed)
	if err != nil {
		// A card's own link may carry a whole query table, which no page
		// option expresses.
		if card, cardErr := site.parseCard(parsed.Path, values); cardErr == nil {
			return card, nil
		}
	}
	return state, err
}

func (site *siteMap) parsePageLink(node *siteNode, values url.Values, pathValues map[string]string, consumed map[string]bool) (siteState, error) {
	var state siteState
	var err error
	if node.Dashboard != "" {
		state, err = site.parseDashboard(node, values, consumed)
	} else {
		state, err = site.parsePage(node, values, pathValues, consumed)
	}
	if err != nil {
		return siteState{}, err
	}
	for key := range values {
		if !consumed[key] {
			return siteState{}, linkError("url", fmt.Sprintf("%s does not take the parameter %s.", node.Path, key), site.nodeParams(node)...)
		}
	}
	return state, site.validate(node, state, "url ")
}

func (site *siteMap) pageRoutes() []string {
	out := []string{}
	for _, path := range site.nodeOrder {
		if route := site.Nodes[path].Route; !slices.Contains(out, route) {
			out = append(out, route)
		}
	}
	return out
}

func (site *siteMap) nodeParams(node *siteNode) []string {
	out := []string{}
	for _, option := range node.Options {
		if strings.HasPrefix(option.Param, "{") || option.Key != "" {
			continue
		}
		if !slices.Contains(out, option.Param) {
			out = append(out, option.Param)
		}
		if option.EndParam != "" {
			out = append(out, option.EndParam)
		}
	}
	if node.Dashboard != "" {
		out = append(out, siteAnalyzeDays)
	} else if site.optionOfKind(node, "window") != nil {
		out = append(out, siteWindowMode, siteWindowField, siteWindowDataset)
	}
	if node.Ribbon != nil {
		out = append(out, node.Ribbon.Param)
	}
	return out
}

// matchNode finds the page a path and its fixed parameters address: of the
// nodes whose route matches, the one whose fixed parameters all match, the
// most specific first.
func (site *siteMap) matchNode(path string, values url.Values) (*siteNode, map[string]string) {
	var best *siteNode
	var bestValues map[string]string
	for _, name := range site.nodeOrder {
		node := site.Nodes[name]
		pathValues, ok := matchRoute(node.Route, path)
		if !ok {
			continue
		}
		matches := true
		for _, key := range node.Fixed.keys {
			value, _ := node.Fixed.get(key)
			if values.Get(key) != siteString(value) {
				matches = false
			}
		}
		if matches && (best == nil || len(node.Fixed.keys) > len(best.Fixed.keys)) {
			best, bestValues = node, pathValues
		}
	}
	return best, bestValues
}

func matchRoute(route, path string) (map[string]string, bool) {
	routeParts, pathParts := strings.Split(route, "/"), strings.Split(path, "/")
	if len(routeParts) != len(pathParts) {
		return nil, false
	}
	values := map[string]string{}
	for index, part := range routeParts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			if pathParts[index] == "" {
				return nil, false
			}
			values[part] = pathParts[index]
		} else if part != pathParts[index] {
			return nil, false
		}
	}
	return values, true
}

// whereOnly reads a where-only query token over a base query: its where
// terms, or an error when it carries anything else.
func (site *siteMap) whereOnly(param, token string, base json.RawMessage) ([]qt.WhereTerm, error) {
	decoded, err := qt.DecodeQueryLink(token)
	if err != nil {
		return nil, linkError("url", fmt.Sprintf("%s is not a readable query: %v", param, err))
	}
	canonical, err := site.queryToken(base, decoded.Where)
	if err != nil {
		return nil, linkError("url", fmt.Sprintf("%s holds where terms a link cannot carry: %v", param, err))
	}
	if !sameQueryToken(token, canonical) {
		return nil, linkError("url", fmt.Sprintf("%s carries more than where terms (columns, sorting, grouping or metrics), which a site link cannot express.", param))
	}
	return decoded.Where, nil
}

// cutWindow takes a window's terms off the end of the where terms: the
// first window in days whose terms end them.
func (site *siteMap) cutWindow(where []qt.WhereTerm, days []int, template []qt.WhereTerm, names map[string]string) ([]qt.WhereTerm, int) {
	for _, day := range days {
		terms := site.fillTerms(template, day, names)
		if len(where) >= len(terms) && compactJSON(where[len(where)-len(terms):]) == compactJSON(terms) {
			return where[:len(where)-len(terms)], day
		}
	}
	return where, 0
}

func (site *siteMap) parseDashboard(node *siteNode, values url.Values, consumed map[string]bool) (siteState, error) {
	dashboard := site.Analyze.Dashboards[node.Dashboard]
	state := siteState{Page: node.Path}
	chartParam := site.option(node, "chart").Param
	consumed[chartParam] = true
	written, err := site.readURLChart(node, values, chartParam)
	if err != nil {
		return siteState{}, err
	}
	state.Chart = written
	whereParam := site.optionOfKind(node, "where").Param
	consumed[whereParam] = true
	var where []qt.WhereTerm
	if values.Has(whereParam) {
		var err error
		if where, err = site.whereOnly(whereParam, values.Get(whereParam), nil); err != nil {
			return siteState{}, err
		}
	}
	state.Filters, state.Days = site.cutWindow(where, site.windowDays(node), site.Windows.DashboardTerms, map[string]string{"{first}": dashboard.Time.First, "{last}": dashboard.Time.Last})
	consumed[siteAnalyzeDays] = true
	expected := site.Windows.AnalyzeDaysDefault
	if slices.Contains(site.Windows.AnalyzeDays, state.Days) {
		expected = state.Days
	}
	if values.Has(siteAnalyzeDays) && values.Get(siteAnalyzeDays) != strconv.Itoa(expected) {
		return siteState{}, linkError("url", fmt.Sprintf("%s=%s disagrees with the window in %s, which needs %d.", siteAnalyzeDays, values.Get(siteAnalyzeDays), whereParam, expected))
	}
	if node.Ribbon != nil {
		consumed[node.Ribbon.Param] = true
		if values.Has(node.Ribbon.Param) {
			ribbon, _ := site.ribbon(node, state.Days)
			if !sameJSONText(values.Get(node.Ribbon.Param), ribbon) {
				return siteState{}, linkError("url", node.Ribbon.Param+" selects a ribbon, zoom or range other than the window's, which a site link cannot express.")
			}
		}
	}
	return state, nil
}

// readURLChart reads a link's chart: one the page draws as given, written
// in current names with only its view's keys.
func (site *siteMap) readURLChart(node *siteNode, values url.Values, chartParam string) (orderedObject, error) {
	dashboard := site.Analyze.Dashboards[node.Dashboard]
	// Without a chart the page opens its defaults, as a current chart.
	chart := map[string]any{"v": float64(site.Analyze.Version)}
	if text := values.Get(chartParam); values.Has(chartParam) {
		chart = map[string]any{}
		if err := json.Unmarshal([]byte(text), &chart); err != nil || chart == nil {
			return orderedObject{}, linkError("url", chartParam+" is not a JSON object.")
		}
	}
	verdict, _ := site.judge(dashboard, chart)
	written := dashboard.viewChart(chart)
	if _, renamed := dashboard.readChart(chart); verdict == verdictValid || verdict == verdictCoercible && renamed && chart["v"] != nil {
		if again, _ := site.judge(dashboard, written.toMap()); again == verdictValid {
			verdict = verdictValid
		}
	}
	if verdict != verdictValid {
		if err := site.checkChart(node, chart, "url "+chartParam+"."); err != nil {
			return orderedObject{}, err
		}
		return orderedObject{}, linkError("url", chartParam+" is not drawn as given.")
	}
	return written, nil
}

func (site *siteMap) parsePage(node *siteNode, values url.Values, pathValues map[string]string, consumed map[string]bool) (siteState, error) {
	state := siteState{Page: node.Path, Options: map[string]any{}}
	if where := site.optionOfKind(node, "where"); where != nil {
		consumed[where.Param] = true
		var terms []qt.WhereTerm
		if values.Has(where.Param) {
			var err error
			if terms, err = site.whereOnly(where.Param, values.Get(where.Param), node.BaseQuery); err != nil {
				return siteState{}, err
			}
		}
		state.Filters = terms
		if site.optionOfKind(node, "window") != nil && values.Has(siteWindowDays) {
			dataset := site.Datasets[node.Dataset]
			for _, key := range []string{siteWindowMode, siteWindowDays, siteWindowField, siteWindowDataset} {
				consumed[key] = true
			}
			if values.Get(siteWindowMode) != "relative" || values.Get(siteWindowField) != dataset.WindowField || values.Get(siteWindowDataset) != node.Dataset {
				return siteState{}, linkError("url", fmt.Sprintf("a window on %s is window_mode=relative, window_field=%s and window_dataset=%s.", node.Path, dataset.WindowField, node.Dataset))
			}
			days, err := strconv.Atoi(values.Get(siteWindowDays))
			if err != nil {
				return siteState{}, linkError("url", "window_days is not a number of days.")
			}
			rest, cut := site.cutWindow(terms, []int{days}, site.Windows.QueryTerms, map[string]string{"{field}": dataset.WindowField})
			if cut == 0 {
				return siteState{}, linkError("url", fmt.Sprintf("the window parameters say %d days, but %s does not end with that window's terms.", days, where.Param))
			}
			state.Filters, state.Days = rest, days
		}
	}
	for _, option := range node.Options {
		if strings.HasPrefix(option.Param, "{") {
			if value, ok := pathValues[option.Param]; ok {
				decoded, err := url.PathUnescape(value)
				if err != nil {
					return siteState{}, linkError("url", "has a malformed "+option.Name+".")
				}
				state.Options[option.Name] = decoded
			}
			continue
		}
		if option.Key != "" || option.Kind == "where" || option.Kind == "window" || !values.Has(option.Param) {
			continue
		}
		consumed[option.Param] = true
		text := values.Get(option.Param)
		switch option.Kind {
		case "text_search":
			options, err := site.parseTextBuilder(node, text)
			if err != nil {
				return siteState{}, err
			}
			for key, value := range options {
				state.Options[key] = value
			}
		case "boolean":
			if len(option.Values) == 0 || text != siteString(option.Values[0].Value) {
				return siteState{}, linkError("url", fmt.Sprintf("%s=%s is not %s=%s.", option.Param, text, option.Param, siteString(option.Values[0].Value)))
			}
			state.Options[option.Name] = true
		case "range":
			consumed[option.EndParam] = true
			start, startErr := strconv.ParseInt(text, 10, 64)
			end, endErr := strconv.ParseInt(values.Get(option.EndParam), 10, 64)
			if startErr != nil || endErr != nil {
				return siteState{}, linkError("url", fmt.Sprintf("%s and %s must both be epoch milliseconds.", option.Param, option.EndParam))
			}
			state.Options[option.Name] = [2]int64{start, end}
		default:
			if text != "" {
				state.Options[option.Name] = text
			}
		}
	}
	for _, option := range node.Options {
		if option.EndParam != "" && values.Has(option.EndParam) && !consumed[option.EndParam] {
			return siteState{}, linkError("url", fmt.Sprintf("%s needs %s.", option.EndParam, option.Param))
		}
	}
	return state, nil
}

// parseTextBuilder reads the Library's text_builder: its rules and scope.
// Anything else it holds (fuzzy matching, case, other scope filters) is not
// in the site map, and the rebuilt draft would differ.
func (site *siteMap) parseTextBuilder(node *siteNode, text string) (map[string]any, error) {
	var draft struct {
		Root struct {
			Children []map[string]any `json:"children"`
		} `json:"root"`
		Scope struct {
			Surface string   `json:"surface"`
			Roles   []string `json:"roles"`
		} `json:"scope"`
	}
	if err := json.Unmarshal([]byte(text), &draft); err != nil {
		return nil, linkError("url", "text_builder is not a JSON text search.")
	}
	rules := []siteTextRule{}
	for _, child := range draft.Root.Children {
		rule := siteTextRule{Kind: asString(child["kind"]), Text: asString(child["text"]), Other: asString(child["other"]), Distance: asString(child["distance"]), Exclude: child["exclude"] == true}
		if template := asString(mustGet(node.RuleTemplate, "distance")); rule.Distance == template {
			rule.Distance = ""
		}
		rules = append(rules, rule)
	}
	options := map[string]any{"text": rules}
	scopes := site.option(node, "scope")
	scope := ""
	for _, value := range scopes.Values {
		surface := node.ScopeSurfaces[siteString(value.Value)]
		if surface.Surface == draft.Scope.Surface && slices.Equal(surface.Roles, draft.Scope.Roles) && (len(surface.Roles) > 0 || len(draft.Scope.Roles) == 0) {
			scope = siteString(value.Value)
			break
		}
	}
	if scope == "" {
		return nil, linkError("url", "text_builder searches a scope the site map does not list.", optionValues(scopes)...)
	}
	if scope != siteString(scopes.Default) {
		options["scope"] = scope
	}
	rebuilt, err := site.textBuilder(node, options)
	if err != nil {
		return nil, err
	}
	if !sameJSONText(rebuilt, text) {
		return nil, linkError("url", "text_builder holds settings beyond its rules and scope (such as fuzzy or case-sensitive matching, or other filters), which a site link cannot express.")
	}
	return options, nil
}

// parseCard reads a card's own link: the card's link at one of its
// windows. A link with card_id names its card; one without is compared with
// every card whose link has the same path and parameters.
func (site *siteMap) parseCard(path string, values url.Values) (siteState, error) {
	candidates := site.Cards.Items
	if id := values.Get("card_id"); values.Has("card_id") {
		card := site.cardByID[id]
		if card == nil {
			return siteState{}, linkError("url", fmt.Sprintf("card_id %q is not a card.", id))
		}
		candidates = []*siteCard{card}
	}
	for _, card := range candidates {
		if card.URL.Path != path || len(card.URL.Params) != len(values) {
			continue
		}
		windows := []int{0}
		if card.Window != nil {
			windows = card.Window.Days
		}
		node := site.Nodes[card.Page]
		var changeable []string
		if card.State != nil && node.Dashboard != "" {
			changeable = []string{site.option(node, "chart").Param, site.optionOfKind(node, "where").Param}
		}
		for _, days := range windows {
			built, _ := site.cardURL(card, days)
			expected, _ := url.Parse(built)
			if expected.Path != path {
				continue
			}
			want := expected.Query()
			if sameSiteParams(values, want) {
				return siteState{Page: card.Page, Card: card.ID, Days: days}, nil
			}
			if changeable == nil || !sameSiteParamsExcept(values, want, changeable) {
				continue
			}
			// The card with another chart or filters, as its dashboard writes it.
			chart, err := site.readURLChart(node, values, changeable[0])
			if err != nil {
				return siteState{}, err
			}
			where, err := site.whereOnly(changeable[1], values.Get(changeable[1]), nil)
			if err != nil {
				return siteState{}, err
			}
			terms := site.cardWindowTerms(card, days)
			if len(where) < len(terms) || compactJSON(where[len(where)-len(terms):]) != compactJSON(terms) {
				return siteState{}, linkError("url", fmt.Sprintf("%s no longer ends with card %s's window terms for %d days, which a site link cannot express.", changeable[1], card.ID, days))
			}
			filters := where[:len(where)-len(terms)]
			if len(filters) > 0 {
				if err := site.checkFilters(node, filters, "url filters"); err != nil {
					return siteState{}, err
				}
			}
			return siteState{Page: card.Page, Card: card.ID, Changed: true, Chart: chart, Filters: filters, Days: days}, nil
		}
	}
	if len(candidates) == 1 {
		card := candidates[0]
		return siteState{}, linkError("url", fmt.Sprintf("is not card %s's link at any of its windows; a changed card is a plain %s link (site_link with card and the changes).", card.ID, card.Page))
	}
	return siteState{}, linkError("url", "is no card's link.")
}

// ----- Titles and summaries ----------------------------------------------------

func (site *siteMap) title(state siteState) string {
	node := site.Nodes[state.Page]
	if state.Card != "" {
		return site.cardText(site.cardByID[state.Card].Title, state.Days)
	}
	if node.Dashboard != "" {
		view, _ := state.Chart.get("view")
		if label := site.Analyze.Labels.View[siteString(view)]; label != "" {
			return node.Title + " · " + label
		}
	}
	if id, ok := state.Options["work_id"].(string); ok {
		return node.Title + " " + id
	}
	return node.Title
}

// section names the top-level page a page sits under, such as Analyze for
// analyze/tokens.
func (site *siteMap) section(page string) string {
	top, _, _ := strings.Cut(page, "/")
	node := site.Nodes[top]
	if node == nil {
		return "Pharos"
	}
	title, _, _ := strings.Cut(node.Title, " · ")
	return title
}

func (site *siteMap) cardText(text string, days int) string {
	if days == 0 {
		return text
	}
	for token, value := range site.Cards.TextWindows[strconv.Itoa(days)] {
		text = strings.ReplaceAll(text, token, value)
	}
	return text
}

// summary is one plain sentence of what the link shows.
func (site *siteMap) summary(state siteState) string {
	node := site.Nodes[state.Page]
	if state.Card != "" && !state.Changed {
		card := site.cardByID[state.Card]
		return sentence(site.cardText(card.Description, state.Days))
	}
	parts := []string{}
	switch {
	case node.Dashboard != "":
		parts = append(parts, site.chartPhrase(node, state.Chart.toMap()))
	case node.Dataset != "" && site.optionOfKind(node, "where") != nil && node.Path != "library":
		parts = append(parts, site.Datasets[node.Dataset].Label+" rows")
	case node.Path == "library":
		parts = append(parts, site.libraryPhrase(node, state.Options))
	default:
		phrase := node.Title
		details := []string{}
		for _, option := range node.Options {
			if value, ok := state.Options[option.Name]; ok && !strings.HasPrefix(option.Param, "{") {
				details = append(details, option.Name+" "+siteOptionText(value))
			}
		}
		if len(details) > 0 {
			phrase += " " + strings.Join(details, ", ")
		} else if node.Description != "" {
			phrase = strings.TrimSuffix(node.Description, ".")
		}
		parts = append(parts, phrase)
	}
	if len(state.Filters) > 0 {
		parts = append(parts, "where "+site.filterPhrase(site.Datasets[node.Dataset], state.Filters))
	}
	if site.optionOfKind(node, "window") != nil {
		if state.Changed && state.Days > 0 {
			parts = append(parts, fmt.Sprintf("last %s of %s, as the card counts its window", site.Windows.Labels[strconv.Itoa(state.Days)], site.cardByID[state.Card].Window.Field))
		} else if state.Days > 0 {
			label := site.Windows.Labels[strconv.Itoa(state.Days)]
			if label == "" {
				label = strconv.Itoa(state.Days) + " days"
			}
			parts = append(parts, "last "+label)
		} else if node.Dashboard != "" || len(state.Filters) > 0 {
			parts = append(parts, "all time")
		}
	}
	return sentence(strings.Join(parts, ", "))
}

func siteOptionText(value any) string {
	switch v := value.(type) {
	case bool:
		return "on"
	case [2]int64:
		return fmt.Sprintf("%s to %s", time.UnixMilli(v[0]).UTC().Format(time.RFC3339), time.UnixMilli(v[1]).UTC().Format(time.RFC3339))
	}
	return siteString(value)
}

func sentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return text
	}
	first, size := utf8.DecodeRuneInString(text)
	text = string(unicode.ToUpper(first)) + text[size:]
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return text
}

// lowerLabel lowercases a label's first letter, unless it starts an acronym.
func lowerLabel(label string) string {
	first, size := utf8.DecodeRuneInString(label)
	if next, _ := utf8.DecodeRuneInString(label[size:]); unicode.IsUpper(next) {
		return label
	}
	return string(unicode.ToLower(first)) + label[size:]
}

func (site *siteMap) chartPhrase(node *siteNode, chart map[string]any) string {
	dashboard := site.Analyze.Dashboards[node.Dashboard]
	merged, _ := dashboard.readChart(chart)
	viewName := dashboard.viewName(merged)
	view := dashboard.Views[viewName]
	drawn := site.drawChart(view, merged)
	// A setting neither the merge nor a rule gives is the page's default.
	value := func(key string) any {
		if value, ok := drawn[key]; ok {
			return value
		}
		if value, ok := merged[key]; ok {
			return value
		}
		return view.Defaults[key]
	}
	text := func(key string) string { return siteString(value(key)) }
	measure := func(key string) string {
		if measure, ok := dashboard.Measures[text(key)]; ok {
			return measure.Label
		}
		return text(key)
	}
	split := ""
	if id := text("split"); id != "" && id != "none" && value("split") != nil {
		label := id
		if breakdown, ok := dashboard.Breakdowns[id]; ok {
			label = breakdown.Label
		} else if component, ok := dashboard.Components[id]; ok {
			label = component.Label
		}
		split = " by " + lowerLabel(label)
	}
	unit := func(plural bool) string {
		if unit, ok := dashboard.Units[text("unit")]; ok {
			if plural && unit.Plural != "" {
				return lowerLabel(unit.Plural)
			}
			return lowerLabel(unit.Label)
		}
		return text("unit")
	}
	switch viewName {
	case "trend":
		period := text("period")
		count := int(jsNumber(value(period)))
		phrase := measure("metric") + " per " + period
		singular := strings.TrimSuffix(period, "s")
		if count > 1 && (period == "hours" || period == "days") {
			phrase = fmt.Sprintf("%s per %d %s", measure("metric"), count, period)
		} else if count >= 1 {
			phrase = measure("metric") + " per " + singular
		}
		phrase += split
		if value("share") == true {
			phrase += ", as a share of the total"
		}
		if text("display") == "cumulative" {
			phrase += ", cumulative"
		}
		if rolling := int(jsNumber(value("rollingPeriods"))); rolling > 0 {
			span := singular
			if count > 1 && (period == "hours" || period == "days") {
				span = "period"
			}
			phrase += fmt.Sprintf(", %d-%s rolling %s", rolling, span, text("rollingOp"))
		}
		if value("norm") == true {
			phrase += fmt.Sprintf(", against the norm of the previous %s periods", text("normPeriods"))
		}
		return phrase
	case "compare":
		phrase := measure("metric") + split
		if reduce := text("reduce"); text("unit") != "records" {
			phrase += ", " + lowerLabel(site.Analyze.Labels.Reduce[reduce]) + " over " + unit(true)
		}
		if text("compareAs") == "share" {
			phrase += ", as shares"
		}
		return phrase
	case "distribution":
		return "distribution of " + lowerLabel(measure("metric")) + " per " + unit(false) + split
	case "scatter":
		return measure("metric") + " against " + lowerLabel(measure("metricY")) + " per " + unit(false) + split
	case "cycle":
		return fmt.Sprintf("%s by %s (%s)%s", measure("metric"), text("cycle"), lowerLabel(site.Analyze.Labels.Reduce[text("reduce")]), split)
	}
	return measure("metric") + split
}

var siteOpWords = map[string]string{
	"=": "is", "!=": "is not", ">": "is over", ">=": "is at least", "<": "is under", "<=": "is at most",
	"contains": "contains", "starts_with": "starts with", "ends_with": "ends with", "matches_regex": "matches", "not_matches_regex": "does not match",
	"length_gt": "is longer than", "length_lt": "is shorter than", "length_eq": "has length", "is_null": "is empty", "is_not_null": "is set", "includes": "includes",
}

func (site *siteMap) filterPhrase(dataset *siteDataset, terms []qt.WhereTerm) string {
	label := func(name string) string {
		for _, field := range dataset.Fields {
			if field.Name == name {
				return lowerLabel(field.Label)
			}
		}
		return name
	}
	clause := func(c qt.WhereClause) string {
		op := siteOpWords[c.Op]
		if op == "" {
			op = c.Op
		}
		text := label(c.Field) + " " + op
		if c.Value != "" {
			text += " " + c.Value
		}
		if c.Negated {
			text = "not (" + text + ")"
		}
		return text
	}
	parts := []string{}
	for _, term := range terms {
		if term.IsGroup() {
			members := []string{}
			for _, member := range term.Any {
				members = append(members, clause(member))
			}
			parts = append(parts, "("+strings.Join(members, " or ")+")")
		} else {
			parts = append(parts, clause(term.Literal()))
		}
	}
	return strings.Join(parts, " and ")
}

func (site *siteMap) libraryPhrase(node *siteNode, options map[string]any) string {
	if rules, ok := options["text"].([]siteTextRule); ok {
		labels := map[string]string{}
		for _, value := range site.option(node, "rule").Values {
			labels[siteString(value.Value)] = value.Label
		}
		scope := siteString(site.option(node, "scope").Default)
		if name, ok := options["scope"].(string); ok {
			scope = name
		}
		scopeLabel := scope
		for _, value := range site.option(node, "scope").Values {
			if siteString(value.Value) == scope {
				scopeLabel = value.Label
			}
		}
		phrases := []string{}
		for _, rule := range rules {
			phrase := fmt.Sprintf("%s %q", lowerLabel(labels[rule.Kind]), rule.Text)
			if rule.Other != "" {
				phrase += fmt.Sprintf(" and %q", rule.Other)
			}
			if rule.Exclude {
				phrase = "not " + phrase
			}
			phrases = append(phrases, phrase)
		}
		return "search " + lowerLabel(scopeLabel) + " for " + strings.Join(phrases, " and ")
	}
	if search, ok := options["search"].(string); ok {
		kind, _ := options["kind"].(string)
		phrase := fmt.Sprintf("%s search for %q", kind, search)
		if options["case"] == true {
			phrase += ", case-sensitive"
		}
		return phrase
	}
	return site.Datasets[node.Dataset].Label + " table"
}

// arguments are site_link's arguments for a state, so an agent can change
// one and ask again.
func (site *siteMap) arguments(state siteState) map[string]any {
	out := map[string]any{"page": state.Page}
	if state.Card != "" {
		out["card"] = state.Card
		if state.Days > 0 {
			out["days"] = state.Days
		}
		if !state.Changed {
			return out
		}
		options := map[string]any{}
		for key, value := range state.Chart.toMap() {
			if key != "v" {
				options[key] = value
			}
		}
		out["options"] = options
		out["filters"] = append([]qt.WhereTerm{}, state.Filters...)
		return out
	}
	options := map[string]any{}
	if site.Nodes[state.Page].Dashboard != "" {
		for key, value := range state.Chart.toMap() {
			if key != "v" {
				options[key] = value
			}
		}
	}
	for key, value := range state.Options {
		switch v := value.(type) {
		case [2]int64:
			options[key] = []int64{v[0], v[1]}
		default:
			options[key] = value
		}
	}
	if len(options) > 0 {
		out["options"] = options
	}
	if len(state.Filters) > 0 {
		out["filters"] = state.Filters
	}
	if state.Days > 0 {
		out["days"] = state.Days
	} else if site.optionOfKind(site.Nodes[state.Page], "window") != nil {
		out["days"] = "all"
	}
	return out
}
