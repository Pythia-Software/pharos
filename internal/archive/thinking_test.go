package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
)

func seedThinking(t *testing.T, c *Catalog) {
	seedToolCalls(t, c, 0)
	tx := seedTx(t, c.DB)
	exec := func(s string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(s, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE workspaces SET source_kind=CASE id WHEN 'ws-0' THEN 'claude' WHEN 'ws-2' THEN 'conductor' ELSE source_kind END")
	exec(`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('conv-0','conv-2','native-alias',1,'{}')`)
	exec(`INSERT INTO conversation_group_membership(conversation_id,workspace_id,active) VALUES('conv-3','ws-3',0)`)
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	stamp := func(seconds float64) string { return base.Add(time.Duration(seconds * 1e9)).Format(time.RFC3339Nano) }
	times := []float64{0, 20, 40, 60, 80, 100, 120, 800, 1600, 1700, 1800}
	for i, at := range times {
		model := "timed-model"
		if i == 9 {
			model = "second-model"
		}
		exec(`INSERT INTO model_requests(id,conversation_id,agent_session_id,native_id,sequence,requested_at,model,effort,input_tokens,output_tokens,reasoning_output_tokens) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprint("request-", i), "conv-0", "session-0", fmt.Sprint("response-", i), i, stamp(at), model, "high", 1000+i*100, 100, 50)
	}
	anchors := map[int]float64{1: 10, 2: 35, 3: 30, 4: 70, 5: 101, 6: 119.95, 7: 150, 8: 1000, 9: 1690, 10: 1799.9}
	for i, at := range anchors {
		stream := "session-0"
		if i == 4 {
			stream = "session-1"
		}
		exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,sequence,provider,kind,tool_name,tool_category,status,ended_at,next_request_id) VALUES(?,?,?,?,?,'claude','tool_call','Read','read','ok',?,?)`, fmt.Sprint("tool-", i), "ws-0", "conv-0", stream, i, stamp(at), fmt.Sprint("request-", i))
	}
	exec(`INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,sequence,provider,kind,tool_name,tool_category,status,next_request_id) VALUES('missing-end','ws-0','conv-0','session-0',20,'claude','tool_call','Read','read','ok','request-2')`)
	for _, conv := range []int{1, 2, 3} {
		exec(`INSERT INTO model_requests(id,conversation_id,agent_session_id,native_id,sequence,requested_at,model,effort) VALUES(?,?,?,?,0,?,'no-clock','')`, fmt.Sprint("other-", conv), fmt.Sprint("conv-", conv), fmt.Sprint("session-", conv), fmt.Sprint("other-response-", conv), stamp(20))
	}
	for i, body := range []string{`{"message":{"id":"response-1"},"thinkingDurationMs":9000}`, `{"message":{"id":"response-1"},"thinkingDurationMs":10000}`, `{"type":"cost-state","totalDuration":10000,"totalAPIDuration":1000,"totalToolDuration":2000}`, `{"type":"cost-state","totalDuration":10000,"totalAPIDuration":8000,"totalToolDuration":4000}`, `{"message":{"id":"response-8"},"thinkingDurationMs":true}`, `{"type":"cost-state","totalDuration":true,"totalAPIDuration":1,"totalToolDuration":1}`} {
		exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES(?,'conv-0',?,'assistant','metadata',?,'hash',?,?)`, fmt.Sprint("meta-", i), fmt.Sprint("native-meta-", i), body, stamp(20), i)
	}
	commitSeed(t, tx)
}
func TestThinkingProjection(t *testing.T) {
	c, config := testCatalog(t)
	seedThinking(t, c)
	o, err := readThinkingOptions(url.Values{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	records, err := c.thinkingRecords(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 12 {
		t.Fatalf("retained %d, alias/inactive records must not duplicate", len(records))
	}
	byID := map[string]thinkingRecord{}
	for _, r := range records {
		byID[r.ID] = r
	}
	for id, want := range map[string]string{"request-0": "No linked tool timestamp", "request-2": "Incomplete tool timestamps", "request-3": "Stale tool anchor", "request-4": "Different agent stream", "request-5": "Nonpositive gap", "request-6": "Below 100 ms", "request-7": "Above 600 seconds"} {
		r := byID[id]
		if r.Quality != want || r.Gap != nil {
			t.Errorf("%s: %s / %v", id, r.Quality, r.Gap)
		}
	}
	if *byID["request-1"].Gap != 10 || *byID["request-8"].Gap != 600 || byID["request-10"].Gap == nil || *byID["request-10"].Gap != .1 {
		t.Fatal("exact boundary durations", byID)
	}
	o.From = byID["request-9"].At
	o.Model = "second-model"
	filtered, err := c.thinkingRecords(context.Background(), o)
	if err != nil || len(filtered) != 1 || filtered[0].Gap == nil || *filtered[0].Gap != 10 {
		t.Fatal("filter must preserve prior response evidence", filtered, err)
	}
	server := testServer(t, config, c)
	w := apiCall(t, server, config.APIToken, http.MethodGet, "/api/thinking?overview=1&timezone=UTC", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result thinkingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Retained != 12 || result.Timed != 4 || result.Summary.Median == nil || *result.Summary.Median != 10 {
		t.Fatal(result)
	}
	if len(result.Coverage) != 3 || len(result.AgentCoverage) == 0 {
		t.Fatal("coverage must include untimed models and harnesses", result.Coverage, result.AgentCoverage)
	}
	invalidField, _ := qt.EncodeQueryLink(qt.LinkQuery{Where: []qt.WhereTerm{{Field: "not_a_column", Op: "=", Value: "x"}}})
	if w := apiCall(t, server, config.APIToken, http.MethodGet, "/api/thinking?filters="+url.QueryEscape(invalidField), nil); w.Code != 400 {
		t.Fatal("invalid native fields must not broaden the population", w.Code, w.Body.String())
	}
	filter, _ := qt.EncodeQueryLink(qt.LinkQuery{Where: []qt.WhereTerm{{Field: "model", Op: "=", Value: "second-model"}}})
	w = apiCall(t, server, config.APIToken, http.MethodGet, "/api/thinking?filters="+url.QueryEscape(filter), nil)
	var selected thinkingResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &selected) != nil || selected.Retained != 1 || selected.Timed != 1 {
		t.Fatal("native filters must intersect the dashboard population", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/thinking?filters=invalid", "/api/thinking?period=wrong", "/api/thinking?range=custom&from=invalid", "/api/thinking?minimum=-1", "/api/thinking?hourA=2", "/api/thinking?timezone=Unknown%2Fzone"} {
		w = apiCall(t, server, config.APIToken, http.MethodGet, path, nil)
		if w.Code != 400 {
			t.Fatalf("invalid controls %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := apiCall(t, server, "", http.MethodGet, "/api/thinking", nil); w.Code != 401 {
		t.Fatal("timing must require authorization", w.Code)
	}
	document, _ := querySchemaDocument("model_requests")
	schema, err := querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := thinkingRequestDataset.Rows(context.Background(), c.reader(), querytable.Query{Select: []string{"id", "gap_seconds"}, Limit: 20}, schema)
	if err != nil || rows.Total != 12 {
		t.Fatal("native query and dashboard population differ", rows, err)
	}
	validation, err := c.thinkingValidation(context.Background(), thinkingOptions{}, records)
	if err != nil {
		t.Fatal(err)
	}
	if validation.Pairs != 1 || *validation.MedianRatio != 1 || validation.CounterCount != 1 || validation.CounterOverlap != 1 {
		t.Fatal("timers must deduplicate and counters use latest", validation)
	}
}
func TestThinkingAggregation(t *testing.T) {
	zone, _ := time.LoadLocation("America/Denver")
	o, _ := readThinkingOptions(url.Values{"timezone": {"America/Denver"}, "view": {"calendar"}}, time.Now())
	records := []thinkingRecord{}
	for day := 1; day <= 5; day++ {
		for _, hour := range []int{9, 18} {
			for i := 0; i < 24; i++ {
				gap := 2.0
				ratio := 1.0
				if hour == 18 {
					ratio = .5
				}
				records = append(records, thinkingRecord{ID: fmt.Sprint(day, hour, i), Conversation: fmt.Sprint(i % 3), Model: "model", Effort: "high", At: time.Date(2026, 10, day, hour, i, 0, 0, zone), Gap: &gap, Ratio: &ratio, Reasoning: float64(i + 1)})
			}
		}
	}
	result, err := aggregateThinking(context.Background(), records, o)
	if err != nil {
		t.Fatal(err)
	}
	g := result.Groups[0]
	if result.Timed != 240 || result.Adjusted != 240 || g.Comparison.Days != 5 || *g.Comparison.Ratio != .5 {
		t.Fatal(result, g)
	}
	o.Rows = "month"
	monthly, err := aggregateThinking(context.Background(), records, o)
	if err != nil || len(monthly.Groups[0].Calendar) != 2 || *monthly.Groups[0].Calendar[1].Raw != 2 {
		t.Fatal("pool individual observations before medians", monthly, err)
	}
	if result.CalendarBounds[result.CalendarPeriods[0]].From != "2026-10-01T06:00:00Z" {
		t.Fatal("named-zone date boundaries", result.CalendarBounds)
	}
	o.View = "reasoning"
	records[0].Gap = nil
	tokens, _ := aggregateThinking(context.Background(), records, o)
	if tokens.Groups[0].Stats.N != 240 || tokens.Timed != 239 {
		t.Fatal("reasoning population must not depend on timing")
	}
	for i, v := range thinkingEdges[:len(thinkingEdges)-1] {
		if thinkingBin(v) != i {
			t.Fatal("left inclusive bins", v)
		}
	}
	if thinkingBin(600) != 15 {
		t.Fatal("final maximum belongs in last bin")
	}
	stats := thinkingSummarize([]float64{1, 2, 8, 20})
	if *stats.Median != 5 || *stats.P95 != 18.199999999999996 {
		if math.Abs(*stats.P95-18.2) > 1e-12 {
			t.Fatal(stats)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := aggregateThinking(ctx, records, o); err == nil {
		t.Fatal("cancelled request must stop")
	}
}
func TestThinkingModelsAndPairs(t *testing.T) {
	rs := []thinkingRecord{}
	for conv := 0; conv < 30; conv++ {
		for i := 0; i < 12; i++ {
			input := float64(1000 + (i*137+conv*83)%1000)
			reasoning := float64(20 + (i*17+conv*11)%100)
			output := reasoning + float64(30+(i*11+conv*13)%100)
			gap := math.Exp(float64(conv)*.01) * math.Pow(input, .4) * math.Pow(1+reasoning, .2) * math.Pow(1+output-reasoning, .1)
			rs = append(rs, thinkingRecord{ID: fmt.Sprint(conv, "/", i), Model: "fit-model", Effort: "high", Conversation: fmt.Sprint(conv), Stream: "main", At: time.Date(2026, 10, 20, 0, i, 0, 0, time.UTC), Sequence: i, Input: input, Output: output, Reasoning: reasoning, Gap: &gap})
		}
	}
	factors := thinkingModels(context.Background(), rs, true, true)
	if len(factors) != 3 {
		t.Fatal("three supported factors", factors)
	}
	for _, f := range factors {
		want := map[string]float64{"context": math.Pow(2, .4), "reasoning": math.Pow(2, .2), "other": math.Pow(2, .1)}[f.Factor]
		if math.Abs(f.Ratio-want) > .01 {
			t.Fatal("known within-conversation factor", f, want)
		}
	}
	n := 0
	for _, r := range rs {
		if r.Ratio != nil {
			n++
		}
	}
	if n < 250 {
		t.Fatal("conversation-held-out estimates need supported coverage", n)
	}
	pairs := []thinkingRecord{}
	for i := 0; i < 6; i++ {
		gap, context, output := 10.0, 1000.0, 100.0
		if i >= 3 {
			gap, context, output = 2, 200, 20
		}
		pairs = append(pairs, thinkingRecord{ID: fmt.Sprint(i), Model: "pair", Effort: "high", Conversation: "conv", Stream: "main", At: time.Date(2026, 10, 20, 0, i, 0, 0, time.UTC), Sequence: i, Gap: &gap, Input: context, Output: output, Compaction: i == 3})
	}
	compactions := thinkingCompactions(pairs)
	if len(compactions) != 1 || compactions[0].Gap != .2 || compactions[0].Context != .2 {
		t.Fatal(compactions)
	}
	boundaryGap, nextGap := 500.0, 4.0
	pairs[3].Gap, pairs[5].Gap = &boundaryGap, &nextGap
	compactions = thinkingCompactions(pairs)
	if len(compactions) != 1 || compactions[0].After != 3 || compactions[0].Gap != .3 {
		t.Fatal("compaction-crossing wait must not enter the after median", compactions)
	}
	pairs[4].Gap = nil
	if len(thinkingCompactions(pairs)) != 0 {
		t.Fatal("the crossing wait cannot satisfy the two clean after-gap minimum")
	}
	cleanGap := 2.0
	pairs[4].Gap = &cleanGap
	pairs[4].Previous = "filtered-out-request"
	if len(thinkingCompactions(pairs)) != 0 {
		t.Fatal("filtered requests must not bridge a compaction pair")
	}
	pairs[4].Previous = ""
	pairs[4].Effort = "low"
	if len(thinkingCompactions(pairs)) != 0 {
		t.Fatal("changing effort is not a paired compaction")
	}
	trace := thinkingTraces(rs[:12])
	if len(trace) != 1 || trace[0].Points[4].Smooth != nil || trace[0].Points[5].Smooth == nil {
		t.Fatal("trace smoothing needs six observations", trace)
	}
	if !reflect.DeepEqual(thinkingRanks([]float64{8, 2, 2}), []float64{2, .5, .5}) {
		t.Fatal("tied ranks")
	}
}

func TestThinkingDSTBuckets(t *testing.T) {
	zone, _ := time.LoadLocation("America/Denver")
	first, _ := time.Parse(time.RFC3339, "2026-11-01T07:30:00Z")
	second := first.Add(time.Hour)
	a, b := thinkingPeriod(first, "hour", 1, 1, zone), thinkingPeriod(second, "hour", 1, 1, zone)
	if a.Equal(b) || !thinkingNext(a, "hour", 1, 1).Equal(b) {
		t.Fatal("repeated local hours must remain separate", a, b)
	}
	start := thinkingPeriod(first, "hours", 6, 1, zone)
	next := thinkingNext(start, "hours", 6, 1)
	if next.Hour() != 6 || next.Sub(start) != 7*time.Hour {
		t.Fatal("six civil hours include the DST transition", start, next)
	}
	if !thinkingPeriod(next.Add(time.Minute), "hours", 6, 1, zone).Equal(next) {
		t.Fatal("period assignment and boundaries disagree")
	}
	for _, sample := range []struct{ zone, date string }{
		{"America/Denver", "2026-03-08"}, {"America/Denver", "2026-11-01"},
		{"Europe/Berlin", "2026-03-29"}, {"Europe/Berlin", "2026-10-25"},
		{"Australia/Lord_Howe", "2026-10-04"}, {"Australia/Lord_Howe", "2026-04-05"},
	} {
		for _, hours := range []int{1, 2, 3, 4, 6, 8, 12, 24, 5} {
			t.Run(fmt.Sprintf("%s/%s/%dh", sample.zone, sample.date, hours), func(t *testing.T) {
				zone, err := time.LoadLocation(sample.zone)
				if err != nil {
					t.Fatal(err)
				}
				date, err := time.ParseInLocation("2006-01-02", sample.date, zone)
				if err != nil {
					t.Fatal(err)
				}
				from, to := date.AddDate(0, 0, -1), date.AddDate(0, 0, 2)
				records := []thinkingRecord{}
				for at := from; at.Before(to); at = at.Add(15 * time.Minute) {
					gap := 2.0
					records = append(records, thinkingRecord{ID: at.Format(time.RFC3339), Conversation: "one", Model: "one", At: at, Gap: &gap})
				}
				o, err := readThinkingOptions(url.Values{"view": {"time"}, "period": {"hours"}, "hours": {fmt.Sprint(hours)}, "timezone": {sample.zone}, "range": {"custom"}, "from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				v, err := aggregateThinking(t.Context(), records, o)
				if err != nil {
					t.Fatal(err)
				}
				shown := 0
				for _, key := range v.Periods {
					shown += v.Groups[0].Periods[key].Stats.N
				}
				if shown != len(records) {
					t.Fatalf("renderable buckets contain %d of %d observations", shown, len(records))
				}
				for _, r := range records {
					key := thinkingPeriodKey(thinkingPeriod(r.At, o.Period, hours, 1, zone), o.Period)
					bounds, ok := v.PeriodBounds[key]
					lo, _ := time.Parse(time.RFC3339Nano, bounds.From)
					hi, _ := time.Parse(time.RFC3339Nano, bounds.To)
					if !ok || r.At.Before(lo) || !r.At.Before(hi) {
						t.Fatalf("%s assigned outside its rendered bucket %s: %+v", r.At, key, bounds)
					}
				}
			})
		}
	}

}

func TestThinkingCachedPopulationParityAndFreshness(t *testing.T) {
	c, _ := testCatalog(t)
	seedThinking(t, c)
	ctx := t.Context()
	if _, err := c.DB.Exec(`UPDATE model_requests SET requested_at='2026-10-20T00:28:20.1236Z' WHERE id='request-9'`); err != nil {
		t.Fatal(err)
	}
	original, err := c.thinkingSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := qt.EncodeQueryLink(qt.LinkQuery{Where: []qt.WhereTerm{{Field: "model", Op: "=", Value: "second-model"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []url.Values{{}, {"model": {"second-model"}}, {"effort": {"high"}}, {"range": {"custom"}, "from": {"2026-10-20T00:28:20Z"}}, {"filters": {token}}, {"range": {"custom"}, "from": {"2026-10-20T00:28:20.124Z"}, "to": {"2026-10-20T00:28:20.125Z"}}} {
		o, err := readThinkingOptions(q, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		direct, err := c.thinkingRecords(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := c.thinkingNativeIDs(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := selectThinkingRecords(ctx, original, o, ids)
		if err != nil {
			t.Fatal(err)
		}
		selected = append([]thinkingRecord{}, selected...)
		sort.Slice(direct, func(i, j int) bool { return direct[i].ID < direct[j].ID })
		sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
		if !reflect.DeepEqual(direct, selected) {
			t.Fatalf("cached filtering differs from native timing projection: %v\n%+v\n%+v", q, direct, selected)
		}
	}
	// Native filters may require timing itself and may combine OR branches.
	token, _ = qt.EncodeQueryLink(qt.LinkQuery{Where: []qt.WhereTerm{{Any: []qt.WhereClause{{Field: "gap_seconds", Op: ">", Value: "20"}, {Field: "effort", Op: "=", Value: "Unreported"}}}}})
	o, err := readThinkingOptions(url.Values{"filters": {token}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ids, err := c.thinkingNativeIDs(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := selectThinkingRecords(ctx, original, o, ids)
	if err != nil || len(filtered) != 2 {
		t.Fatal("native timing/OR filter", len(filtered), err)
	}
	validation, err := c.thinkingValidation(ctx, thinkingOptions{}, original)
	if err != nil || validation.Pairs != 1 || *validation.MedianRatio != 1 {
		t.Fatal(validation, err)
	}
	other := otherProcess(t, c)
	if _, err := other.DB.Exec(`UPDATE messages SET text='{"message":{"id":"response-1"},"thinkingDurationMs":20000}' WHERE id='meta-1'`); err != nil {
		t.Fatal(err)
	}
	updatedValidation, err := c.thinkingValidation(ctx, thinkingOptions{}, original)
	if err != nil || *updatedValidation.MedianRatio != 2 || *validation.MedianRatio != 1 {
		t.Fatal("validation cache freshness/immutability", updatedValidation, err)
	}
	if _, err := other.DB.Exec(`UPDATE tool_calls SET ended_at='2026-10-20T00:00:19Z' WHERE id='tool-1'`); err != nil {
		t.Fatal(err)
	}
	latest, err := c.thinkingSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	find := func(rs []thinkingRecord, id string) float64 {
		for _, r := range rs {
			if r.ID == id && r.Gap != nil {
				return *r.Gap
			}
		}
		return -1
	}
	if find(original, "request-1") != 10 || find(latest, "request-1") != 1 {
		t.Fatal("external commits must invalidate without mutating an earlier snapshot")
	}
	if _, err := other.DB.Exec(`UPDATE conversation_group_membership SET active=0 WHERE conversation_id='conv-0'`); err != nil {
		t.Fatal(err)
	}
	// Marking a previously unlisted conversation inactive also invalidates aliases.
	if _, err := other.DB.Exec(`INSERT OR REPLACE INTO conversation_group_membership(conversation_id,workspace_id,active) VALUES('conv-0','ws-0',0)`); err != nil {
		t.Fatal(err)
	}
	next, err := c.thinkingSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if find(next, "request-1") != -1 {
		t.Fatal("identity/group changes left the timing cache stale")
	}
}

func TestThinkingFitCacheIsolation(t *testing.T) {
	c, _ := testCatalog(t)
	records := []thinkingRecord{}
	for conv := 0; conv < 30; conv++ {
		for i := 0; i < 12; i++ {
			input := float64(1000 + (i*137+conv*83)%1000)
			reasoning := float64(20 + (i*17+conv*11)%100)
			output := reasoning + float64(30+(i*11+conv*13)%100)
			gap := math.Exp(float64(conv)*.01) * math.Pow(input, .4) * math.Pow(1+reasoning, .2) * math.Pow(1+output-reasoning, .1)
			records = append(records, thinkingRecord{ID: fmt.Sprint(conv, "/", i), Model: "fit-model", Effort: "high", Conversation: fmt.Sprint(conv), Stream: "main", At: time.Date(2026, 10, 20, 0, i, 0, 0, time.UTC), Sequence: i, Input: input, Output: output, Reasoning: reasoning, Gap: &gap})
		}
	}
	first, err := c.thinkingFit(t.Context(), records, true, true)
	if err != nil || len(first.Factors) == 0 {
		t.Fatal(first, err)
	}
	n := 0
	for i, r := range records {
		if r.Ratio != nil {
			t.Fatal("fit mutated shared request population")
		}
		if first.Ratios[i] > 0 {
			n++
		}
	}
	if n < 250 {
		t.Fatal("cached fit lost supported estimates", n)
	}
	again, err := c.thinkingFit(t.Context(), records, true, true)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("same cohort changed", err)
	}
	// An overlapping source update may retain the same request IDs. Its inputs
	// must not reuse an older cohort's fit, even within the same cache generation.
	for i := 0; i < 6; i++ {
		records[i].Input *= 2
		if _, err := c.thinkingFit(t.Context(), records, true, true); err != nil {
			t.Fatal(err)
		}
	}
	c.derived.mu.Lock()
	defer c.derived.mu.Unlock()
	fits := 0
	for key, e := range c.derived.entries {
		if strings.HasPrefix(key, "thinking:fit:") {
			fits++
			if e.compute != nil {
				t.Fatal("completed fit retains its population closure")
			}
		}
	}
	if fits != 4 {
		t.Fatal("fit cohorts must be bounded and distinguish updated inputs", fits)
	}
}
