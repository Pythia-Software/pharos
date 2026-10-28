package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
		"query beyond where":   "/analyze?analyze_mode=query&analyze_topic=tokens&analyze_data=usage&q_usage=" + mustQuery(t, `{"select":[{"field":"model"}],"where":[]}`),
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
	if !state.Changed || state.Card != "human-word-total" || state.Days != 30 || chart["rollingPeriods"] != float64(30) || chart["scale"] != "logarithmic" {
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
	if read, _ := site.parseURL(page["url"].(string)); !read.Changed || read.Days != 30 || read.Chart.toMap()["scale"] != "logarithmic" {
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
