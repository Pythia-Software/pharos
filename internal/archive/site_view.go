package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

type siteViewField struct {
	Param          string   `json:"param"`
	Pages          []string `json:"pages"`
	Kind           string   `json:"kind"`
	Description    string   `json:"description"`
	Values         []string `json:"values,omitempty"`
	Default        any      `json:"default,omitempty"`
	Details        any      `json:"details,omitempty"`
	CaptureEntries bool     `json:"captureEntries,omitempty"`
}
type siteTarget struct {
	Pattern     string   `json:"pattern"`
	Pages       []string `json:"pages"`
	Description string   `json:"description"`
	RevealOnly  bool     `json:"revealOnly,omitempty"`
}
type siteViewContract struct {
	Dependencies map[string]any           `json:"dependencies"`
	Version      int                      `json:"version"`
	Fields       map[string]siteViewField `json:"fields"`
	Targets      []siteTarget             `json:"targets"`
	Sections     map[string][]string      `json:"sections"`
	Excluded     map[string]string        `json:"excluded"`
}

func viewPageMatch(pattern, page string) bool {
	return pattern == "*" || pattern == page || strings.HasSuffix(pattern, "/*") && strings.HasPrefix(page, strings.TrimSuffix(pattern, "*"))
}
func viewApplies(pages []string, page string) bool {
	for _, pattern := range pages {
		if viewPageMatch(pattern, page) {
			return true
		}
	}
	return false
}
func (site *siteMap) viewFields(page string) map[string]siteViewField {
	out := map[string]siteViewField{}
	for key, field := range site.ViewContract.Fields {
		if viewApplies(field.Pages, page) {
			if node := site.Nodes[page]; node != nil {
				field.Param = strings.ReplaceAll(field.Param, "{dataset}", node.Dataset)
			}
			out[key] = field
		}
	}
	return out
}
func viewValue(field siteViewField, value any) (string, error) {
	var raw string
	switch field.Kind {
	case "boolean":
		v, ok := value.(bool)
		if !ok {
			return "", fmt.Errorf("needs a boolean")
		}
		if v {
			raw = "1"
		} else {
			raw = "0"
		}
	case "enum", "text":
		v, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("needs text")
		}
		raw = v
		if field.Kind == "enum" && !slices.Contains(field.Values, v) {
			return "", fmt.Errorf("choose one of %s", strings.Join(field.Values, ", "))
		}
	case "json", "strings":
		b, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		raw = string(b)
		if field.Kind == "strings" {
			var values []any
			if err := json.Unmarshal(b, &values); err != nil || values == nil {
				return "", fmt.Errorf("needs an array of strings")
			}
			for _, value := range values {
				if _, ok := value.(string); !ok {
					return "", fmt.Errorf("needs an array of strings")
				}
			}
		} else if _, ok := value.(map[string]any); !ok {
			if _, ok := value.([]any); !ok {
				return "", fmt.Errorf("needs JSON state")
			}
		}
	default:
		return "", fmt.Errorf("unknown view kind %s", field.Kind)
	}
	if len(raw) > 100000 {
		return "", fmt.Errorf("is too large")
	}
	return raw, nil
}
func (site *siteMap) checkAttention(page, target string, query *qt.LinkQuery) error {
	choices := []string{}
	for _, family := range site.ViewContract.Targets {
		if viewApplies(family.Pages, page) {
			choices = append(choices, family.Pattern)
		}
	}
	if strings.HasPrefix(target, "detail:") && slices.Contains(choices, "detail:<json>") {
		return site.checkDetailAttention(page, target, query)
	}
	if id, ok := strings.CutPrefix(target, "card:"); ok && site.cardByID[id] == nil {
		ids := []string{}
		for id := range site.cardByID {
			ids = append(ids, "card:"+id)
		}
		sort.Strings(ids)
		return linkError("attention", "unknown card ID; browse cards", ids...)
	}
	if id, ok := strings.CutPrefix(target, "control:"); ok && !strings.HasPrefix(id, "chart:") {
		fields := site.viewFields(page)
		native := site.optionOfKind(site.Nodes[page], "where")
		if _, exists := fields[id]; !exists && (native == nil || id != native.Param || !strings.HasPrefix(id, "q_")) {
			controls := []string{}
			for key := range fields {
				controls = append(controls, "control:"+key)
			}
			if native != nil && strings.HasPrefix(native.Param, "q_") {
				controls = append(controls, "control:"+native.Param)
			}
			sort.Strings(controls)
			return linkError("attention", "unknown view control; browse "+page+"/view", controls...)
		}
	} else if ok && !strings.HasPrefix(page, "analyze/") {
		return linkError("attention", "chart controls need an Analyze page", choices...)
	}
	for _, family := range site.ViewContract.Targets {
		if !viewApplies(family.Pages, page) {
			continue
		}
		prefix, _, dynamic := strings.Cut(family.Pattern, "<")
		if target == family.Pattern || dynamic && strings.HasPrefix(target, prefix) && len(target) > len(prefix) && len(target) < 4096 {
			return nil
		}
	}
	return linkError("attention", fmt.Sprintf("%q is not a target on %s; browse %s/targets", target, page, page), choices...)
}

