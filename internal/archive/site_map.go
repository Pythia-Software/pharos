package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// The site map (assets/site-map.json, docs/site-map.md) lists every page the
// UI serves, the options each takes, and for the Analyze dashboards which
// charts the page draws as given. web/generate-site-map.mjs writes it from the
// TypeScript that defines the pages; Go reads it as data and never re-derives
// a page rule. This file loads it, judges dashboard charts by its rules, and
// builds and parses site links (siteLinkFromURL). site_tools.go serves it to
// agents as browse_site and site_link.

type siteMap struct {
	Version   int                       `json:"version"`
	Nodes     map[string]*siteNode      `json:"nodes"`
	IDSources map[string][]siteIDSource `json:"id_sources"`
	Excluded  struct {
		LegacyRoutes map[string]string `json:"legacy_routes"`
		Params       []string          `json:"params"`
	} `json:"excluded"`
	Windows    siteWindows             `json:"windows"`
	Analyze    siteAnalyze             `json:"analyze"`
	Datasets   map[string]*siteDataset `json:"datasets"`
	OpSets     map[string][]string     `json:"op_sets"`
	EmptyQuery json.RawMessage         `json:"empty_query"`
	Cards      struct {
		TextWindows map[string]map[string]string `json:"text_windows"`
		Items       []*siteCard                  `json:"items"`
	} `json:"cards"`

	nodeOrder []string
	cardByID  map[string]*siteCard
}

type siteIDSource struct {
	Tool  string `json:"tool"`
	Field string `json:"field"`
}

