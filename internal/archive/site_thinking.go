package archive

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// The Thinking dashboards (Waiting and Compaction, Thinking) keep every
// control in one JSON parameter, thinking, the view field of that name. The
// site map lists its main keys as page options, and its Range as days, so
// site_link takes the same options and days there as on the other
// dashboards. They are written into view.thinking, and read back out of it.

const thinkingParam = "thinking"

func thinkingPage(page string) bool {
	return page == "analyze/thinking-overview" || page == "analyze/thinking-detail"
}

// thinkingRanges are the Range presets by days, as thinkingRangeDays in
// thinking-model.ts names them.
var thinkingRanges = map[int]string{1: "1d", 7: "7d", 30: "30d", 90: "90d", 180: "6m", 365: "1y"}

func (site *siteMap) thinkingOptions(node *siteNode) []*siteOption {
	out := []*siteOption{}
	for _, option := range node.Options {
		if option.Param == thinkingParam && option.Key != "" && option.Kind != "days" {
			out = append(out, option)
		}
	}
	return out
}

// thinkingHolds reports whether option's requirements hold, reading each
// other option through value, or its default when it has none.
func (site *siteMap) thinkingHolds(node *siteNode, option *siteOption, value func(name string) any) bool {
	for _, need := range option.Requires {
		other := value(need.Option)
		if other == nil {
			other = site.option(node, need.Option).Default
		}
		if !listHolds(any(need.Values), other) {
			return false
		}
	}
	return true
}

// thinkingArgs moves options and days on a Thinking dashboard into
// view.thinking. A key given both ways must agree.
func (site *siteMap) thinkingArgs(args map[string]any) (map[string]any, error) {
	page, _ := args["page"].(string)
	page = strings.Trim(page, "/")
	_, optionsGiven := args["options"]
	_, daysGiven := args["days"]
	if !thinkingPage(page) || args["card"] != nil || args["url"] != nil || !optionsGiven && !daysGiven {
		return args, nil
	}
	node := site.Nodes[page]
	options := map[string]any{}
	if raw := args["options"]; raw != nil {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, linkError("options", "must be an object of option names and values.")
		}
		options = object
	}
	view := map[string]any{}
	if raw, ok := args["view"]; ok {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, linkError("view", "needs an object")
		}
		maps.Copy(view, object)
	}
	state := map[string]any{}
	if raw, ok := view[thinkingParam]; ok {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, linkError("view.thinking", "needs Thinking dashboard state")
		}
		maps.Copy(state, object)
	}
	set := func(arg, key string, value any) error {
		if previous, ok := state[key]; ok && !reflect.DeepEqual(previous, value) {
			return linkError(arg, fmt.Sprintf("disagrees with view.thinking.%s.", key))
		}
		state[key] = value
		return nil
	}
	names := []string{}
	for _, option := range site.thinkingOptions(node) {
		names = append(names, option.Name)
	}
	for _, name := range slices.Sorted(maps.Keys(options)) {
		option := site.option(node, name)
		arg := "options." + name
		if option == nil || !slices.Contains(names, name) {
			hint := "is not an option of " + page + "."
			if name == "days" || name == "filters" {
				hint = "is given as the " + name + " argument, not an option."
			}
			return nil, linkError(arg, hint, names...)
		}
		text, ok := options[name].(string)
		if !ok {
			return nil, linkError(arg, "must be text.")
		}
		if option.Kind == "enum" && !slices.Contains(optionValues(option), text) {
			return nil, linkError(arg, fmt.Sprintf("%q is not a choice.", text), optionValues(option)...)
		}
		if !site.thinkingHolds(node, option, func(name string) any {
			if value, given := options[name]; given {
				return value
			}
			return state[name]
		}) {
			need := option.Requires[0]
			return nil, linkError(arg, fmt.Sprintf("needs options.%s to be %s; view.thinking takes it otherwise.", need.Option, strings.Join(siteStrings(need.Values), " or ")))
		}
		if err := set(arg, option.Key, text); err != nil {
			return nil, err
		}
	}
	if daysGiven {
		var choices []string
		for _, value := range site.option(node, "days").Values {
			choices = append(choices, siteString(value.Value))
		}
		var preset string
		switch raw := args["days"]; {
		case raw == "today", raw == "all":
			preset = raw.(string)
		default:
			days, _, err := argDays(raw)
			if err != nil {
				return nil, err
			}
			if preset = thinkingRanges[days]; days == 0 {
				preset = "all"
			} else if preset == "" {
				return nil, linkError("days", fmt.Sprintf("%d is not a Range of %s; view.thinking takes a custom from/to range.", days, page), choices...)
			}
		}
		if err := set("days", "range", preset); err != nil {
			return nil, err
		}
	}
	view[thinkingParam] = state
	out := map[string]any{}
	for key, value := range args {
		if key != "options" && key != "days" {
			out[key] = value
		}
	}
	out["view"] = view
	return out, nil
}