func (site *siteMap) checkDetailAttention(page, reference string, query *qt.LinkQuery) error {
	node := site.Nodes[page]
	if node == nil || node.Dataset == "" || site.optionOfKind(node, "where") == nil || !strings.HasPrefix(site.optionOfKind(node, "where").Param, "q_") {
		return linkError("reveal", "detail targets need a query-table page")
	}
	if jsTextLength(reference) >= 4096 {
		return linkError("reveal", "detail target is too large")
	}
	var target map[string]any
	if json.Unmarshal([]byte(strings.TrimPrefix(reference, "detail:")), &target) != nil || target == nil {
		return linkError("reveal", `needs detail:{"rowId":"id","field":"property","path":"$"}; field/path are optional`)
	}
	for name := range target {
		if !slices.Contains([]string{"rowId", "field", "path"}, name) {
			return linkError("reveal", "unknown detail target member "+name, "rowId", "field", "path")
		}
	}
	switch id := target["rowId"].(type) {
	case string:
		if id == "" {
			return linkError("reveal", "needs a nonempty rowId")
		}
	case float64:
	default:
		return linkError("reveal", "rowId needs a string or number; preserve its type")
	}
	field, hasField := target["field"]
	if hasField {
		name, ok := field.(string)
		if !ok || name == "" {
			return linkError("reveal", "field needs a property name")
		}
		found, choices := false, []string{}
		properties := site.Datasets[node.Dataset].Properties
		if query != nil && query.Grouping != nil {
			result, err := site.checkQueryLevel(node, site.Datasets[node.Dataset], *query, "query", 0)
			if err != nil {
				return err
			}
			properties = result.Columns
		}
		for _, property := range properties {
			candidate := siteString(property["name"])
			choices = append(choices, candidate)
			found = found || candidate == name
		}
		if !found {
			return linkError("reveal.field", "unknown property; browse "+page+"/query", choices...)
		}
	}
	if path, given := target["path"]; given {
		value, ok := path.(string)
		if !ok || !hasField {
			return linkError("reveal.path", "JSONPath needs a field and a bracket JSONPath")
		}
		// The published link codec owns JSONPath validation in both languages.
		state := qt.NewJSONInspectionState()
		state.CollapsedPaths = []string{value}
		entry := qt.ScopedJSONInspectionState{Dataset: site.Datasets[node.Dataset].SchemaName, RowID: target["rowId"], Field: field.(string), State: state}
		details, _ := json.Marshal(map[string]any{"modes": []string{"expand"}, "jsonInspections": []qt.ScopedJSONInspectionState{entry}})
		if _, err := qt.EncodeQueryLink(qt.LinkQuery{ItemDetails: details}); err != nil {
			return linkError("reveal.path", err.Error())
		}
	}
	return nil
}
func (site *siteMap) build(state siteState) (string, error) {
	view, query, highlights, reveal := state.View, state.Query, state.Highlight, state.Reveal
	state.View, state.Query, state.Highlight, state.Reveal = nil, nil, nil, ""
	raw, err := site.buildBase(state)
	if err != nil {
		return "", err
	}
	if len(view) == 0 && query == nil && len(highlights) == 0 && reveal == "" {
		return raw, nil
	}
	u, _ := url.Parse(raw)
	values := u.Query()
	if query != nil {
		if err := site.checkNativeQuery(state.Page, *query); err != nil {
			return "", err
		}
		if query.HostState != nil && query.HostState.Version != 1 {
			return "", linkError("query.hostState", "unsupported host state version")
		}
		option := site.optionOfKind(site.Nodes[state.Page], "where")
		if option == nil || !strings.HasPrefix(option.Param, "q_") {
			return "", linkError("query", "needs a query-table page")
		}
		copy := *query
		query = &copy
		if query.HostState != nil {
			host := *query.HostState
			host.Params = map[string][]string{}
			for key, list := range query.HostState.Params {
				host.Params[key] = slices.Clone(list)
			}
			query.HostState = &host
			for key, list := range query.HostState.Params {
				if !slices.Contains(option.HostParams, key) {
					return "", linkError("query.hostState", "unknown host parameter "+key)
				}
				if len(list) != 1 {
					return "", linkError("query.hostState", "host parameters need one value")
				}
				values[key] = list
			}
		}
	}
	fields := site.viewFields(state.Page)
	for key, value := range view {
		field, ok := fields[key]
		if !ok {
			choices := []string{}
			for name := range fields {
				choices = append(choices, name)
			}
			sort.Strings(choices)
			return "", linkError("view."+key, "is not a view field on "+state.Page+"; browse "+state.Page+"/view", choices...)
		}
		if err := validateViewShape(key, value); err != nil {
			return "", linkError("view."+key, err.Error(), field.Values...)
		}
		if key == "record_keyword" {
			clause := value.(map[string]any)
			valid := false
			for _, candidate := range site.Datasets[site.Nodes[state.Page].Dataset].Fields {
				if candidate.Name == clause["field"] && (candidate.Type == "text" || candidate.Type == "enum") && !strings.HasPrefix(candidate.Name, "@computed/") {
					valid = true
				}
			}
			if !valid {
				return "", linkError("view.record_keyword.field", "needs a source text or enum field")
			}
		}
		encoded, err := viewValue(field, value)
		if err != nil {
			return "", linkError("view."+key, err.Error(), field.Values...)
		}
		if previous, exists := values[field.Param]; exists && (len(previous) != 1 || previous[0] != encoded && !sameJSONText(previous[0], encoded)) {
			return "", linkError("view."+key, "disagrees with the page option or query host state")
		}
		if query != nil && query.HostState != nil {
			option := site.optionOfKind(site.Nodes[state.Page], "where")
			if slices.Contains(option.HostParams, field.Param) {
				if list, exists := query.HostState.Params[field.Param]; exists && (len(list) != 1 || list[0] != encoded && !sameJSONText(list[0], encoded)) {
					return "", linkError("view."+key, "disagrees with query.hostState")
				}
				query.HostState.Params[field.Param] = []string{encoded}
			}
		}
		values.Set(field.Param, encoded)
	}
	if query != nil {
		token, err := qt.EncodeQueryLink(*query)
		if err != nil {
			return "", err
		}
		option := site.optionOfKind(site.Nodes[state.Page], "where")
		values.Set(option.Param, token)
		if err := site.checkHostState(option.Param, query.HostState, site.Nodes[state.Page], values); err != nil {
			return "", err
		}
	}
	for _, target := range highlights {
		for _, family := range site.ViewContract.Targets {
			prefix, _, dynamic := strings.Cut(family.Pattern, "<")
			if family.RevealOnly && viewApplies(family.Pages, state.Page) && (target == family.Pattern || dynamic && strings.HasPrefix(target, prefix)) {
				return "", linkError("highlight", family.Pattern+" supports reveal only; browse "+state.Page+"/targets")
			}
		}
		if err := site.checkAttention(state.Page, target, query); err != nil {
			return "", err
		}
	}
	if reveal != "" {
		if err := site.checkAttention(state.Page, reveal, query); err != nil {
			return "", err
		}
		values.Set("reveal", reveal)
	}
	if len(highlights) > 0 {
		values["highlight"] = slices.Compact(slices.Sorted(slices.Values(highlights)))
	}
	state.View = view
	if err := site.validateViewWindow(state, values); err != nil {
		return "", err
	}
	u.RawQuery = values.Encode()
	return u.String(), nil
}
func (site *siteMap) linkFromArgs(args map[string]any) (siteState, error) {
	if _, ok := args["url"]; ok {
		return site.linkBaseFromArgs(args)
	}
	base := map[string]any{}
	for key, value := range args {
		if !slices.Contains([]string{"view", "query", "highlight", "reveal"}, key) {
			base[key] = value
		}
	}
	state, err := site.linkBaseFromArgs(base)
	if err != nil {
		return state, err
	}
	if raw, ok := args["view"]; ok {
		value, ok := raw.(map[string]any)
		if !ok {
			return state, linkError("view", "needs an object")
		}
		state.View = site.completeViewArguments(value)
	}
	for _, field := range []string{"window_timezone", "dashboard_timezone"} {
		if pin, given := state.View[field]; given {
			zone, ok := pin.(string)
			location, err := viewTimezone(zone)
			if !ok || err != nil {
				return state, linkError("view."+field, "needs an IANA timezone")
			}
			if state.TodayFrom != "" {
				state.TodayTimezone = zone
				state.TodayFrom = localMidnight(time.Now(), location).UTC().Format(time.RFC3339Nano)
			}
		}
	}
	if raw, ok := args["query"]; ok {
		if args["filters"] != nil || args["card"] != nil {
			return state, linkError("query", "give without filters or card; days may add a moving window to a query without time bounds; browse "+state.Page+"/query")
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return state, err
		}
		var query qt.LinkQuery
		if err = json.Unmarshal(b, &query); err != nil {
			return state, nativeQueryShapeError(err, state.Page)
		}
		if err := site.checkNativeQuery(state.Page, query); err != nil {
			return state, err
		}
		if args["days"] != nil {
			if err := site.addNativeWindow(&state, &query); err != nil {
				return state, err
			}
		}
		token, err := qt.EncodeQueryLink(query)
		if err != nil {
			return state, linkError("query", err.Error())
		}
		query, err = qt.DecodeQueryLink(token)
		if err != nil {
			return state, err
		}
		if err := site.checkNativeQuery(state.Page, query); err != nil {
			return state, err
		}
		state.Query = &query
	}
	if raw, ok := args["highlight"]; ok {
		b, _ := json.Marshal(raw)
		if json.Unmarshal(b, &state.Highlight) != nil {
			return state, linkError("highlight", "needs an array of target references")
		}
	}
	if raw, ok := args["reveal"]; ok {
		value, ok := raw.(string)
		if !ok {
			return state, linkError("reveal", "needs one target reference")
		}
		state.Reveal = value
	}
	// Building validates the contract and target compatibility before returning.
	if _, err := site.build(state); err != nil {
		return state, err
	}
	return state, nil
}
func (site *siteMap) parseURL(raw string) (siteState, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return siteState{}, err
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return siteState{}, err
	}
	for key, list := range values {
		if len(list) != 1 && key != "highlight" {
			return siteState{}, linkError("url", "repeats the parameter "+key)
		}
	}
	// The reader uses this canonical nested route. Keep the existing public work
	// builder's spelling while accepting both forms and preserving the identity.
	if parts := strings.Split(strings.Trim(u.Path, "/"), "/"); len(parts) == 4 && parts[0] == "work" && parts[2] == "conversation" {
		if values.Has("conversation") && values.Get("conversation") != parts[3] {
			return siteState{}, linkError("url", "conversation path and parameter disagree")
		}
		u.Path = "/work/" + parts[1]
		values.Set("conversation", parts[3])
	}
	highlights, reveal := slices.Clone(values["highlight"]), values.Get("reveal")
	values.Del("highlight")
	values.Del("reveal")
	// Preserve prebuilt-card identity and the compact arguments of existing
	// links whenever the original parser already represents the whole view.
	u.RawQuery = values.Encode()
	if state, err := site.parseBaseURL(u.String()); err == nil {
		state.Highlight, state.Reveal = highlights, reveal
		if _, err := site.build(state); err != nil {
			return state, err
		}
		return state, nil
	}
	// Global presentation defaults are written on every page. Removing only
	// those first preserves native card identity and ribbon templates.
	common := url.Values{}
	for name, list := range values {
		common[name] = slices.Clone(list)
	}
	globals := map[string]any{}
	for name, field := range site.ViewContract.Fields {
		if !slices.Contains(field.Pages, "*") || !common.Has(field.Param) {
			continue
		}
		value, err := decodeViewValue(field, common.Get(field.Param))
		if err != nil {
			return siteState{}, linkError("url", err.Error())
		}
		globals[name] = value
		common.Del(field.Param)
	}
	if len(globals) > 0 {
		candidate := *u
		candidate.RawQuery = common.Encode()
		if state, err := site.parseBaseURL(candidate.String()); err == nil {
			state.View, state.Highlight, state.Reveal = globals, highlights, reveal
			if _, err := site.build(state); err != nil {
				return state, linkError("url", err.Error())
			}
			return state, nil
		}
	}
	// Resolve the page with its fixed route parameters before handling view fields.
	var node *siteNode
	for _, name := range site.nodeOrder {
		candidate := site.Nodes[name]
		if _, ok := matchRoute(candidate.Route, u.Path); !ok {
			continue
		}
		matches := true
		for _, key := range candidate.Fixed.keys {
			v, _ := candidate.Fixed.get(key)
			if values.Get(key) != siteString(v) {
				matches = false
			}
		}
		if matches && (node == nil || len(candidate.Fixed.keys) > len(node.Fixed.keys)) {
			node = candidate
		}
	}
	originalValues := u.Query()
	view := map[string]any{}
	var query *qt.LinkQuery
	if node != nil {
		for key, field := range site.viewFields(node.Path) {
			if !values.Has(field.Param) {
				continue
			}
			// Preserve the compact legacy text argument for the existing simple shape.
			if key == "search_state" {
				if _, e := site.parseTextBuilder(node, values.Get(field.Param)); e == nil {
					continue
				}
			}
			raw := values.Get(field.Param)
			var value any = raw
			switch field.Kind {
			case "boolean":
				if raw != "0" && raw != "1" {
					return siteState{}, linkError("url", field.Param+" needs 0 or 1")
				}
				value = raw == "1"
			case "json", "strings":
				if json.Unmarshal([]byte(raw), &value) != nil {
					return siteState{}, linkError("url", field.Param+" has invalid JSON")
				}
			}
			if _, err := viewValue(field, value); err != nil {
				return siteState{}, linkError("url", field.Param+": "+err.Error())
			}
			view[key] = value
			values.Del(field.Param)
		}
		if option := site.optionOfKind(node, "where"); option != nil && strings.HasPrefix(option.Param, "q_") && values.Has(option.Param) {
			q, err := qt.DecodeQueryLink(values.Get(option.Param))
			if err != nil {
				return siteState{}, linkError("url", err.Error())
			}
			if err := site.checkHostState(option.Param, q.HostState, node, originalValues); err != nil {
				return siteState{}, err
			}
			canonical, _ := site.queryToken(node.BaseQuery, q.Where)
			if !sameQueryToken(withoutHostState(q), canonical) || len(view) > 0 && q.HostState != nil {
				query = &q
				source := q
				for source.Grouping != nil {
					var group struct {
						Source qt.LinkQuery `json:"source"`
					}
					if err := json.Unmarshal(source.Grouping, &group); err != nil {
						return siteState{}, err
					}
					source = group.Source
				}
				token, err := site.queryToken(node.BaseQuery, source.Where)
				if err != nil {
					return siteState{}, err
				}
				values.Set(option.Param, token)
			}
		}
	}
	u.RawQuery = values.Encode()
	state, err := site.parseBaseURL(u.String())
	if err != nil {
		return state, err
	}
	state.View, state.Query, state.Highlight, state.Reveal = view, query, highlights, reveal
	if _, err := site.build(state); err != nil {
		return state, linkError("url", err.Error())
	}
	return state, nil
}
func (site *siteMap) arguments(state siteState) map[string]any {
	out := site.baseArguments(state)
	if len(state.View) > 0 {
		out["view"] = state.View
		if state.View["window_mode"] != nil {
			delete(out, "days")
		}
	}
	if state.TodayTimezone != "" {
		view := map[string]any{}
		for key, value := range state.View {
			view[key] = value
		}
		field := "window_timezone"
		if node := site.Nodes[state.Page]; node != nil && node.Dashboard != "" {
			field = "dashboard_timezone"
		}
		view[field] = state.TodayTimezone
		out["view"] = view
	}
	if state.Query != nil {
		out["query"] = state.Query
		delete(out, "filters")
		delete(out, "days")
	}
	if len(state.Highlight) > 0 {
		out["highlight"] = state.Highlight
	}
	if state.Reveal != "" {
		out["reveal"] = state.Reveal
	}
	return out
}
func (site *siteMap) browseViewStateList(node *siteNode, global bool) map[string]any {
	fields := site.viewFields(node.Path)
	keys := make([]string, 0, len(fields))
	for key, field := range fields {
		if slices.Contains(field.Pages, "*") == global {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	items := []map[string]any{}
	for _, key := range keys {
		items = append(items, map[string]any{"path": node.Path + "/view/" + key, "name": key, "field": map[string]any{"param": fields[key].Param, "kind": fields[key].Kind, "description": fields[key].Description, "values": fields[key].Values}})
	}
	path := node.Path + "/view"
	if global {
		path += "/global"
	} else {
		items = append(items, map[string]any{"path": path + "/global", "title": "Shared navigation and chrome", "description": "Global view controls; applicable on every page."})
	}
	return map[string]any{"path": path, "argument": "view", "items": items}

}
func (site *siteMap) browseViewState(node *siteNode) map[string]any {
	return site.browseViewStateList(node, false)
}
func (site *siteMap) browseGlobalViewState(node *siteNode) map[string]any {
	return site.browseViewStateList(node, true)
}
func (site *siteMap) browseTargets(node *siteNode) map[string]any {
	items := []map[string]any{}
	for _, target := range site.ViewContract.Targets {
		if viewApplies(target.Pages, node.Path) {
			items = append(items, map[string]any{"pattern": target.Pattern, "pages": target.Pages, "description": target.Description, "reveal_only": target.RevealOnly, "id_sources": site.attentionIDSources(target.Pattern)})
		}
	}
	if option := site.optionOfKind(node, "where"); option != nil && strings.HasPrefix(option.Param, "q_") {
		items = append(items, map[string]any{"pattern": "control:" + option.Param, "description": "The native query builder for this dataset."})
	}

	return map[string]any{"path": node.Path + "/targets", "items": items, "sections": site.ViewContract.Sections[node.Path], "controls_path": node.Path + "/view", "cards_path": "cards", "availability": "A target must be present in the selected view. Dynamic IDs are syntax-checked, not queried; matching filters remain your responsibility.", "highlight": "Multiple target references", "reveal": "One target: scroll + distinctive highlight; never browser focus. text: uses literal matching."}
}

// Resolve only library-backed references, with the same dataset/scope as the
// browser endpoint. No browser cache is consulted by an agent.
func (site *siteMap) resolveViewLink(ctx context.Context, catalog *Catalog, args map[string]any) (map[string]any, error) {
	raw, ok := args["url"].(string)
	if !ok {
		return args, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	values := u.Query()
	changed := false
	for param, list := range values {
		if !strings.HasPrefix(param, "q_") || len(list) != 1 || !strings.HasPrefix(list[0], "@") {
			continue
		}
		dataset := strings.TrimPrefix(param, "q_")
		if site.Datasets[dataset] == nil {
			return nil, linkError("url", "unknown snapshot dataset "+dataset)
		}
		if catalog == nil {
			return nil, linkError("url", "a library is required to resolve this snapshot")
		}
		snapshot, err := (pharosQuerySnapshotStore{catalog}).Resolve(ctx, queryTableScope, "pharos_"+dataset, strings.TrimPrefix(list[0], "@"))
		if err != nil {
			return nil, linkError("url", "snapshot cannot be opened: "+err.Error())
		}
		values.Set(param, snapshot.Token)
		changed = true
	}
	if !changed {
		return args, nil
	}
	u.RawQuery = values.Encode()
	out := map[string]any{}
	for key, value := range args {
		out[key] = value
	}
	out["url"] = u.String()
	return out, nil
}

func plainDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func viewTimezone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("needs an IANA timezone")
	}
	return time.LoadLocation(name)
}
func jsTextLength(value string) int { return len(utf16.Encode([]rune(value))) }
func searchHasWord(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) })
}