type siteNode struct {
	Path        string        `json:"path"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Route       string        `json:"route"`
	Fixed       orderedObject `json:"fixed"`
	Options     []*siteOption `json:"options"`
	Children    []string      `json:"children"`
	Dataset     string        `json:"dataset"`
	Dashboard   string        `json:"dashboard"`
	Cards       bool          `json:"cards"`
	Settings    string        `json:"settings"`
	Builder     string        `json:"builder"`
	Ribbon      *struct {
		Param  string            `json:"param"`
		JSON   orderedObject     `json:"json"`
		Lookup map[string]string `json:"lookup"`
	} `json:"ribbon"`
	BaseQuery           json.RawMessage `json:"base_query"`
	TextBuilderTemplate orderedObject   `json:"text_builder_template"`
	RuleTemplate        orderedObject   `json:"rule_template"`
	ScopeSurfaces       map[string]struct {
		Surface string   `json:"surface"`
		Roles   []string `json:"roles"`
	} `json:"scope_surfaces"`
}

type siteOption struct {
	Name        string      `json:"name"`
	Param       string      `json:"param"`
	Key         string      `json:"key"`
	EndParam    string      `json:"end_param"`
	Kind        string      `json:"kind"`
	Values      []siteValue `json:"values"`
	Default     any         `json:"default"`
	ID          string      `json:"id"`
	Description string      `json:"description"`
	Requires    []struct {
		Option string `json:"option"`
		Values []any  `json:"values"`
	} `json:"requires"`
	Conflicts []string `json:"conflicts"`
	// HostParams are the parameters a where option's table keeps as host
	// query state, which the app's query tokens carry (host-query-state.ts).
	HostParams []string `json:"host_params"`
}

type siteValue struct {
	Value       any    `json:"value"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

type siteWindows struct {
	Days               []int                        `json:"days"`
	Dashboard          []int                        `json:"dashboard"`
	AnalyzeDays        []int                        `json:"analyze_days"`
	AnalyzeDaysDefault int                          `json:"analyze_days_default"`
	Labels             map[string]string            `json:"labels"`
	Tables             map[string]map[string]string `json:"tables"`
	DashboardTerms     []qt.WhereTerm               `json:"dashboard_terms"`
	QueryTerms         []qt.WhereTerm               `json:"query_terms"`
}

type siteAnalyze struct {
	Version      int            `json:"version"`
	FillDefaults map[string]any `json:"fill_defaults"`
	Absent       string         `json:"absent"`
	Fallback     string         `json:"fallback"`
	Labels       struct {
		View   map[string]string `json:"view"`
		Reduce map[string]string `json:"reduce"`
	} `json:"labels"`
	Dashboards map[string]*siteDashboard `json:"dashboards"`
}

type siteDashboard struct {
	Dataset string `json:"dataset"`
	Time    struct {
		First string `json:"first"`
		Last  string `json:"last"`
	} `json:"time"`
	MergeDefaults map[string]any `json:"merge_defaults"`
	Legacy        []struct {
		Key   string         `json:"key"`
		Value any            `json:"value"`
		Set   map[string]any `json:"set"`
	} `json:"legacy"`
	Measures map[string]struct {
		Label          string `json:"label"`
		Unit           string `json:"unit"`
		Kind           string `json:"kind"`
		PerRecordValue bool   `json:"per_record_value"`
		Note           string `json:"note"`
	} `json:"measures"`
	Breakdowns map[string]struct {
		Label  string   `json:"label"`
		Fields []string `json:"fields"`
	} `json:"breakdowns"`
	Components map[string]struct {
		Label    string   `json:"label"`
		Measures []string `json:"measures"`
		Parts    []string `json:"parts"`
	} `json:"components"`
	Units map[string]struct {
		Label      string   `json:"label"`
		Plural     string   `json:"plural"`
		Note       string   `json:"note"`
		Breakdowns []string `json:"breakdowns"`
	} `json:"units"`
	Cycles           map[string]string    `json:"cycles"`
	UnavailableViews map[string]string    `json:"unavailable_views"`
	Defaults         map[string]any       `json:"defaults"`
	Views            map[string]*siteView `json:"views"`

	viewOrder []string
}

type siteView struct {
	Label    string                 `json:"label"`
	Keys     []string               `json:"keys"`
	Defaults map[string]any         `json:"defaults"`
	Domains  map[string][]any       `json:"domains"`
	Numeric  map[string]siteNumeric `json:"numeric"`
	Rules    []siteRule             `json:"rules"`
}

type siteNumeric struct {
	Min       *float64 `json:"min"`
	Max       *float64 `json:"max"`
	Integer   bool     `json:"integer"`
	MaxValues *float64 `json:"max_values"`
}

type siteRule struct {
	Option      string `json:"option"`
	Fill        any    `json:"fill"`
	Constraints []struct {
		Given   []string `json:"given"`
		Allowed any      `json:"allowed"`
	} `json:"constraints"`
	Fallback *struct {
		Given  []string `json:"given"`
		Values any      `json:"values"`
	} `json:"fallback"`
}

type siteDataset struct {
	Label       string      `json:"label"`
	Description string      `json:"description"`
	WindowField string      `json:"window_field"`
	Fields      []siteField `json:"fields"`
}

type siteField struct {
	Name        string      `json:"name"`
	Label       string      `json:"label"`
	Type        string      `json:"type"`
	Description string      `json:"description"`
	Ops         string      `json:"ops"`
	Values      []siteValue `json:"values"`
}

type siteCard struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Category    string         `json:"category"`
	Dataset     string         `json:"dataset"`
	Kind        string         `json:"kind"`
	Page        string         `json:"page"`
	Chart       orderedObject  `json:"chart"`
	State       map[string]any `json:"state"`
	Filters     []qt.WhereTerm `json:"filters"`
	Window      *struct {
		Field       string         `json:"field"`
		Days        []int          `json:"days"`
		DefaultDays []int          `json:"default_days"`
		Terms       []qt.WhereTerm `json:"terms"`
		ChartDays   []string       `json:"chart_days"`
	} `json:"window"`
	URL struct {
		Path   string          `json:"path"`
		Params []siteCardParam `json:"params"`
	} `json:"url"`
}

// siteCardParam is one [parameter, spec] pair of a card's URL template.
type siteCardParam struct {
	Name    string
	Literal *string
	Spec    struct {
		Days        bool              `json:"days"`
		Query       json.RawMessage   `json:"query"`
		Card        string            `json:"card"`
		CardChart   bool              `json:"card_chart"`
		CardFilters bool              `json:"card_filters"`
		JSON        orderedObject     `json:"json"`
		Lookup      map[string]string `json:"lookup"`
	}
}