// thinkingArguments reads a Thinking dashboard's options and days back out of
// view.thinking, the inverse of thinkingArgs.
func (site *siteMap) thinkingArguments(out map[string]any) {
	page, _ := out["page"].(string)
	view, _ := out["view"].(map[string]any)
	state, _ := view[thinkingParam].(map[string]any)
	if !thinkingPage(page) || out["card"] != nil || state == nil {
		return
	}
	node, full := site.Nodes[page], state
	state, view = maps.Clone(state), maps.Clone(view)
	options := map[string]any{}
	for _, option := range site.thinkingOptions(node) {
		// The page writes every control, including ones its view does not
		// use (calendar rows outside the calendar). Those stay in
		// view.thinking, which takes them as they are.
		if value, ok := state[option.Key].(string); ok && site.thinkingHolds(node, option, func(name string) any { return full[name] }) {
			options[option.Name] = value
			delete(state, option.Key)
		}
	}
	if len(options) > 0 {
		out["options"] = options
	}
	switch value, _ := state["range"].(string); value {
	case "today", "all":
		out["days"] = value
		delete(state, "range")
	default:
		for days, preset := range thinkingRanges {
			if preset == value {
				out["days"] = days
				delete(state, "range")
			}
		}
	}
	if len(state) > 0 {
		view[thinkingParam] = state
	} else {
		delete(view, thinkingParam)
	}
	if len(view) > 0 {
		out["view"] = view
	} else {
		delete(out, "view")
	}
}

// thinkingSummary says what a Thinking dashboard link shows: its view and
// breakdown, model and effort, filters and Range.
func (site *siteMap) thinkingSummary(node *siteNode, state siteState) string {
	thinking, _ := state.View[thinkingParam].(map[string]any)
	label := func(name string, value any) string {
		if option := site.option(node, name); option != nil {
			for _, item := range option.Values {
				if siteString(item.Value) == siteString(value) && item.Label != "" {
					return item.Label
				}
			}
		}
		return siteString(value)
	}
	parts := []string{node.Title}
	if node.Path == "analyze/thinking-detail" {
		view := thinking["view"]
		if view == nil {
			view = site.option(node, "view").Default
		}
		phrase := label("view", view)
		if by, _ := thinking["groupBy"].(string); by != "" && by != "none" {
			phrase += " by " + label("groupBy", by)
		}
		parts = []string{phrase}
	}
	for _, key := range []string{"model", "effort"} {
		if value, _ := thinking[key].(string); value != "" && value != "all" {
			parts = append(parts, key+" "+value)
		}
	}
	if len(state.Filters) > 0 {
		parts = append(parts, "where "+site.filterPhrase(site.Datasets[node.Dataset], state.Filters))
	}
	switch value, _ := thinking["range"].(string); value {
	case "", "all":
		parts = append(parts, "all time")
	case "today":
		parts = append(parts, "today since local midnight")
	case "custom":
		parts = append(parts, fmt.Sprintf("from %s to %s", siteString(thinking["from"]), siteString(thinking["to"])))
	default:
		for days, preset := range thinkingRanges {
			if preset == value {
				parts = append(parts, "last "+label("days", strconv.Itoa(days)))
			}
		}
	}
	return strings.Join(parts, ", ")
}