func validSearchDuration(duration string) bool {
	clock := time.Unix(0, 0).UTC()
	zero, _ := qt.ResolveRelativeDatetime("+0s", clock)
	resolved, err := qt.ResolveRelativeDatetime("-"+strings.TrimSpace(duration), clock)
	return err == nil && resolved != zero
}

func validateViewShape(name string, value any) error {
	object, _ := value.(map[string]any)
	switch name {
	case "disclosures", "reader_content", "query_layout":
		if object == nil {
			return fmt.Errorf("needs booleans by stable identity")
		}
		for _, value := range object {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("needs booleans by stable identity")
			}
		}
	case "chart_pins":
		if object == nil {
			return fmt.Errorf("needs bucket keys by chart identity")
		}
		for _, value := range object {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("needs bucket keys by chart identity")
			}
		}
	case "query_opened", "query_dashboard_filters":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("needs a native query token")
		}
		if _, err := qt.DecodeQueryLink(text); err != nil {
			return err
		}
	case "window_timezone", "dashboard_timezone":
		if name, ok := value.(string); !ok {
			return fmt.Errorf("needs an IANA timezone")
		} else if _, err := viewTimezone(name); err != nil {
			return fmt.Errorf("needs an IANA timezone")
		}
	case "delegation_page", "reader_page", "sidebar_width":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("needs a decimal integer")
		}
		number, err := strconv.Atoi(text)
		if err != nil || !plainDigits(text) || number < 0 || (name == "delegation_page" || name == "reader_page") && number > 250 || name == "sidebar_width" && (number < 300 || number > 5000) {
			return fmt.Errorf("out of range")
		}
	case "search_state", "reader_search":
		if name == "reader_search" && len(object) == 0 {
			return nil
		}
		return validateSearchDraft(object)
	case "record_keyword":
		if _, ok := object["field"].(string); !ok || object["field"] == "" {
			return fmt.Errorf("needs a text field ID")
		}
		draft, _ := object["draft"].(map[string]any)
		if err := validateSearchDraft(draft); err != nil {
			return err
		}
		if draft["fuzzy"] == true {
			return fmt.Errorf("record keywords do not support fuzzy matching")
		}
		scope := draft["scope"].(map[string]any)
		for _, key := range []string{"surface", "repository", "provider", "source", "from", "to", "conversation_id", "workspace_id"} {
			wanted := ""
			if key == "surface" {
				wanted = "prose"
			}
			if scope[key] != wanted {
				return fmt.Errorf("record keywords do not support message scope filters")
			}
		}
		for _, key := range []string{"roles", "kinds"} {
			raw, _ := json.Marshal(scope[key])
			if string(raw) != "[]" {
				return fmt.Errorf("record keywords do not support message scope filters")
			}
		}

	}
	return nil
}