func (p *siteCardParam) UnmarshalJSON(data []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(data, &pair); err != nil || len(pair) != 2 {
		return fmt.Errorf("site map: a card parameter is not a [name, spec] pair")
	}
	if err := json.Unmarshal(pair[0], &p.Name); err != nil {
		return err
	}
	var literal string
	if json.Unmarshal(pair[1], &literal) == nil {
		p.Literal = &literal
		return nil
	}
	return json.Unmarshal(pair[1], &p.Spec)
}

// orderedObject is a JSON object that keeps its members' order, for the
// JSON parameters a link writes as the page does (JSON.stringify keeps
// insertion order). Members stay raw, so nested objects keep theirs too.
type orderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

func (o *orderedObject) UnmarshalJSON(data []byte) error {
	o.keys, o.values = nil, map[string]json.RawMessage{}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return fmt.Errorf("not a JSON object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if _, seen := o.values[key]; !seen {
			o.keys = append(o.keys, key)
		}
		o.values[key] = value
	}
	_, err = decoder.Token()
	return err
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for index, key := range o.keys {
		if index > 0 {
			out.WriteByte(',')
		}
		name, _ := json.Marshal(key)
		out.Write(name)
		out.WriteByte(':')
		if err := json.Compact(&out, o.values[key]); err != nil {
			return nil, err
		}
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func (o *orderedObject) set(key string, value any) {
	if o.values == nil {
		o.values = map[string]json.RawMessage{}
	}
	if _, seen := o.values[key]; !seen {
		o.keys = append(o.keys, key)
	}
	o.values[key] = json.RawMessage(compactJSON(value))
}

func (o orderedObject) get(key string) (any, bool) {
	raw, ok := o.values[key]
	if !ok {
		return nil, false
	}
	var value any
	_ = json.Unmarshal(raw, &value)
	return value, true
}

func (o orderedObject) clone() orderedObject {
	out := orderedObject{keys: append([]string(nil), o.keys...), values: map[string]json.RawMessage{}}
	for key, value := range o.values {
		out.values[key] = value
	}
	return out
}

// compactJSON is JSON.stringify's output: no spaces and no HTML escaping.
func compactJSON(value any) string {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(out.String(), "\n")
}

// objectKeys lists the members of a JSON object in order.
func objectKeys(raw json.RawMessage) []string {
	var object orderedObject
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	return object.keys
}

var (
	siteMapOnce   sync.Once
	siteMapLoaded *siteMap
	siteMapErr    error
)

// loadedSiteMap reads the embedded site map once.
func loadedSiteMap() (*siteMap, error) {
	siteMapOnce.Do(func() {
		raw, err := assets.ReadFile("assets/site-map.json")
		if err != nil {
			siteMapErr = err
			return
		}
		siteMapLoaded, siteMapErr = parseSiteMap(raw)
	})
	return siteMapLoaded, siteMapErr
}

func parseSiteMap(raw []byte) (*siteMap, error) {
	var site siteMap
	if err := json.Unmarshal(raw, &site); err != nil {
		return nil, fmt.Errorf("site map: %w", err)
	}
	var top struct {
		Nodes   json.RawMessage `json:"nodes"`
		Analyze struct {
			Dashboards map[string]struct {
				Views json.RawMessage `json:"views"`
			} `json:"dashboards"`
		} `json:"analyze"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	site.nodeOrder = objectKeys(top.Nodes)
	for topic, dashboard := range site.Analyze.Dashboards {
		dashboard.viewOrder = objectKeys(top.Analyze.Dashboards[topic].Views)
	}
	site.cardByID = map[string]*siteCard{}
	for _, card := range site.Cards.Items {
		site.cardByID[card.ID] = card
	}
	for _, path := range site.nodeOrder {
		node := site.Nodes[path]
		if node.Dataset != "" && site.Datasets[node.Dataset] == nil {
			return nil, fmt.Errorf("site map: %s reads unknown dataset %s", path, node.Dataset)
		}
		if node.Dashboard != "" && site.Analyze.Dashboards[node.Dashboard] == nil {
			return nil, fmt.Errorf("site map: %s draws unknown dashboard %s", path, node.Dashboard)
		}
	}
	return &site, nil
}

// ----- Values ------------------------------------------------------------------

// scalar normalizes a JSON scalar: every number is a float64.
func scalar(value any) any {
	switch v := value.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return value
}

// sameValue is JavaScript's strict equality on JSON scalars, and equality
// of JSON text for objects and arrays.
func sameValue(a, b any) bool {
	a, b = scalar(a), scalar(b)
	switch a.(type) {
	case map[string]any, []any:
		return compactJSON(a) == compactJSON(b)
	}
	switch b.(type) {
	case map[string]any, []any:
		return false
	}
	return a == b
}

// jsString is String(value) for a JSON scalar.
func siteString(value any) string {
	switch v := scalar(value).(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case nil:
		return "null"
	}
	return compactJSON(value)
}

func jsNumber(value any) float64 {
	switch v := scalar(value).(type) {
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}

func lookupTable(table any, keys []string) any {
	node := table
	for _, key := range keys {
		object, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = object[key]
	}
	return node
}

func listHolds(list any, value any) bool {
	items, ok := list.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if sameValue(item, value) {
			return true
		}
	}
	return false
}

func mergeChart(base, chart map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(chart))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range chart {
		merged[key] = value
	}
	return merged
}

// ----- Dashboard charts (docs/site-map.md, "Valid combinations") ------------------

const (
	verdictValid     = "valid"
	verdictCoercible = "coercible"
	verdictInvalid   = "invalid"
)

// readChart is the chart as the page reads it: over merge_defaults, with an
// older name read as the current one.
func (d *siteDashboard) readChart(chart map[string]any) (map[string]any, bool) {
	original := mergeChart(d.MergeDefaults, chart)
	merged, renamed := mergeChart(original, nil), false
	for _, entry := range d.Legacy {
		if compactJSON(original[entry.Key]) == compactJSON(entry.Value) {
			renamed = true
			for key, value := range entry.Set {
				merged[key] = value
			}
		}
	}
	return merged, renamed
}

func (d *siteDashboard) viewName(merged map[string]any) string {
	if view, ok := merged["view"]; ok && view != nil {
		return siteString(view)
	}
	return "trend"
}

// chartProblem says why a chart is not drawn as given: the key, and for a
// coercible one, what the page draws instead.
type chartProblem struct {
	Key    string
	Reason string
	Drawn  any
	drawn  bool
}

// judge returns the page's verdict on a chart, and the first problem.
func (site *siteMap) judge(d *siteDashboard, chart map[string]any) (string, *chartProblem) {
	merged, renamed := d.readChart(chart)
	viewName := d.viewName(merged)
	view := d.Views[viewName]
	if view == nil {
		return verdictInvalid, &chartProblem{Key: "view", Reason: fmt.Sprintf("%q is not a view of this dashboard", viewName)}
	}
	if v, ok := merged["v"]; ok && v != nil && !sameValue(v, float64(site.Analyze.Version)) {
		return verdictInvalid, &chartProblem{Key: "v", Reason: fmt.Sprintf("v must be %d", site.Analyze.Version)}
	} else if ok && v == nil {
		return verdictInvalid, &chartProblem{Key: "v", Reason: fmt.Sprintf("v must be %d", site.Analyze.Version)}
	}
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "v" || name == "view" {
			continue
		}
		if reason := view.checkValue(name, merged[name]); reason != "" {
			return verdictInvalid, &chartProblem{Key: name, Reason: reason}
		}
	}
	if _, hasV := merged["v"]; !hasV {
		return verdictCoercible, &chartProblem{Key: "v", Reason: "a chart without v is an older link, which the page migrates"}
	}
	if renamed {
		return verdictCoercible, &chartProblem{Key: "metric", Reason: "the chart uses an older name, which the page renames"}
	}
	drawn := site.drawChart(view, merged)
	for _, name := range view.Keys {
		value, given := chart[name]
		got, isDrawn := drawn[name]
		if given && isDrawn && !sameValue(got, value) {
			return verdictCoercible, &chartProblem{Key: name, Reason: "the page draws another value", Drawn: got, drawn: true}
		}
	}
	return verdictValid, nil
}

// checkValue is the domain check validAnalysisState makes on one key; "" when
// the value is accepted.
func (view *siteView) checkValue(name string, value any) string {
	numeric, hasNumeric := view.Numeric[name]
	if name == "selection" {
		limit := 0.0
		if hasNumeric && numeric.MaxValues != nil {
			limit = *numeric.MaxValues
		}
		object, ok := value.(map[string]any)
		if !ok {
			return "selection must be {values: [series names], excluded: true or false}"
		}
		if _, ok := object["excluded"].(bool); !ok {
			return "selection.excluded must be true or false"
		}
		values, ok := object["values"].([]any)
		if !ok {
			return "selection.values must be a list of series names"
		}
		if float64(len(values)) > limit {
			return fmt.Sprintf("selection holds at most %d values", int(limit))
		}
		for _, item := range values {
			if _, ok := item.(string); !ok {
				return "selection.values must be strings"
			}
		}
		return ""
	}
	if hasNumeric && numeric.Min != nil {
		number, ok := scalar(value).(float64)
		if !ok || number < *numeric.Min || numeric.Max != nil && number > *numeric.Max || numeric.Integer && number != float64(int64(number)) {
			return "must be " + numeric.describe()
		}
		return ""
	}
	if domain, ok := view.Domains[name]; ok && !listHolds(any(domain), value) {
		return fmt.Sprintf("%s is not a choice of this view", compactJSON(value))
	}
	return ""
}

func (n siteNumeric) describe() string {
	kind := "a number"
	if n.Integer {
		kind = "an integer"
	}
	switch {
	case n.Min != nil && n.Max != nil:
		return fmt.Sprintf("%s from %s to %s", kind, siteString(*n.Min), siteString(*n.Max))
	case n.Min != nil:
		return fmt.Sprintf("%s of at least %s", kind, siteString(*n.Min))
	}
	return kind
}

// multiplePeriods: the merge over fill_defaults has period hours with
// hours > 1, or days with days > 1.
func (site *siteMap) multiplePeriods(merged map[string]any) bool {
	state := mergeChart(site.Analyze.FillDefaults, merged)
	period := siteString(state["period"])
	return period == "hours" && jsNumber(state["hours"]) > 1 || period == "days" && jsNumber(state["days"]) > 1
}

// ruleContext evaluates given option names for a rule, as drawChart does.
type ruleContext struct {
	site   *siteMap
	merged map[string]any
	drawn  map[string]any
}

func (c ruleContext) key(value any, present bool) string {
	if !present {
		return c.site.Analyze.Absent
	}
	return siteString(value)
}

func (c ruleContext) value(name string, self any, selfPresent bool) string {
	switch name {
	case "self":
		return c.key(self, selfPresent)
	case "multiple_periods":
		return strconv.FormatBool(c.site.multiplePeriods(c.merged))
	}
	if value, ok := c.drawn[name]; ok {
		return c.key(value, value != nil)
	}
	value, ok := c.merged[name]
	return c.key(value, ok && value != nil)
}

func (c ruleContext) values(names []string, self any, selfPresent bool) []string {
	out := make([]string, len(names))
	for index, name := range names {
		out[index] = c.value(name, self, selfPresent)
	}
	return out
}

// drawChart applies the view's rules to the merged chart in order. A drawn
// key with a nil value is drawn as undefined.
func (site *siteMap) drawChart(view *siteView, merged map[string]any) map[string]any {
	drawn := map[string]any{}
	context := ruleContext{site: site, merged: merged, drawn: drawn}
	for _, rule := range view.Rules {
		own, present := merged[rule.Option]
		present = present && own != nil
		fallback := func() any {
			if rule.Fallback == nil {
				return nil
			}
			if len(rule.Fallback.Given) == 0 {
				return rule.Fallback.Values
			}
			return lookupTable(rule.Fallback.Values, context.values(rule.Fallback.Given, own, present))
		}
		if !present && rule.Fill == site.Analyze.Fallback {
			drawn[rule.Option] = fallback()
			continue
		}
		if !present && rule.Fill != nil {
			own, present = rule.Fill, true
		}
		allowed := present
		for _, constraint := range rule.Constraints {
			if !allowed {
				break
			}
			allowed = listHolds(lookupTable(constraint.Allowed, context.values(constraint.Given, own, present)), own)
		}
		if allowed {
			drawn[rule.Option] = own
		} else {
			drawn[rule.Option] = fallback()
		}
	}
	return drawn
}

// allowedHere lists the values a rule allows for its option given the
// other options' values in this chart, and those values in words, for an
// error that lists the choices.
func (site *siteMap) allowedHere(view *siteView, merged map[string]any, option string) ([]any, []string) {
	for index, rule := range view.Rules {
		if rule.Option != option {
			continue
		}
		context := ruleContext{site: site, merged: merged, drawn: site.drawChart(&siteView{Rules: view.Rules[:index]}, merged)}
		var givens []string
		for _, constraint := range rule.Constraints {
			for _, name := range constraint.Given {
				switch name {
				case "self":
				case "multiple_periods":
					if site.multiplePeriods(merged) {
						givens = append(givens, "each period spans several hours or days")
					} else {
						givens = append(givens, "each period is one unit")
					}
				default:
					givens = append(givens, fmt.Sprintf("%s is %s", name, context.value(name, nil, false)))
				}
			}
		}
		allowed := []any{}
		for _, candidate := range view.Domains[option] {
			ok := true
			for _, constraint := range rule.Constraints {
				if !listHolds(lookupTable(constraint.Allowed, context.values(constraint.Given, candidate, true)), candidate) {
					ok = false
					break
				}
			}
			if ok {
				allowed = append(allowed, candidate)
			}
		}
		return allowed, uniqueStrings(givens)
	}
	return view.Domains[option], nil
}

// viewChart is the chart Go writes: older names read as current ones, then
// only the keys its view draws, in the view's order, and a non-empty legend
// selection (docs/site-map.md, "Go writes current names and only the view's
// keys").
func (d *siteDashboard) viewChart(chart map[string]any) orderedObject {
	merged := mergeChart(d.MergeDefaults, chart)
	current := mergeChart(chart, nil)
	for _, entry := range d.Legacy {
		if compactJSON(merged[entry.Key]) == compactJSON(entry.Value) {
			for key, value := range entry.Set {
				current[key] = value
			}
		}
	}
	viewName := "trend"
	if view, ok := current["view"]; ok && view != nil {
		viewName = siteString(view)
	} else if view, ok := d.MergeDefaults["view"]; ok && view != nil {
		viewName = siteString(view)
	}
	var out orderedObject
	view := d.Views[viewName]
	if view == nil {
		return out
	}
	for _, key := range view.Keys {
		if value, ok := current[key]; ok {
			out.set(key, value)
		}
	}
	if selection, ok := current["selection"].(map[string]any); ok {
		if values, ok := selection["values"].([]any); ok && len(values) > 0 {
			out.set("selection", selection)
		}
	}
	return out
}

func (o orderedObject) toMap() map[string]any {
	out := map[string]any{}
	for _, key := range o.keys {
		value, _ := o.get(key)
		out[key] = value
	}
	return out
}

// ----- Encoding ----------------------------------------------------------------

// siteParams is an ordered query string. Go writes parameters as
// URLSearchParams does: space as +, * kept and ~ encoded, unlike
// url.Values.Encode, which also sorts the keys.
type siteParams [][2]string

func (p *siteParams) add(key, value string) { *p = append(*p, [2]string{key, value}) }

func (p siteParams) encode() string {
	parts := make([]string, len(p))
	for index, pair := range p {
		parts[index] = formEscape(pair[0]) + "=" + formEscape(pair[1])
	}
	return strings.Join(parts, "&")
}

func formEscape(value string) string {
	escaped := url.QueryEscape(value)
	return strings.ReplaceAll(strings.ReplaceAll(escaped, "%2A", "*"), "~", "%7E")
}

func siteURL(path string, params siteParams) string {
	if len(params) == 0 {
		return path
	}
	return path + "?" + params.encode()
}

// fillWindowJSON replaces {window_days} in a template with the window.
func fillWindowJSON(raw []byte, days int) []byte {
	return bytes.ReplaceAll(raw, []byte("{window_days}"), []byte(strconv.Itoa(days)))
}

// windowTerms fills a window term template: {window_days} with the days and
// each other placeholder with its field.
func windowTerms(template []qt.WhereTerm, days int, fields map[string]string) []qt.WhereTerm {
	out := make([]qt.WhereTerm, len(template))
	for index, term := range template {
		term.Value = strings.ReplaceAll(term.Value, "{window_days}", strconv.Itoa(days))
		if field, ok := fields[term.Field]; ok {
			term.Field = field
		}
		out[index] = term
	}
	return out
}

// queryToken encodes a query template (members equal to empty_query left
// out, as the site map stores them) merged over empty_query.
func (site *siteMap) queryToken(template json.RawMessage, where []qt.WhereTerm) (string, error) {
	query, err := site.expandQuery(template)
	if err != nil {
		return "", err
	}
	if where != nil {
		query.Where = where
	}
	return qt.EncodeQueryLink(query)
}

func (site *siteMap) expandQuery(template json.RawMessage) (qt.LinkQuery, error) {
	var members map[string]any
	if len(template) > 0 {
		if err := json.Unmarshal(template, &members); err != nil {
			return qt.LinkQuery{}, err
		}
	}
	var query qt.LinkQuery
	err := json.Unmarshal([]byte(compactJSON(site.expandMembers(members))), &query)
	return query, err
}

// expandMembers merges a query template over empty_query, and its
// grouping's source too. It stays JSON, so nested where terms keep every
// member the codec reads (an empty value included).
func (site *siteMap) expandMembers(members map[string]any) map[string]any {
	merged := map[string]any{}
	_ = json.Unmarshal(site.EmptyQuery, &merged)
	for key, value := range members {
		merged[key] = value
	}
	if grouping, ok := members["grouping"].(map[string]any); ok {
		if source, ok := grouping["source"].(map[string]any); ok {
			expanded := mergeChart(grouping, nil)
			expanded["source"] = site.expandMembers(source)
			merged["grouping"] = expanded
		}
	}
	return merged
}

// sameQueryToken compares two query tokens by their decoded state, since
// deflate bytes differ between implementations. An empty host query state
// (which tables with host clauses write as their default) is no state.
func sameQueryToken(a, b string) bool {
	left, err := qt.DecodeQueryLink(a)
	if err != nil {
		return false
	}
	right, err := qt.DecodeQueryLink(b)
	if err != nil {
		return false
	}
	for _, query := range []*qt.LinkQuery{&left, &right} {
		if query.HostState != nil && query.HostState.Version >= 1 && len(query.HostState.Params) == 0 {
			query.HostState = nil
		}
	}
	return compactJSON(left) == compactJSON(right)
}

func sameJSONText(a, b string) bool {
	var left, right any
	if json.Unmarshal([]byte(a), &left) != nil || json.Unmarshal([]byte(b), &right) != nil {
		return false
	}
	return compactJSON(left) == compactJSON(right)
}

// sameSiteParams compares two links' parameters: query tokens by decoded
// state, JSON parameters by value, others as text. Order does not matter.
func sameSiteParams(a, b url.Values) bool {
	if len(a) != len(b) {
		return false
	}
	for key, values := range a {
		other, ok := b[key]
		if !ok || len(values) != 1 || len(other) != 1 {
			return false
		}
		if !sameParamValue(key, values[0], other[0]) {
			return false
		}
	}
	return true
}

// sameSiteParamsExcept is sameSiteParams over the parameters other than
// skip, which both links must still carry.
func sameSiteParamsExcept(a, b url.Values, skip []string) bool {
	trim := func(values url.Values) url.Values {
		out := url.Values{}
		for key, value := range values {
			if !slices.Contains(skip, key) {
				out[key] = value
			}
		}
		return out
	}
	for _, key := range skip {
		if !a.Has(key) || !b.Has(key) {
			return false
		}
	}
	return sameSiteParams(trim(a), trim(b))
}

func sameParamValue(key, a, b string) bool {
	if a == b {
		return true
	}
	if key == "analyze_filters" || strings.HasPrefix(key, "q_") {
		return sameQueryToken(a, b)
	}
	if strings.HasPrefix(a, "{") || strings.HasPrefix(a, "[") {
		return sameJSONText(a, b)
	}
	return false
}
