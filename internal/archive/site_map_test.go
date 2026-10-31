package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// siteCases is testdata/site-map-cases.json, which the site map's generator
// writes from the page's own functions (docs/site-map.md, "Cases").
type siteCases struct {
	Example struct {
		Page    string                  `json:"page"`
		Topic   string                  `json:"topic"`
		Chart   map[string]any          `json:"chart"`
		Filters []qt.WhereTerm          `json:"filters"`
		Days    *int                    `json:"days"`
		Verdict string                  `json:"verdict"`
		URL     string                  `json:"url"`
		Decoded map[string]qt.LinkQuery `json:"decoded"`
	} `json:"example"`
	Analyze map[string][]struct {
		Chart   map[string]any `json:"chart"`
		Verdict string         `json:"verdict"`
		Written string         `json:"written"`
		Filters []qt.WhereTerm `json:"filters"`
		Days    *int           `json:"days"`
		URL     string         `json:"url"`
	} `json:"analyze"`
	Cards []struct {
		ID   string `json:"id"`
		Days *int   `json:"days"`
		URL  string `json:"url"`
	} `json:"cards"`
	Nodes []struct {
		Path string `json:"path"`
		URL  string `json:"url"`
	} `json:"nodes"`
}

func readSiteCases(t *testing.T) siteCases {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "site-map-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases siteCases
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func testSiteMap(t *testing.T) *siteMap {
	t.Helper()
	site, err := loadedSiteMap()
	if err != nil {
		t.Fatal(err)
	}
	return site
}

func TestSiteMapJudgesEveryChartCaseAsThePageDoes(t *testing.T) {
	site, cases := testSiteMap(t), readSiteCases(t)
	count := 0
	for topic, items := range cases.Analyze {
		dashboard := site.Analyze.Dashboards[topic]
		if dashboard == nil {
			t.Fatalf("no dashboard %s", topic)
		}
		for _, item := range items {
			count++
			if verdict, problem := site.judge(dashboard, item.Chart); verdict != item.Verdict {
				t.Errorf("%s %s: verdict %s, want %s (%+v)", topic, compactJSON(item.Chart), verdict, item.Verdict, problem)
			}
		}
	}
	if count < 1000 {
		t.Fatalf("only %d chart cases", count)
	}
}

// sameLink compares a built link with the page's: the path, and each
// parameter, query tokens by their decoded state.
func sameLink(t *testing.T, name, got, want string) {
	t.Helper()
	left, err := url.Parse(got)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	right, _ := url.Parse(want)
	if left.Path != right.Path || !sameSiteParams(left.Query(), right.Query()) {
		t.Errorf("%s:\n got %s\nwant %s", name, got, want)
	}
}

// roundTrip parses a link and rebuilds it: the rebuilt link is the page's
// link, and parsing it again rebuilds it exactly.
func roundTrip(t *testing.T, site *siteMap, name, link string) siteState {
	t.Helper()
	state, err := site.parseURL(link)
	if err != nil {
		t.Errorf("%s: %v", name, err)
		return state
	}
	built, err := site.build(state)
	if err != nil {
		t.Errorf("%s: %v", name, err)
		return state
	}
	sameLink(t, name, built, link)
	again, err := site.parseURL(built)
	if err != nil {
		t.Errorf("%s: rebuilt %s: %v", name, built, err)
		return state
	}
	if rebuilt, _ := site.build(again); rebuilt != built {
		t.Errorf("%s: not canonical:\n%s\n%s", name, built, rebuilt)
	}
	// The page's link is already canonical, compared decoded.
	if result, err := site.linkTool(map[string]any{"url": link}); err != nil || result["canonical"] != true {
		t.Errorf("%s: canonical %v, %v", name, result["canonical"], err)
	}
	// site_link's arguments for the link give the link again.
	args := linkArgs(t, compactJSON(site.arguments(state)))
	if result, err := site.linkTool(args); err != nil || result["url"] != built {
		t.Errorf("%s: arguments %s give %v, %v", name, compactJSON(args), result["url"], err)
	}
	return state
}

func TestSiteMapBuildsAndReadsEveryDashboardCase(t *testing.T) {
	site, cases := testSiteMap(t), readSiteCases(t)
	count := 0
	for topic, items := range cases.Analyze {
		page := "analyze/" + topic
		for _, item := range items {
			if item.URL == "" {
				continue
			}
			count++
			days := 0
			if item.Days != nil {
				days = *item.Days
			}
			name := topic + " " + compactJSON(item.Chart)
			state := siteState{Page: page, Chart: site.Analyze.Dashboards[topic].viewChart(item.Chart), Filters: item.Filters, Days: days}
			built, err := site.build(state)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			sameLink(t, name, built, item.URL)

			read := roundTrip(t, site, name, item.URL)
			if read.Days != days || compactJSON(read.Filters) != compactJSON(item.Filters) {
				t.Errorf("%s: read days %d filters %s", name, read.Days, compactJSON(read.Filters))
			}
		}
	}
	if count == 0 {
		t.Fatal("no dashboard links")
	}
}

func TestSiteMapBuildsAndReadsEveryCardCase(t *testing.T) {
	site, cases := testSiteMap(t), readSiteCases(t)
	for _, item := range cases.Cards {
		days := 0
		if item.Days != nil {
			days = *item.Days
		}
		name := fmt.Sprintf("%s at %d", item.ID, days)
		built, err := site.build(siteState{Page: site.cardByID[item.ID].Page, Card: item.ID, Days: days})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		sameLink(t, name, built, item.URL)
		withGlobals, _ := url.Parse(item.URL)
		params := withGlobals.Query()
		params.Set("nav_collapsed", "0")
		params.Set("nav_expanded", "{}")
		withGlobals.RawQuery = params.Encode()
		if read, err := site.parseURL(withGlobals.String()); err != nil || read.Card != item.ID {
			t.Errorf("%s with global view fields: %+v %v", name, read, err)
		}
		if read := roundTrip(t, site, name, item.URL); read.Card != item.ID || read.Days != days {
			t.Errorf("%s: read %+v", name, read)
		}
	}
}

func TestSiteMapReadsEveryPageCase(t *testing.T) {
	site, cases := testSiteMap(t), readSiteCases(t)
	for _, item := range cases.Nodes {
		if read := roundTrip(t, site, item.Path+" "+item.URL, item.URL); read.Page != item.Path {
			t.Errorf("%s: read as %s", item.URL, read.Page)
		}
	}
}

// linkArgs is site_link's arguments as an MCP client sends them: JSON.
func linkArgs(t *testing.T, text string) map[string]any {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal([]byte(text), &args); err != nil {
		t.Fatal(err)
	}
	return args
}

func TestSiteLinkBuildsTheMotivatingExample(t *testing.T) {
	site, cases := testSiteMap(t), readSiteCases(t)
	example := cases.Example
	// The example's whole chart gives the example's link.
	full := map[string]any{"page": example.Page, "options": example.Chart, "filters": example.Filters, "days": *example.Days}
	full = linkArgs(t, compactJSON(full))
	result, err := site.linkTool(full)
	if err != nil {
		t.Fatal(err)
	}
	sameLink(t, "example", result["url"].(string), example.URL)
	// An agent names only what it wants; the page draws the same chart.
	result, err = site.linkTool(linkArgs(t, `{"page": "analyze/tokens", "options": {"view": "trend", "metric": "tokens", "split": "model_family", "rollingPeriods": 7},
		"filters": [{"field": "provider", "op": "=", "value": "claude"}, {"field": "model_family", "op": "contains", "value": "opus"}], "days": 30}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "Tokens per day by model, 7-day rolling mean, where provider is claude and model contains opus, last 30 days."; result["summary"] != want {
		t.Errorf("summary %q, want %q", result["summary"], want)
	}
	if result["title"] != "Tokens and Cost · Over time" {
		t.Errorf("title %q", result["title"])
	}
	state, err := site.parseURL(result["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	dashboard, view := site.Analyze.Dashboards["tokens"], site.Analyze.Dashboards["tokens"].Views["trend"]
	merged, _ := dashboard.readChart(state.Chart.toMap())
	drawn := site.drawChart(view, merged)
	for _, key := range view.Keys {
		value, ok := drawn[key]
		if !ok {
			value, ok = merged[key]
		}
		if !ok {
			value = view.Defaults[key]
		}
		if !sameValue(value, example.Chart[key]) {
			t.Errorf("draws %s %v, want %v", key, value, example.Chart[key])
		}
	}
	if verdict, _ := site.judge(dashboard, state.Chart.toMap()); verdict != verdictValid || state.Days != 30 || compactJSON(state.Filters) != compactJSON(example.Filters) {
		t.Errorf("read %s, days %d, filters %s", verdict, state.Days, compactJSON(state.Filters))
	}
	// Its arguments give the same link again.
	again, err := site.linkTool(linkArgs(t, compactJSON(result["arguments"])))
	if err != nil || again["url"] != result["url"] {
		t.Errorf("arguments %s give %v, %v", compactJSON(result["arguments"]), again["url"], err)
	}
}

func TestSiteLinkErrorsNameTheArgumentAndListTheChoices(t *testing.T) {
	site := testSiteMap(t)
	for _, test := range []struct{ args, arg, wants string }{
		{`{"page": "analyze/tokens", "options": {"metric": "cost", "split": "type"}}`, "options.split", "model_family"},
		{`{"page": "analyze/tokens", "options": {"metric": "bogus"}}`, "options.metric", "tokens"},
		{`{"page": "analyze/tokens", "options": {"view": "compare", "unit": "agent_session", "split": "model_family"}}`, "options.split", "session_kind"},
		{`{"page": "analyze/tokens", "options": {"view": "trend", "unit": "conversation"}}`, "options.unit", "rollingPeriods"},
		{`{"page": "analyze/tokens", "options": {"view": "pie"}}`, "options.view", "compare"},
		{`{"page": "analyze/tokens", "options": {"hours": 0}}`, "options.hours", ""},
		{`{"page": "analyze/tokens", "filters": [{"field": "provider", "op": "=", "value": "anthropic"}]}`, "filters[0].value", "claude"},
		{`{"page": "analyze/tokens", "filters": [{"field": "provider", "op": "contains", "value": "c"}]}`, "filters[0].op", "is_not_null"},
		{`{"page": "analyze/tokens", "filters": [{"field": "modelfamily", "op": "=", "value": "x"}]}`, "filters[0].field", ""},
		{`{"page": "analyze/tokens", "filters": [{"field": "total_tokens", "op": ">", "value": "many"}]}`, "filters[0].value", ""},
		{`{"page": "analyze/tokens", "filters": [{"field": "first_usage_at", "op": ">=", "value": "-30 days"}]}`, "filters[0].value", ""},
		{`{"page": "analyze/tokens", "days": 14}`, "days", "30"},
		{`{"page": "analyze/tokenz"}`, "page", "analyze/tokens"},
		{`{"page": "findings", "options": {"tab": "evidence"}}`, "options.tab", ""},
		{`{"page": "findings", "options": {"list": "all"}}`, "options.list", "watching"},
		{`{"page": "work", "options": {"work_id": "w", "message": "m"}}`, "options.message", ""},
		{`{"page": "work", "options": {"work_id": "w", "sort": "cost"}}`, "options.sort", ""},
		{`{"page": "library", "options": {"search": "api.go"}}`, "options.search", ""},
		{`{"page": "library", "options": {"text": [{"kind": "near", "text": "a"}]}}`, "options.text[0].other", ""},
		{`{"page": "settings/sync", "days": 7}`, "days", ""},
		{`{"card": "no-such-card"}`, "card", ""},
		{`{"card": "tool-failures", "days": 14}`, "days", "30"},
		{`{"page": "analyze/tokens", "colour": "red"}`, "colour", "options"},
	} {
		_, err := site.linkTool(linkArgs(t, test.args))
		var linkErr *siteLinkError
		if !errors.As(err, &linkErr) {
			t.Errorf("%s: %v", test.args, err)
			continue
		}
		if linkErr.Arg != test.arg || test.wants != "" && !slices.Contains(linkErr.Choices, test.wants) {
			t.Errorf("%s: %s, want %s with choice %q", test.args, err, test.arg, test.wants)
		}
	}
}

func TestSiteLinkRejectsWhatTheSiteMapCannotExpress(t *testing.T) {
	site, cases := testSiteMap(t), readSiteCases(t)
	example, _ := url.Parse(cases.Example.URL)
	with := func(change func(url.Values)) string {
		values := example.Query()
		change(values)
		return example.Path + "?" + values.Encode()
	}
	for name, link := range map[string]string{
		"unknown parameter":    with(func(v url.Values) { v.Set("colour", "red") }),
		"private parameter":    with(func(v url.Values) { v.Set("ask_chat", "chat-1") }),
		"token parameter":      "/?token=secret",
		"snapshot reference":   "/analyze?analyze_mode=query&analyze_topic=tokens&analyze_data=usage&q_usage=@snap-1",
		"legacy alias":         "/tools",
		"unknown page":         "/nowhere",
		"other origin":         "https://example.com" + cases.Example.URL,
		"coerced chart":        with(func(v url.Values) { v.Set("analyze_chart", `{"v":2,"view":"trend","metric":"cost","split":"type"}`) }),
		"older chart":          with(func(v url.Values) { v.Set("analyze_chart", `{"view":"trend","metric":"tokens"}`) }),
		"invalid chart":        with(func(v url.Values) { v.Set("analyze_chart", `{"v":2,"metric":"bogus"}`) }),
		"window disagreement":  with(func(v url.Values) { v.Set("analyze_days", "90") }),
		"unknown filter field": "/analyze?analyze_mode=query&analyze_topic=tokens&analyze_data=usage&q_usage=" + mustToken(t, site, `[{"field":"nope","op":"=","value":"x"}]`),
		"tab without finding":  "/findings?tab=evidence",
		"unknown list":         "/findings?list=everything",
		"repeated parameter":   "/findings?list=wins&list=open",
		"text search settings": "/library?text_builder=" + url.QueryEscape(`{"editor":"visual","match":"rules","root":{"id":"group-1","kind":"group","join":"all","children":[],"exclude":false},"text":"","mode":"advanced","scope":{"surface":"prose","roles":[],"kinds":[]},"fuzzy":true}`),
		"message and at":       "/work/w?conversation=c&message=m&at=2026-01-01T00:00:00Z",
	} {
		if _, err := site.linkTool(map[string]any{"url": link}); err == nil {
			t.Errorf("%s: %s was accepted", name, link)
		} else if !strings.HasPrefix(err.Error(), "url") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A link in older names opens as the current chart, and comes back in them.
	result, err := site.linkTool(map[string]any{"url": with(func(v url.Values) {
		v.Set("analyze_chart", `{"v":2,"view":"trend","metric":"percent","split":"model_family"}`)
	})})
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := site.parseURL(result["url"].(string)); compactJSON(state.Chart) != `{"v":2,"view":"trend","metric":"tokens","split":"model_family","share":true}` || result["canonical"] != false {
		t.Errorf("older names read as %s", compactJSON(state.Chart))
	}
}

func mustToken(t *testing.T, site *siteMap, where string) string {
	t.Helper()
	var terms []qt.WhereTerm
	if err := json.Unmarshal([]byte(where), &terms); err != nil {
		t.Fatal(err)
	}
	token, err := site.queryToken(nil, terms)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func mustQuery(t *testing.T, query string) string {
	t.Helper()
	var state qt.LinkQuery
	if err := json.Unmarshal([]byte(query), &state); err != nil {
		t.Fatal(err)
	}
	token, err := qt.EncodeQueryLink(state)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// A card's text at a one-day window reads "the last 1 day", not "1 days".
func TestSiteLinkWritesACardsOneDayWindowInTheSingular(t *testing.T) {
	site := testSiteMap(t)
	result, err := site.linkTool(map[string]any{"card": "tool-failures", "days": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if summary := result["summary"].(string); summary != "Recorded error calls started in the last 1 day, split by tool." {
		t.Errorf("summary %q", summary)
	}
	if week, _ := site.linkTool(map[string]any{"card": "tool-failures", "days": float64(7)}); week["summary"] != "Recorded error calls started in the last 7 days, split by tool." {
		t.Errorf("7-day summary %q", week["summary"])
	}
}

func TestSiteLinkChangesACardAsItsDashboardDoes(t *testing.T) {
	site := testSiteMap(t)
	card := site.cardByID["human-word-total"]
	if card == nil || card.Window == nil || !slices.Contains(card.Window.ChartDays, "rollingPeriods") {
		t.Fatal("human-word-total no longer follows its window with rollingPeriods")
	}
	own, err := site.linkTool(map[string]any{"card": "human-word-total", "days": float64(30)})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := site.linkTool(linkArgs(t, `{"card": "human-word-total", "days": 30, "options": {"scale": "logarithmic"}}`))
	if err != nil {
		t.Fatal(err)
	}
	state, err := site.parseURL(changed["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	// The chart keeps the window's rolling sum, and takes the change.
	chart := state.Chart.toMap()
	if state.Page != "analyze/human" || state.Days != 30 || chart["rollingPeriods"] != float64(30) || chart["scale"] != "logarithmic" {
		t.Fatalf("changed card reads as %+v %s", state, compactJSON(chart))
	}
	// An explicit setting wins over the window's.
	explicit, err := site.linkTool(linkArgs(t, `{"card": "human-word-total", "days": 30, "options": {"rollingPeriods": 7}}`))
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := site.parseURL(explicit["url"].(string)); state.Chart.toMap()["rollingPeriods"] != float64(7) {
		t.Errorf("explicit rollingPeriods: %s", compactJSON(state.Chart))
	}
	// As the page writes it: the card's link with only the chart changed,
	// its filters and window terms on sent_at kept.
	ownURL, _ := url.Parse(own["url"].(string))
	changedURL, _ := url.Parse(changed["url"].(string))
	if !sameSiteParamsExcept(ownURL.Query(), changedURL.Query(), []string{"analyze_chart"}) {
		t.Errorf("a chart change changed more than analyze_chart:\n%s\n%s", ownURL, changedURL)
	}
	filtered, err := site.linkTool(linkArgs(t, `{"card": "human-word-total", "days": 7, "filters": [{"field": "repository_name", "op": "contains", "value": "pharos"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	filteredURL, _ := url.Parse(filtered["url"].(string))
	where, err := qt.DecodeQueryLink(filteredURL.Query().Get("analyze_filters"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"field":"repository_name","op":"contains","value":"pharos"},{"field":"sent_at","op":">=","value":"-7d"},{"field":"sent_at","op":"<","value":"+0s"}]`; compactJSON(where.Where) != want {
		t.Errorf("filters with the card's window: %s", compactJSON(where.Where))
	}
	// A link the dashboard wrote after the same change reads as the same
	// changed card, already canonical.
	values := ownURL.Query()
	written := card.Chart.clone()
	written.set("rollingPeriods", 30)
	written.set("scale", "logarithmic")
	text, _ := written.MarshalJSON()
	values.Set("analyze_chart", string(text))
	page, err := site.linkTool(map[string]any{"url": ownURL.Path + "?" + values.Encode()})
	if err != nil {
		t.Fatal(err)
	}
	if read, _ := site.parseURL(page["url"].(string)); read.Page != "analyze/human" || read.Days != 30 || read.Chart.toMap()["scale"] != "logarithmic" {
		t.Errorf("page-written change reads as %+v", read)
	}
	if again, err := site.linkTool(linkArgs(t, compactJSON(page["arguments"]))); err != nil || again["url"] != page["url"] {
		t.Errorf("arguments %s give %v, %v", compactJSON(page["arguments"]), again["url"], err)
	}
	// Changes that leave the card as it is give the card's own link, which
	// reads back and rebuilds byte for byte.
	for _, args := range []string{
		`{"card": "human-word-total", "options": {"scale": "linear"}}`,
		`{"card": "human-word-total", "filters": [{"field": "typed_words", "op": ">", "value": "0"}]}`,
		`{"card": "human-word-total", "days": 30, "options": {"rollingPeriods": 30}}`,
	} {
		result, err := site.linkTool(linkArgs(t, args))
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		state, err := site.parseURL(result["url"].(string))
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		rebuilt, _ := site.build(state)
		if state.Changed || rebuilt != result["url"] || result["arguments"].(map[string]any)["options"] != nil {
			t.Errorf("%s: changed %v, rebuilt the same %v, arguments %s", args, state.Changed, rebuilt == result["url"], compactJSON(result["arguments"]))
		}
	}
	// A card that opens a query table changes only its window.
	if _, err := site.linkTool(linkArgs(t, `{"card": "findings-open", "options": {"view": "trend"}}`)); err == nil || !strings.Contains(err.Error(), "only change days") {
		t.Errorf("query-table card with options: %v", err)
	}
}

func TestSiteLinkTakesAnOutputBudget(t *testing.T) {
	site := testSiteMap(t)
	if _, err := site.linkTool(linkArgs(t, `{"page": "findings", "max_output_tokens": 300}`)); err != nil {
		t.Fatal(err)
	}
	_, err := site.linkTool(linkArgs(t, `{"page": "nowhere", "max_output_tokens": 200}`))
	var linkErr *siteLinkError
	if !errors.As(err, &linkErr) || len(err.Error()) > 600 || !strings.Contains(err.Error(), "more") {
		t.Errorf("bounded error: %d bytes: %v", len(fmt.Sprint(err)), err)
	}
}

// A combined legend selection reaches a supporting query and a bucket's
// records as one OR of AND branches. A link holding one reads, builds and
// reads again as the same term.
func TestSiteLinkReadsAnOrOfAndSelection(t *testing.T) {
	site := testSiteMap(t)
	where := `[{"field":"last_usage_at","op":">=","value":"-30d"},{"any":[{"all":[{"field":"provider","op":"=","value":"codex"},{"field":"model_family","op":"=","value":"gpt-5"}]},{"all":[{"field":"provider","op":"=","value":"claude"},{"field":"model_family","op":"is_null","value":""}]}]}]`
	state := roundTrip(t, site, "or of and", "/analyze?analyze_mode=query&analyze_topic=tokens&analyze_data=usage&q_usage="+mustToken(t, site, where))
	if len(state.Filters) != 2 || len(state.Filters[1].Branches()) != 2 || len(state.Filters[1].Branches()[1]) != 2 {
		t.Fatalf("read %s", compactJSON(state.Filters))
	}
}

// The app's own Search and Analyze links carry the table's host query state
// in the query token, mirrored in the link's own parameters.
func TestSiteLinkReadsTheAppsHostQueryState(t *testing.T) {
	site := testSiteMap(t)
	built, err := site.linkTool(linkArgs(t, `{"page": "library", "options": {"text": [{"kind": "phrase", "text": "lock this"}], "scope": "user", "view": "conversation"}}`))
	if err != nil {
		t.Fatal(err)
	}
	link, _ := url.Parse(built["url"].(string))
	// The app writes text_builder without the editor's IDs.
	builder := withoutEditorIDs(link.Query().Get("text_builder"))
	withHost := func(param string, host *qt.HostQueryState) string {
		t.Helper()
		query, err := qt.DecodeQueryLink(link.Query().Get(param))
		if param != "q_library" {
			query, err = qt.DecodeQueryLink(mustToken(t, site, `[{"field":"status","op":"=","value":"error"}]`))
		}
		if err != nil {
			t.Fatal(err)
		}
		query.HostState = host
		token, err := qt.EncodeQueryLink(query)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	none := &qt.HostQueryState{Version: 1, Params: map[string][]string{}}
	search := &qt.HostQueryState{Version: 1, Params: map[string][]string{"text_builder": {builder}, "view": {"conversation"}}}
	keyword := &qt.HostQueryState{Version: 1, Params: map[string][]string{"record_tool_calls": {`{"field":"command","draft":{}}`}}}
	library := func(host *qt.HostQueryState, extra url.Values) string {
		values := url.Values{"q_library": {withHost("q_library", host)}}
		for key, list := range extra {
			values[key] = list
		}
		return "/library?" + values.Encode()
	}
	analyze := func(host *qt.HostQueryState, extra string) string {
		return "/analyze?analyze_mode=query&analyze_topic=tools&analyze_data=tool_calls&q_tool_calls=" + withHost("q_tool_calls", host) + extra
	}
	for name, address := range map[string]string{
		"search with no host clause":   library(none, nil),
		"search with its clause":       library(search, url.Values{"text_builder": {builder}, "view": {"conversation"}}),
		"analyze with no host clause":  analyze(none, ""),
		"search and a site-link token": "/library?" + url.Values{"q_library": {link.Query().Get("q_library")}, "text_builder": {builder}}.Encode(),
	} {
		if _, err := site.linkTool(map[string]any{"url": address}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, address := range map[string]string{
		"clause without its parameter": library(search, url.Values{"view": {"conversation"}}),
		"parameter without its clause": library(none, url.Values{"view": {"conversation"}}),
		"clause that differs":          library(search, url.Values{"text_builder": {builder}, "view": {"table"}}),
		"keyword clause":               analyze(keyword, "&record_tool_calls="+url.QueryEscape(keyword.Params["record_tool_calls"][0])),
		"unreadable host state":        library(&qt.HostQueryState{Version: 0, Params: map[string][]string{}}, nil),
	} {
		_, err := site.linkTool(map[string]any{"url": address})
		if err == nil || !strings.HasPrefix(err.Error(), "url") {
			t.Errorf("%s: %s gave %v", name, address, err)
		} else if strings.Contains(err.Error(), "more than where terms") {
			t.Errorf("%s: reported as a table query: %v", name, err)
		}
	}
}

func TestSiteLinkTodayWindows(t *testing.T) {
	site, err := loadedSiteMap()
	if err != nil {
		t.Fatal(err)
	}
	check := func(args map[string]any) {
		t.Helper()
		state, err := site.linkFromArgs(args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if state.TodayFrom == "" {
			t.Fatal("missing calendar intent")
		}
		link, err := site.build(state)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := site.parseURL(link + "&window_mode=today&window_mode=today"); err == nil {
			t.Fatal("duplicate calendar metadata accepted")
		}
		read := roundTrip(t, site, compactJSON(args), link)
		if read.TodayFrom != state.TodayFrom {
			t.Fatalf("lost Today: %+v", read)
		}
		if !strings.Contains(strings.ToLower(site.summary(read)), "today") {
			t.Fatalf("summary: %s", site.summary(read))
		}
	}
	for _, page := range []string{"analyze/tokens", "analyze/human", "analyze/tools", "analyze/parallelism", "analyze/query/usage", "library"} {
		if site.Nodes[page] != nil {
			check(map[string]any{"page": page, "days": "today"})
		}
	}
	for _, card := range site.Cards.Items {
		if card.Window != nil && card.Window.Today {
			check(map[string]any{"card": card.ID, "days": "today"})
		}
	}
	for _, node := range site.Nodes {
		if node.Dataset == "tl1_attempts" || node.Dataset == "findings" {
			if _, err := site.linkFromArgs(map[string]any{"page": node.Path, "days": "today"}); err == nil {
				t.Fatalf("Today offered for %s", node.Path)
			}
		}
	}
}

func TestSiteLinkFullQueriesViewStateAndAttention(t *testing.T) {
	site := testSiteMap(t)
	args := linkArgs(t, `{"page":"analyze/query/usage","query":{"select":[{"field":"model","width":240}],"fitToScreen":false,"where":[{"field":"last_usage_at","op":">=","value":"-7d"}],"orderBy":[{"field":"model","dir":"desc"}],"limit":5,"offset":10,"aggregations":[{"id":"tokens","op":"sum","field":"total_tokens","groupBy":["model"]}]},"view":{"disclosures":{"query:usage:layout":true}},"highlight":["section:summary","row:usage-1"],"reveal":"control:disclosures"}`)
	result, err := site.linkTool(args)
	if err != nil {
		t.Fatal(err)
	}
	read := roundTrip(t, site, "full query", result["url"].(string))
	if read.Query == nil || read.Query.Offset != 10 || read.Query.Limit != 5 || len(read.Query.Select) != 1 || read.Query.FitToScreen || len(read.Query.Aggregations) != 1 || len(read.Highlight) != 2 || read.Reveal != "control:disclosures" {
		t.Fatalf("lost view: %+v", read)
	}
	advanced := linkArgs(t, `{"page":"library","view":{"search_state":{"editor":"visual","match":"literal","mode":"advanced","text":"failure","root":{"kind":"group","join":"all","children":[],"exclude":false},"scope":{"surface":"prose","roles":[],"kinds":[],"repository":"pharos","provider":"","source":"","from":"","to":"","conversation_id":"","workspace_id":""},"fuzzy":false,"caseSensitive":true,"separators":false}}}`)
	if result, err := site.linkTool(advanced); err != nil {
		t.Fatal(err)
	} else {
		roundTrip(t, site, "advanced search", result["url"].(string))
	}
	metadata := linkArgs(t, `{"page":"analyze/query/usage","query":{"select":[{"field":"model"}],"where":[{"field":"last_usage_at","op":">=","value":"2026-10-29T06:00:00Z"},{"field":"first_usage_at","op":"<","value":"+0s"}],"limit":5},"view":{"window_mode":"today","window_days":"1","window_field":"last_usage_at","window_until_field":"first_usage_at","window_dataset":"usage","window_timezone":"America/Denver","query_days":"7","query_dashboard_chart":{"v":2,"view":"trend"}}}`)
	// The registry preserves overlap fields and Today metadata even when the
	// original compact days builder cannot represent this source query.
	if result, err := site.linkTool(metadata); err != nil {
		t.Fatal(err)
	} else {
		roundTrip(t, site, "source-query time intent", result["url"].(string))
	}
	metadata["page"] = "analyze/query/tool_calls"
	metadata["query"].(map[string]any)["select"] = []any{map[string]any{"field": "id"}, map[string]any{"field": "started_at"}, map[string]any{"field": "ended_at"}}
	metadata["query"].(map[string]any)["where"] = []any{map[string]any{"field": "ended_at", "op": ">=", "value": "2026-10-28T06:00:00Z"}, map[string]any{"field": "started_at", "op": "<", "value": "+0s"}}
	window := metadata["view"].(map[string]any)
	window["window_dataset"], window["window_field"], window["window_until_field"] = "tool_calls", "ended_at", "started_at"
	if result, err := site.linkTool(metadata); err != nil {
		t.Fatal(err)
	} else {
		roundTrip(t, site, "overlapping tool calls Today", result["url"].(string))
	}
	window["window_field"] = "id"
	if _, err := site.linkTool(metadata); err == nil {
		t.Fatal("non-timestamp window accepted")
	}
	for _, input := range []string{
		`{"page":"analyze/thinking-detail","view":{"thinking":{"view":"calendar","groupBy":"model_effort","timezone":"America/Denver","range":"30d","hiddenPercentiles":["p95"],"hours":6}}}`,
		`{"page":"cards","view":{"card_window":"today","card_filters":false},"highlight":["card:tool-failures"],"reveal":"text:failed command"}`,
		`{"page":"work","options":{"work_id":"w","conversation":"c"},"view":{"reader_page":"1","reader_expanded":["message:m"],"reader_filter":["prompts","response"],"delegation_columns":["tokens","cost"],"delegation_tint":"sort"},"reveal":"message:m"}`,
	} {
		result, err := site.linkTool(linkArgs(t, input))
		if err != nil {
			t.Fatal(input, err)
		}
		roundTrip(t, site, "view", result["url"].(string))
	}
	for _, state := range []string{`{"view":"unknown"}`, `{"hours":0}`, `{"hourA":2}`, `{"hiddenPercentiles":["p99"]}`, `{"timezone":"unknown/zone"}`, `{"unknown":true}`} {
		if _, err := site.linkTool(linkArgs(t, `{"page":"analyze/thinking-detail","view":{"thinking":`+state+`}}`)); err == nil {
			t.Fatal("invalid Thinking view accepted", state)
		}
	}
	nested, err := site.linkTool(map[string]any{"url": "/work/w/conversation/c?conversation=c&reveal=message%3Am"})
	if err != nil {
		t.Fatal(err)
	}
	if nested["arguments"].(map[string]any)["options"].(map[string]any)["conversation"] != "c" {
		t.Fatal(nested)
	}
	for _, raw := range []string{
		"/cards?card_window=unknown", "/cards?card_filters=false",
		"/cards?reveal=card:a&reveal=card:b", "/cards?highlight=message:m", "/library?text_builder=" + url.QueryEscape(`{}`),
	} {
		if _, err := site.linkTool(map[string]any{"url": raw}); err == nil {
			t.Errorf("accepted invalid view %s", raw)
		}
	}
}

func TestSiteLinkJSONInspectionsAndDetailTargets(t *testing.T) {
	site := testSiteMap(t)
	copy := *site
	copy.Nodes = maps.Clone(site.Nodes)
	site = &copy
	// Even a query-capable legacy node must obey the advertised target scope.
	for _, page := range []string{"tl1/runs", "mcp", "tools"} {
		legacy := *site.Nodes["analyze/query/tool_calls"]
		legacy.Path = page
		site.Nodes[page] = &legacy
	}
	for page, node := range site.Nodes {
		advertised := false
		for _, item := range site.browseTargets(node)["items"].([]map[string]any) {
			advertised = advertised || item["pattern"] == "detail:<json>"
		}
		err := site.checkAttention(page, `detail:{"rowId":"a"}`, nil)
		if (err == nil) != advertised {
			t.Errorf("%s: advertised=%v, validation=%v", page, advertised, err)
		}
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "json-inspection-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name        string
		Valid       bool
		Inspections []any
		Token       string
		Query       map[string]any
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, sample := range cases {
		t.Run(sample.Name, func(t *testing.T) {
			query := map[string]any{"select": []map[string]string{{"field": "id"}, {"field": "tool_name"}}, "itemDetails": map[string]any{"modes": []string{"expand"}, "jsonInspections": sample.Inspections}}
			result, err := site.linkTool(map[string]any{"page": "analyze/query/mcp_calls", "query": query})
			if (err == nil) != sample.Valid {
				t.Fatalf("valid=%v: %v", sample.Valid, err)
			}
			if !sample.Valid {
				return
			}
			if sample.Name == "all inspection choices and quoted paths" && !strings.Contains(result["summary"].(string), `arguments_json find "needle"`) {
				t.Fatal("summary omitted JSON search", result)
			}
			read := roundTrip(t, site, sample.Name, result["url"].(string))
			expected, err := qt.DecodeQueryLink(sample.Token)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			encoded, _ := json.Marshal(read.Query)
			_ = json.Unmarshal(encoded, &got)
			encoded, _ = json.Marshal(expected)
			_ = json.Unmarshal(encoded, &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Go and TypeScript disagree: got %s, want %s", compactJSON(got), compactJSON(want))
			}
		})
	}
	for _, target := range []string{`detail:{"rowId":"0"}`, `detail:{"rowId":0,"field":"arguments_json"}`, `detail:{"rowId":"call-1","field":"arguments_json","path":"$[\"quote\\\"key\"][21]"}`} {
		result, err := site.linkTool(map[string]any{"page": "analyze/query/mcp_calls", "reveal": target})
		if err != nil {
			t.Fatal(target, err)
		}
		read := roundTrip(t, site, "semantic detail", result["url"].(string))
		if read.Reveal != target {
			t.Fatal(read.Reveal)
		}
	}
	for _, target := range []string{`detail:{}`, `detail:{"rowId":null}`, `detail:{"rowId":false}`, `detail:{"rowId":"a","field":"not_a_property"}`, `detail:{"rowId":"a","path":"$"}`, `detail:{"rowId":"a","field":"arguments_json","path":"$.events"}`, `detail:{"rowId":"a","field":"arguments_json","unknown":true}`} {
		if _, err := site.linkTool(map[string]any{"page": "analyze/query/mcp_calls", "reveal": target}); err == nil {
			t.Error("accepted", target)
		}
	}
	if _, err := site.linkTool(map[string]any{"page": "cards", "reveal": `detail:{"rowId":"a"}`}); err == nil {
		t.Error("detail on non-query page accepted")
	}
	if _, err := site.linkTool(map[string]any{"page": "analyze/query/mcp_calls", "highlight": []string{`detail:{"rowId":"a"}`}}); err == nil {
		t.Error("unsupported semantic multi-highlight accepted")
	}
	grouped := linkArgs(t, `{"page":"analyze/query/mcp_calls","query":{"grouping":{"source":{"select":[{"field":"tool_name"}]},"keys":["tool_name"],"summaries":[{"id":"calls","op":"count"}],"representative":null}},"reveal":"detail:{\"rowId\":\"group-id\",\"field\":\"calls\"}"}`)
	if _, err := site.linkTool(grouped); err != nil {
		t.Fatal("grouped property target", err)
	}
	help, err := site.browse(context.Background(), nil, "analyze/query/mcp_calls/query", "")
	if err != nil {
		t.Fatal(err)
	}
	if help["schema_name"] != "pharos_mcp_calls" || help["properties_path"] != "analyze/query/mcp_calls/query/properties" {
		t.Fatal("missing inspection help", help)
	}
	properties, err := site.browseTool(context.Background(), nil, map[string]any{"path": "analyze/query/mcp_calls/query/properties", "query": "arguments_json"})
	if err != nil || len(properties["items"].([]map[string]any)) != 1 {
		t.Fatal("missing paginated property help", properties, err)
	}
}

func TestSiteLinkNativeValidationAndComposition(t *testing.T) {
	site := testSiteMap(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "search-draft-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var searchCases []struct {
		Name        string
		Draft       map[string]any
		Valid       bool
		RecordValid *bool
	}
	if err := json.Unmarshal(data, &searchCases); err != nil {
		t.Fatal(err)
	}
	for _, sample := range searchCases {
		for _, owner := range []string{"search_state", "reader_search", "record_keyword"} {
			t.Run(owner+" "+sample.Name, func(t *testing.T) {
				page, value := "library", any(sample.Draft)
				if owner == "reader_search" {
					page = "work"
				}
				if owner == "record_keyword" {
					page = "analyze/query/usage"
					value = map[string]any{"field": "model", "draft": sample.Draft}
				}
				args := map[string]any{"page": page, "view": map[string]any{owner: value}}
				if page == "work" {
					args["options"] = map[string]any{"work_id": "w"}
				}
				_, err := site.linkTool(args)
				valid := sample.Valid
				if owner == "record_keyword" && sample.RecordValid != nil {
					valid = *sample.RecordValid
				}
				if (err == nil) != valid {
					t.Fatalf("valid=%v: %v", valid, err)
				}
			})
		}
	}
	large := map[string]any{}
	for key, value := range searchCases[0].Draft {
		large[key] = value
	}
	large["editor"], large["text"] = "syntax", strings.Repeat("x", 24001)
	for _, owner := range []string{"search_state", "reader_search", "record_keyword"} {
		page, value := "library", any(large)
		args := map[string]any{}
		if owner == "reader_search" {
			page = "work"
			args["options"] = map[string]any{"work_id": "w"}
		}
		if owner == "record_keyword" {
			page = "analyze/query/usage"
			value = map[string]any{"field": "model", "draft": large}
		}
		args["page"], args["view"] = page, map[string]any{owner: value}
		if _, err := site.linkTool(args); err == nil {
			t.Error("oversized search accepted", owner)
		}
	}

	for _, input := range []string{
		`{"page":"work","options":{"work_id":"w"},"view":{"reader_page":"+5"}}`,
		`{"page":"work","options":{"work_id":"w"},"view":{"reader_page":"-0"}}`,
		`{"page":"work","options":{"work_id":"w"},"view":{"sidebar_width":"+400"}}`,
		`{"page":"work","options":{"work_id":"w"},"view":{"reader_expanded":["m",null]}}`,
		`{"page":"library","days":"today","view":{"window_timezone":"Local"}}`,
		`{"page":"library","options":{"text":"foo"},"view":{"search_state":{"match":"literal","text":"bar"}}}`,
		`{"page":"analyze/query/usage","highlight":["control:q_library"]}`,
	} {
		if _, err := site.linkTool(linkArgs(t, input)); err == nil {
			t.Error("accepted incompatible state", input)
		}
	}
	for _, page := range []string{"library", "analyze/query/usage", "analyze/calls", "analyze/activity"} {
		field := "window_timezone"
		if site.Nodes[page].Dashboard != "" {
			field = "dashboard_timezone"
		}
		result, err := site.linkTool(map[string]any{"page": page, "days": "today", "view": map[string]any{field: "Asia/Tokyo"}})
		if err != nil {
			t.Fatal(page, err)
		}
		state := roundTrip(t, site, "pinned Today", result["url"].(string))
		zone, _ := time.LoadLocation("Asia/Tokyo")
		if state.TodayFrom != localMidnight(time.Now(), zone).UTC().Format(time.RFC3339Nano) {
			t.Fatal("wrong pinned midnight", state.TodayFrom)
		}
		read, err := site.linkTool(map[string]any{"url": result["url"]})
		if err != nil {
			t.Fatal(err)
		}
		rebuilt, err := site.linkTool(read["arguments"].(map[string]any))
		if err != nil {
			t.Fatal(err)
		}
		sameLink(t, "pinned arguments", rebuilt["url"].(string), result["url"].(string))
		if !strings.Contains(result["summary"].(string), "Asia/Tokyo") {
			t.Fatal("pin missing from summary", result)
		}
	}
	if _, err := site.linkTool(map[string]any{"page": "analyze/query/usage", "highlight": []any{"control:q_usage"}}); err != nil {
		t.Fatal(err)
	}
	// Native MIN/MAX and representative values retain their source type;
	// counts, sums and averages produce numbers.
	for _, op := range []string{"min", "max", "row", "count", "count_distinct", "sum", "avg"} {
		fields := []struct{ field, predicate, value, invalid string }{
			{"total_tokens", ">", "100", "not-a-number"},
		}
		if op == "min" || op == "max" || op == "row" {
			fields = append(fields,
				struct{ field, predicate, value, invalid string }{"last_usage_at", ">=", "2026-01-01T00:00:00Z", "not-a-date"},
				struct{ field, predicate, value, invalid string }{"model", "=", "gpt-6", ""},
				struct{ field, predicate, value, invalid string }{"provider", "=", "claude", ""})
		}
		for _, sample := range fields {
			t.Run(op+" "+sample.field, func(t *testing.T) {
				query := map[string]any{
					"grouping": map[string]any{
						"source": map[string]any{}, "keys": []string{"model"},
						"summaries":      []any{map[string]any{"id": "summary_value", "op": op, "field": sample.field}},
						"representative": map[string]any{"orderBy": []any{map[string]any{"field": "last_usage_at", "dir": "desc"}}},
					},
					"select": []any{map[string]any{"field": "model"}, map[string]any{"field": "summary_value"}},
					"where":  []any{map[string]any{"field": "summary_value", "op": sample.predicate, "value": sample.value}},
				}
				args := map[string]any{"page": "analyze/query/usage", "query": query}
				result, err := site.linkTool(args)
				if err != nil {
					t.Fatal(err)
				}
				roundTrip(t, site, "typed summary", result["url"].(string))
				if sample.invalid != "" {
					query["where"].([]any)[0].(map[string]any)["value"] = sample.invalid
					if _, err := site.linkTool(args); err == nil {
						t.Fatal("invalid summary filter value accepted")
					}
				}
			})
		}
	}
	for _, page := range []string{"library", "analyze/query/usage"} {
		t.Run(page+" host search composition", func(t *testing.T) {
			draft := map[string]any{"match": "literal", "text": "failure"}
			key, param, column := "search_state", "text_builder", "title"
			var search any = draft
			if page != "library" {
				key, param, column = "record_keyword", "record_usage", "model"
				search = map[string]any{"field": "model", "draft": draft}
			}
			for _, suppliedHost := range []bool{false, true} {
				query := map[string]any{"select": []any{map[string]any{"field": column}}, "limit": 5}
				if suppliedHost {
					query["hostState"] = map[string]any{"version": 1, "params": map[string]any{}}
				}
				args := map[string]any{"page": page, "query": query, "view": map[string]any{key: search}}
				result, err := site.linkTool(args)
				if err != nil {
					t.Fatal(err)
				}
				link := result["url"].(string)
				read := roundTrip(t, site, "host search", link)
				if read.Query == nil {
					t.Fatal("lost native query")
				}
				address, _ := url.Parse(link)
				if address.Query().Get(param) == "" {
					t.Fatal("lost search parameter", link)
				}
				if suppliedHost && !sameJSONText(read.Query.HostState.Params[param][0], address.Query().Get(param)) {
					t.Fatal("native host and view search disagree", read.Query.HostState)
				}
				if _, err := site.linkTool(map[string]any{"url": link}); err != nil {
					t.Fatal("successful link failed readback", err)
				}
				if suppliedHost && len(query["hostState"].(map[string]any)["params"].(map[string]any)) != 0 {
					t.Fatal("composition mutated caller arguments")
				}
			}
			conflict := map[string]any{"version": 1, "params": map[string]any{param: []string{`{}`}}}
			if _, err := site.linkTool(map[string]any{"page": page, "query": map[string]any{"hostState": conflict}, "view": map[string]any{key: search}}); err == nil || !strings.Contains(err.Error(), "disagrees") {
				t.Fatal("conflicting host search accepted", err)
			}
		})
	}
	invalid := []struct{ input, detail string }{
		{`{"page":"analyze/query/usage","view":{"window_mode":"relative"}}`, "window_dataset"},
		{`{"page":"analyze/query/usage","query":{"where":[{"field":"nonexistent","op":"=","value":"x"}]}}`, "query.where[0]"},
		{`{"page":"analyze/query/usage","query":{"select":[{"field":"nonexistent"}]}}`, "query.select[0].field"},
		{`{"page":"analyze/query/usage","query":{"orderBy":[{"field":"cost","dir":"desc"}]}}`, "query.orderBy[0].field"},
		{`{"page":"analyze/query/usage","query":{"aggregations":[{"id":"a","op":"total","groupBy":[]}]}}`, "Choices:"},
		{`{"page":"analyze/query/usage","query":{"aggregations":[{"id":"a","op":"sum","field":"total_tokens","groupBy":[],"diagnostics":["lost predicate"]}]}}`, "lost predicate"},
		{`{"page":"analyze/query/usage","query":{"select":["model_family"]}}`, "column objects"},
		{`{"page":"analyze/query/usage","query":{"where":[{"field":"model","op":"same_as","value":"x"}]}}`, "Choices:"},
		{`{"page":"cards","highlight":["card:no-such-card"]}`, "Choices:"},
		{`{"page":"cards","reveal":"control:nonexistent"}`, "Choices:"},
		{`{"page":"cards","view":{"bad_field":true}}`, "Choices:"},
		{`{"page":"cards","view":{"card_window":false}}`, "today"},
		{`{"page":"analyze/query/usage","query":{"where":[{"field":"last_usage_at","op":">=","value":"-7d"}]},"days":30}`, "already has source time predicates"},
		{`{"page":"analyze/query/usage","query":{"where":[{"field":"last_usage_at","op":">=","value":"-7d"},{"field":"last_usage_at","op":"<","value":"+0s"}]},"view":{"window_mode":"relative","window_days":"30","window_field":"last_usage_at","window_dataset":"usage"}}`, "disagrees"},
	}
	for _, item := range invalid {
		_, err := site.linkTool(linkArgs(t, item.input))
		if err == nil || !strings.Contains(err.Error(), item.detail) {
			t.Errorf("%s: wanted %q, got %v", item.input, item.detail, err)
		}
	}
	for _, raw := range []string{
		`{"select":[{"field":"model"}],"where":[{"field":"nonexistent","op":"=","value":"x"}],"limit":5}`,
		`{"select":[{"field":"model"}],"aggregations":[{"id":"a","op":"total","groupBy":[]}]}`,
	} {
		token := mustQuery(t, raw)
		if _, err := site.linkTool(map[string]any{"url": "/analyze?analyze_mode=query&analyze_topic=tokens&analyze_data=usage&q_usage=" + url.QueryEscape(token)}); err == nil {
			t.Error("accepted invalid native browser URL", raw)
		}
	}
	for _, input := range []string{
		`{"page":"analyze/query/usage","query":{"select":[{"field":"model"}],"where":[{"field":"provider","op":"=","value":"claude"}],"limit":5},"days":7,"reveal":"row:u"}`,
		`{"page":"analyze/query/usage","query":{"select":[{"field":"model"}],"limit":5},"days":"today"}`,
		`{"page":"analyze/query/usage","query":{"select":[{"field":"model"}],"limit":5},"days":"today","view":{"window_timezone":"Asia/Tokyo"}}`,
		`{"page":"analyze/query/usage","query":{"grouping":{"source":{"where":[{"field":"provider","op":"=","value":"claude"}]},"keys":["model"],"summaries":[{"id":"sum_tokens","op":"sum","field":"total_tokens"}],"representative":null},"select":[{"field":"model"},{"field":"sum_tokens"}],"where":[{"field":"sum_tokens","op":">","value":"100"}],"orderBy":[{"field":"sum_tokens","dir":"desc"}]},"days":7}`,
		`{"page":"library","view":{"search_state":{"match":"literal","text":"failure","scope":{"provider":"claude"}}}}`,
		`{"page":"cards","view":{"card_window":30}}`,
		`{"page":"work","options":{"work_id":"w"},"view":{"delegation_page":2,"reader_page":3,"sidebar_width":450}}`,
	} {
		result, err := site.linkTool(linkArgs(t, input))
		if err != nil {
			t.Error(input, err)
			continue
		}
		roundTrip(t, site, "composed", result["url"].(string))
		if strings.Contains(input, `"query"`) {
			if !strings.Contains(result["title"].(string), "Custom query") || !strings.Contains(result["summary"].(string), "limit") {
				t.Error("query description is not useful", result)
			}
		}
	}
}