// Validate the editor representation rather than restricting advanced searches
// to the old flat-rule template. Execution still validates engine semantics.
func validateSearchDraft(draft map[string]any) error {
	if draft == nil {
		return fmt.Errorf("needs a TextSearchDraft")
	}
	encoded, _ := json.Marshal(draft)
	if jsTextLength(string(encoded)) > 24000 {
		return fmt.Errorf("saved search exceeds 24,000 characters")
	}
	editor, _ := draft["editor"].(string)
	match, _ := draft["match"].(string)
	mode, _ := draft["mode"].(string)
	if !slices.Contains([]string{"visual", "syntax"}, editor) || !slices.Contains([]string{"rules", "literal", "regex"}, match) || !slices.Contains([]string{"advanced", "terms", "phrase", "literal", "regex"}, mode) {
		return fmt.Errorf("unknown search editor or matching mode")
	}
	text, ok := draft["text"].(string)
	if !ok {
		return fmt.Errorf("needs search text")
	}
	scope, _ := draft["scope"].(map[string]any)
	if scope == nil {
		return fmt.Errorf("needs search scope")
	}
	for _, key := range []string{"surface", "repository", "provider", "source", "from", "to", "conversation_id", "workspace_id"} {
		if _, ok := scope[key].(string); !ok {
			return fmt.Errorf("scope.%s needs text", key)
		}
	}
	for _, key := range []string{"roles", "kinds"} {
		if _, err := viewValue(siteViewField{Kind: "strings"}, scope[key]); err != nil {
			return fmt.Errorf("scope.%s needs strings", key)
		}
	}
	for _, key := range []string{"fuzzy", "caseSensitive", "separators"} {
		if value, given := draft[key]; given {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s needs a boolean", key)
			}
		}
	}
	if value, given := draft["dateWindow"]; given {
		window, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("dateWindow needs duration and from text")
		}
		duration, durationOK := window["duration"].(string)
		from, fromOK := window["from"].(string)
		if !durationOK || !fromOK {
			return fmt.Errorf("dateWindow needs duration and from text")
		}
		to, toOK := window["to"].(string)
		active := from == scope["from"] && (duration == "today" && toOK && to == scope["to"] || duration != "today" && scope["to"] == "")
		if active {
			if duration == "today" {
				start, fromOK := parseTime(from)
				end, toOK := parseTime(to)
				if !fromOK || !toOK || end.Before(start) {
					return fmt.Errorf("Today needs valid ordered date bounds")
				}
			} else if !validSearchDuration(duration) || from == "" {
				return fmt.Errorf("dateWindow needs a duration and its resolved from bound")
			}
		}
	}
	from, to := scope["from"].(string), scope["to"].(string)
	dateOnly := func(value string) bool {
		return len(value) == 10 && value[4] == '-' && value[7] == '-' && plainDigits(value[:4]+value[5:7]+value[8:])
	}
	if dateOnly(from) && dateOnly(to) && from > to {
		return fmt.Errorf("end date precedes start date")
	}
	rules := editor == "visual" && match == "rules"
	count := 0
	var walk func(map[string]any, int) (map[string]any, error)
	combine := func(op string, children []any) map[string]any {
		if len(children) == 1 {
			return children[0].(map[string]any)
		}
		return map[string]any{"op": op, "children": children}
	}
	walk = func(condition map[string]any, depth int) (map[string]any, error) {
		count++
		if condition == nil || count > 32 || depth > 4 {
			return nil, fmt.Errorf("invalid search conditions")
		}
		if _, ok := condition["exclude"].(bool); !ok {
			return nil, fmt.Errorf("condition needs exclude boolean")
		}
		if condition["kind"] == "group" {
			if condition["join"] != "all" && condition["join"] != "any" {
				return nil, fmt.Errorf("group needs all/any")
			}
			children, ok := condition["children"].([]any)
			if !ok {
				return nil, fmt.Errorf("group needs children")
			}
			included, excluded := []any{}, []any{}
			for _, child := range children {
				node, _ := child.(map[string]any)
				compiled, err := walk(node, depth+1)
				if err != nil {
					return nil, err
				}
				if node["exclude"] == true {
					excluded = append(excluded, compiled)
				} else {
					included = append(included, compiled)
				}
			}
			if rules && len(included) == 0 {
				return nil, fmt.Errorf("each group needs an included rule")
			}
			op := "and"
			if condition["join"] == "any" {
				op = "or"
			}
			positive := combine(op, included)
			if len(excluded) > 0 {
				return map[string]any{"op": "not", "children": []any{positive, combine("or", excluded)}}, nil
			}
			return positive, nil
		}
		kind, _ := condition["kind"].(string)
		if !slices.Contains([]string{"words", "anyWords", "phrase", "prefix", "near", "pre"}, kind) {
			return nil, fmt.Errorf("unknown rule kind")
		}
		for _, key := range []string{"text", "other", "distance"} {
			if _, ok := condition[key].(string); !ok {
				return nil, fmt.Errorf("rule.%s needs text", key)
			}
		}
		text := strings.TrimSpace(condition["text"].(string))
		if rules && (!searchHasWord(text) || len(text) > 2000) {
			return nil, fmt.Errorf("each rule needs words and at most 2,000 bytes")
		}
		if kind == "near" || kind == "pre" {
			other := strings.TrimSpace(condition["other"].(string))
			distance, err := strconv.ParseFloat(strings.TrimSpace(condition["distance"].(string)), 64)
			if rules && (!searchHasWord(other) || err != nil || distance < 0 || distance > 100 || distance != float64(int(distance))) {
				return nil, fmt.Errorf("proximity needs two phrases and a distance from 0 to 100")
			}
			return map[string]any{"op": kind, "distance": distance, "children": []any{map[string]any{"op": "phrase", "text": text}, map[string]any{"op": "phrase", "text": other}}}, nil
		}
		if kind == "prefix" {
			if rules && strings.ContainsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '*' || r == '?' }) {
				return nil, fmt.Errorf("prefix needs one word without wildcards")
			}
			return map[string]any{"op": "prefix", "text": text}, nil
		}
		if kind == "phrase" {
			return map[string]any{"op": "phrase", "text": text}, nil
		}
		words := []any{}
		for _, word := range strings.Fields(text) {
			if searchHasWord(word) {
				words = append(words, map[string]any{"op": "term", "text": word})
			}
		}
		op := "and"
		if kind == "anyWords" {
			op = "or"
		}
		return combine(op, words), nil
	}
	root, _ := draft["root"].(map[string]any)
	if root == nil || root["kind"] != "group" || root["exclude"] != false {
		return fmt.Errorf("needs a non-excluded root group")
	}
	compiled, err := walk(root, 0)
	if err != nil {
		return err
	}
	if rules {
		encoded, _ := json.Marshal(compiled)
		if jsTextLength(string(encoded)) > 16000 {
			return fmt.Errorf("compiled search exceeds 16,000 characters")
		}
	} else if strings.TrimSpace(text) == "" {
		return fmt.Errorf("needs search text")
	}
	return nil
}

func decodeViewValue(field siteViewField, raw string) (any, error) {
	var value any = raw
	switch field.Kind {
	case "boolean":
		if raw != "0" && raw != "1" {
			return nil, fmt.Errorf("%s needs 0 or 1", field.Param)
		}
		value = raw == "1"
	case "json", "strings":
		if json.Unmarshal([]byte(raw), &value) != nil {
			return nil, fmt.Errorf("%s has invalid JSON", field.Param)
		}
	}
	if _, err := viewValue(field, value); err != nil {
		return nil, err
	}
	return value, nil
}

// A successfully resolved immutable reference is already portable. Keep its
// exact identity rather than creating another snapshot while canonicalizing.
func reuseViewReferences(original, resolved string, result map[string]any) {
	given, e1 := url.Parse(original)
	expanded, e2 := url.Parse(resolved)
	built, e3 := url.Parse(siteString(result["url"]))
	if e1 != nil || e2 != nil || e3 != nil {
		return
	}
	values := built.Query()
	changed := false
	for name, list := range given.Query() {
		if strings.HasPrefix(name, "q_") && len(list) == 1 && strings.HasPrefix(list[0], "@") && sameQueryToken(expanded.Query().Get(name), values.Get(name)) {
			values.Set(name, list[0])
			changed = true
		}
	}
	if changed {
		built.RawQuery = values.Encode()
		result["url"] = built.String()
	}
	result["canonical"] = given.Path == built.Path && sameSiteParams(given.Query(), values)
}

func (site *siteMap) validateViewWindow(state siteState, values url.Values) error {
	hasWindow := false
	for name := range values {
		if strings.HasPrefix(name, "window_") {
			hasWindow = true
		}
	}
	if !hasWindow {
		return nil
	}
	node := site.Nodes[state.Page]
	dataset := ""
	if node != nil {
		dataset = node.Dataset
	}
	// A card dashboard can retain a query over a different source dataset
	// (for example tool-call cards displayed on the Tools overview).
	if card := site.cardByID[state.Card]; card != nil {
		dataset = card.Dataset
	}
	if dataset == "" || values.Get("window_dataset") != dataset {
		return linkError("view.window_dataset", "must name the view's source dataset")
	}
	days, err := strconv.Atoi(values.Get("window_days"))
	if err != nil || !slices.Contains(site.Windows.Days, days) {
		return linkError("view.window_days", "needs a supported lookback")
	}
	mode := values.Get("window_mode")
	if mode == "today" && (days != 1 || dataset == "tl1_attempts" || dataset == "findings") {
		return linkError("view.window_mode", "Today is not supported for this window")
	}
	field, until := values.Get("window_field"), values.Get("window_until_field")
	if until == "" {
		until = field
	}
	for _, name := range []string{field, until} {
		valid := false
		for _, column := range site.Datasets[dataset].Columns {
			if column["name"] == name && column["type"] == "datetime" {
				valid = true
			}
		}
		if !valid {
			return linkError("view.window_field", "needs a source timestamp field")
		}
	}
	option := site.optionOfKind(node, "where")
	param := "q_" + dataset
	if state.Card == "" || !values.Has(param) {
		if option == nil {
			return linkError("view.window_mode", "needs a native query page")
		}
		param = option.Param
	}
	query, err := qt.DecodeQueryLink(values.Get(param))
	if err != nil {
		return linkError("view.window_mode", "needs its native query definition")
	}
	for query.Grouping != nil {
		var group struct {
			Source qt.LinkQuery `json:"source"`
		}
		if json.Unmarshal(query.Grouping, &group) != nil {
			return linkError("query", "invalid grouping source")
		}
		query = group.Source
	}
	lower, upper := false, false
	for _, term := range query.Where {
		if term.Negated {
			continue
		}
		if term.Field == field && term.Op == ">=" {
			if mode == "relative" {
				lower = term.Value == fmt.Sprintf("-%dd", days)
			} else {
				_, err := time.Parse(time.RFC3339Nano, term.Value)
				lower = err == nil
			}
		}
		if term.Field == until && term.Op == "<" {
			upper = mode == "calendar" || mode == "" || term.Value == "+0s"
		}
	}
	if !lower || !upper {
		return linkError("view.window_mode", "time metadata disagrees with the native query bounds")
	}
	return nil
}
