package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"slices"
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
	exec(`UPDATE model_requests SET speed=CASE id WHEN 'request-1' THEN 'fast' ELSE 'standard' END,service_tier=CASE id WHEN 'request-1' THEN 'priority' WHEN 'request-8' THEN 'default' END,speed_source=CASE WHEN id IN ('request-1','request-8') THEN 'service_tier' ELSE 'assumed_standard' END`)
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
	if byID["request-1"].Speed != "fast" || byID["request-8"].Speed != "standard" || byID["request-9"].Speed != "standard" {
		t.Fatal("speed must retain known modes and missing evidence", byID)
	}
	// Seeded rows are marked and derived by the query; stored rows must match.
	if pending, err := c.requestTimingsPending(t.Context()); err != nil || !pending {
		t.Fatal("ledger writes must mark their conversations", pending, err)
	}
	read, err := c.loadThinkingValidation(t.Context(), records)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.refreshRequestTimings(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stored, err := c.loadThinkingValidation(t.Context(), records); err != nil || !reflect.DeepEqual(stored, read) {
		t.Fatalf("stored validation evidence differs from message bodies: %v\n%+v\n%+v", err, stored, read)
	}
	if pending, err := c.requestTimingsPending(t.Context()); err != nil || pending {
		t.Fatal("refresh must store every marked conversation", pending, err)
	}
	stored, err := c.thinkingRecords(t.Context(), o)
	sort.Slice(stored, func(i, j int) bool { return stored[i].ID < stored[j].ID })
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	if err != nil || !reflect.DeepEqual(stored, records) {
		t.Fatalf("stored timings differ from derived ones: %v\n%+v\n%+v", err, stored, records)
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
	// The endpoint keeps only what needs whole conversations in order; the
	// counts, waits and coverage come from model_requests (thinking-query.ts).
	if result.Timezone != "UTC" || result.Groups == nil || result.Validation != nil || !strings.Contains(w.Header().Get("Server-Timing"), "fit") {
		t.Fatal(w.Body.String())
	}
	for _, unused := range []string{"Retained", "summary", "coverage", "periods", "Models"} {
		if strings.Contains(w.Body.String(), `"`+unused+`"`) {
			t.Fatalf("the endpoint still answers %s: %s", unused, w.Body.String())
		}
	}
	invalidField, _ := qt.EncodeQueryLink(qt.LinkQuery{Where: []qt.WhereTerm{{Field: "not_a_column", Op: "=", Value: "x"}}})
	if w := apiCall(t, server, config.APIToken, http.MethodGet, "/api/thinking?filters="+url.QueryEscape(invalidField), nil); w.Code != 400 {
		t.Fatal("invalid native fields must not broaden the population", w.Code, w.Body.String())
	}
	filter, _ := qt.EncodeQueryLink(qt.LinkQuery{Where: []qt.WhereTerm{{Field: "model", Op: "=", Value: "second-model"}}})
	if w = apiCall(t, server, config.APIToken, http.MethodGet, "/api/thinking?view=calendar&filters="+url.QueryEscape(filter), nil); w.Code != 200 {
		t.Fatal("native filters", w.Code, w.Body.String())
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

	// The dashboard's wait bands and local hours, as native fields.
	banded, err := thinkingRequestDataset.Rows(t.Context(), c.reader(), querytable.Query{Select: []string{"id", "gap_seconds", "gap_bin", "hour_of_day", "requested_at"}, Limit: 20}, schema)
	if err != nil || banded.Total != 12 {
		t.Fatal(banded, err)
	}
	for _, row := range banded.Rows {
		at, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(row["requested_at"]))
		if row["hour_of_day"] == nil || int(floatOr(row["hour_of_day"])) != at.Local().Hour() {
			t.Fatalf("hour of day %v for %v", row["hour_of_day"], at)
		}
		gap, ok := row["gap_seconds"].(float64)
		if !ok {
			if row["gap_bin"] != nil {
				t.Fatalf("a request without a wait has band %v", row["gap_bin"])
			}
			continue
		}
		if row["gap_bin"] == nil || int(floatOr(row["gap_bin"])) != thinkingBin(gap) {
			t.Fatalf("wait %v in band %v, want %d", gap, row["gap_bin"], thinkingBin(gap))
		}
	}
	// In a viewer's zone, here one 5:45 ahead of UTC, the calendar fields
	// follow that zone rather than the service's.
	kathmandu, _ := time.LoadLocation("Asia/Kathmandu")
	zoned, err := thinkingZonedDataset(thinkingRequestDataset, "Asia/Kathmandu")
	if err != nil {
		t.Fatal(err)
	}
	local, err := zoned.Rows(t.Context(), c.reader(), querytable.Query{Select: []string{"id", "requested_at", "day", "week", "month", "hour_of_day"}, Limit: 20}, schema)
	if err != nil || local.Total != 12 {
		t.Fatal(local, err)
	}
	for _, row := range local.Rows {
		at, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(row["requested_at"]))
		at = at.In(kathmandu)
		monday := at.AddDate(0, 0, -((int(at.Weekday()) + 6) % 7))
		if row["day"] != at.Format("2006-01-02") || row["week"] != monday.Format("2006-01-02") || row["month"] != at.Format("2006-01") || int(floatOr(row["hour_of_day"])) != at.Hour() {
			t.Fatalf("zoned calendar %v for %v", row, at)
		}
	}
	if _, err := thinkingZonedDataset(thinkingRequestDataset, "Nowhere/Zone"); err == nil {
		t.Fatal("an unknown zone was accepted")
	}
	count := ungroupedRowsRequest(time.Now(), qt.WhereTerm{Field: "hour_of_day", Op: ">=", Value: "0"})
	if w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/model_requests/query-v3?timezone=Asia%2FKathmandu", count); w.Code != 200 {
		t.Fatalf("a zoned count answered %d: %s", w.Code, w.Body.String())
	}
	if w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/model_requests/query-v3?timezone=Nowhere%2FZone", count); w.Code != 400 {
		t.Fatalf("an unknown zone answered %d: %s", w.Code, w.Body.String())
	}
	for _, edge := range []float64{0.1, 0.15, 0.2, 499.9, 500, 600} {
		var band int
		if err := c.reader().QueryRow("SELECT "+thinkingBinSQL("?1"), edge).Scan(&band); err != nil || band != thinkingBin(edge) {
			t.Fatalf("band of %v: %d, want %d (%v)", edge, band, thinkingBin(edge), err)
		}
	}

	for _, field := range []string{"service_tier", "speed_source"} {
		selected, err := thinkingRequestDataset.Rows(t.Context(), c.reader(), querytable.Query{Select: []string{"id", "speed", field}, Where: []querytable.WhereTerm{{Field: "speed", Op: "=", Value: "fast"}}, Limit: 20}, schema)
		want := map[string]string{"service_tier": "priority", "speed_source": "service_tier"}[field]
		if err != nil || selected.Total != 1 || selected.Rows[0][field] != want {
			t.Fatalf("native inference field %s: %+v %v", field, selected, err)
		}
	}
	for _, by := range []string{"speed", "model_speed", "model_effort_speed"} {
		selected, err := thinkingRequestDataset.Rows(t.Context(), c.reader(), querytable.Query{Select: []string{"id", "speed", by}, Where: []querytable.WhereTerm{{Field: "speed", Op: "=", Value: "fast"}}, Limit: 20}, schema)
		if err != nil || selected.Total != 1 || selected.Rows[0]["speed"] != "fast" {
			t.Fatalf("native speed breakdown %s: %+v %v", by, selected, err)
		}
	}
	v3Schema, err := sqliteQuerySchema("model_requests")
	if err != nil {
		t.Fatal(err)
	}
	metric := metricRequest(qt.AggSpec{ID: "speeds", Op: "count", GroupBy: []string{"speed", "speed_source"}})
	resultBySpeed, err := server.executeQueryFixture(t.Context(), "model_requests", nil, v3Schema, nil, &metric)
	if err != nil {
		t.Fatal(err)
	}
	buckets := resultBySpeed.Metrics.Metrics[0].Buckets
	if len(buckets) != 3 {
		t.Fatalf("speed and classification source must group independently: %+v", buckets)
	}
	total := int64(0)
	for _, bucket := range buckets {
		total += integer(bucket.Value)
	}
	if total != 12 {
		t.Fatalf("native speed grouping lost requests: %+v", buckets)
	}
	// Recommended time-of-day queries open the actual hourly request inputs.
	// The timestamp must be groupable in both the authored and Go schemas.
	var calendar qt.QueryRequestV3
	if err := json.Unmarshal([]byte(`{"version":3,"profile":"qt-sqlite-v1","rows":true,"source":{},"grouping":{"keys":[{"field":"requested_at","interval":"hour","timeZone":"UTC"}],"summaries":[{"id":"requests","op":"count"},{"id":"wait","op":"formula","expression":"MEDIAN([gap_seconds])"}],"representative":null},"result":{"select":["requested_at","requests","wait"],"limit":50}}`), &calendar); err != nil {
		t.Fatal(err)
	}
	calendar.Now = time.Now().UnixMilli()
	w = apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/model_requests/query-v3", calendar)
	var hourly qt.QueryResultV3
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &hourly) != nil {
		t.Fatal("hourly request evidence", w.Code, w.Body.String())
	}
	hours := map[time.Time]bool{}
	for _, record := range records {
		hours[record.At.Truncate(time.Hour)] = true
	}
	total = 0
	for _, row := range hourly.Rows {
		total += integer(row["requests"])
	}
	if len(hourly.Rows) != len(hours) || total != int64(len(records)) {
		t.Fatalf("hourly evidence lost request population: %+v", hourly.Rows)
	}
	validation, err := c.thinkingValidation(context.Background(), thinkingOptions{}, records)
	if err != nil {
		t.Fatal(err)
	}
	if validation.Pairs != 1 || *validation.MedianRatio != 1 || validation.CounterCount != 1 || validation.CounterOverlap != 1 {
		t.Fatal("timers must deduplicate and counters use latest", validation)
	}
	// A page sorted newest first reads only the newest stored requests
	// (newestBound), and all of them when those do not fill it. Either way
	// it has the rows and total of the whole source.
	if err := c.refreshRequestTimings(t.Context()); err != nil {
		t.Fatal(err)
	}
	page := func(limit int, where ...qt.WhereTerm) ([]string, int64) {
		t.Helper()
		q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true, Source: qt.WireQuery{Limit: 1},
			Result: qt.WireQuery{Select: []string{"id", "requested_at", "speed"}, Where: where, OrderBy: []qt.OrderBy{{Field: "requested_at", Dir: "desc"}, {Field: "id", Dir: "asc"}}, Limit: limit}}
		result, _, err := server.executeQueryV3(t.Context(), "model_requests", nil, v3Schema, q)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, row := range result.Rows {
			ids = append(ids, firstString(row["id"]))
		}
		return ids, result.Total
	}
	for _, c := range []struct {
		limit int
		where []qt.WhereTerm
	}{{2, nil}, {50, nil}, {50, []qt.WhereTerm{{Field: "speed", Op: "=", Value: "no such speed"}}}, {2, []qt.WhereTerm{{Field: "speed", Op: "=", Value: "fast"}}}, {2, []qt.WhereTerm{{Field: "speed", Op: "!=", Value: "fast"}}}} {
		override(t, &newestDepth, func(through int) int { return through })
		override(t, &maxNewestPage, 500)
		before := wholeSourceReads.Load()
		bounded, total := page(c.limit, c.where...)
		// The whole source is read only when it has rows the newest lack: a
		// short page that holds every match, or none, has nothing more to find.
		if again := wholeSourceReads.Load() - before; again != 0 && c.limit == 50 {
			t.Fatalf("newest %d of %v: read the whole source for a page that already held all %d rows", c.limit, c.where, total)
		}
		override(t, &maxNewestPage, 0)
		whole, all := page(c.limit, c.where...)
		if len(whole) == 0 && c.limit != 50 || total != all || !slices.Equal(bounded, whole) {
			t.Fatalf("newest %d of %v: bounded %v of %d, whole %v of %d", c.limit, c.where, bounded, total, whole, all)
		}
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
	if result.Adjusted != 240 || g.Adjusted != 240 || g.Stats != nil || g.Comparison.Days != 5 || *g.Comparison.Ratio != .5 || len(g.Bands) != 8 || g.Bands[3].Days != 5 {
		t.Fatal(result, g)
	}
	if len(g.Calendar) != 10 || g.Calendar[0].Day != "2026-10-01" || g.Calendar[0].Hour != 9 || *g.Calendar[1].Ratio != .5 {
		t.Fatal("daily cells in the named zone", g.Calendar)
	}
	o.Rows, o.Metric = "month", "ratio"
	monthly, err := aggregateThinking(context.Background(), records, o)
	if err != nil || len(monthly.Groups[0].Calendar) != 2 || *monthly.Groups[0].Calendar[1].Ratio != .5 || monthly.Groups[0].Calendar[1].Adjusted != 120 {
		t.Fatal("pool individual estimates before medians", monthly, err)
	}
	if stats := monthly.Groups[0].Stats; stats == nil || stats.N != 240 || *stats.Median != .75 || *monthly.Groups[0].Conversations != 3 {
		t.Fatal("the ratio metric describes the estimates", stats)
	}
	o.View = "time"
	if other, _ := aggregateThinking(context.Background(), records, o); len(other.Groups) != 0 {
		t.Fatal("only time of day reads the fitted cells", other)
	}
	o.View = "calendar"
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

func TestThinkingCachedPopulationParityAndFreshness(t *testing.T) {
	c, _ := testCatalog(t)
	seedThinking(t, c)
	ctx := t.Context()
	if _, err := c.DB.Exec(`UPDATE model_requests SET requested_at='2026-10-20T00:28:20.1236Z' WHERE id='request-9'`); err != nil {
		t.Fatal(err)
	}
	// Later commits, from any process, change stored conversations: the query
	// must derive those again until the next refresh.
	if err := c.refreshRequestTimings(ctx); err != nil {
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

func TestThinkingSpeedMigration(t *testing.T) {
	c, _ := testCatalog(t)
	seedThinking(t, c)
	if err := c.refreshRequestTimings(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-speed stored projection, including its covering index.
	if _, err := c.DB.Exec(`DROP INDEX model_request_timings_requested_idx;
 ALTER TABLE model_request_timings DROP COLUMN speed;
 UPDATE meta SET value='timings-v1' WHERE key='model_request_timings_version'`); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureRequestTimings(); err != nil {
		t.Fatal(err)
	}
	if err := c.refreshRequestTimings(t.Context()); err != nil {
		t.Fatal(err)
	}
	var speed string
	if err := c.DB.QueryRow("SELECT speed FROM model_request_timings WHERE id='request-1'").Scan(&speed); err != nil || speed != "fast" {
		t.Fatalf("migrated speed %q: %v", speed, err)
	}
}

// The page merges fitted estimates onto query-v3 groups by key, so Go keys a
// record's group as thinking-query.ts keys the same row
// (testdata/thinking-group-keys.json, also read by thinking-query.test.mjs).
func TestThinkingGroupKeys(t *testing.T) {
	var fixture struct {
		Cases []struct {
			GroupBy, Key, Label string
			Row                 map[string]any
		}
	}
	data, err := os.ReadFile("../../testdata/thinking-group-keys.json")
	if err != nil || json.Unmarshal(data, &fixture) != nil || len(fixture.Cases) == 0 {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		text := func(field string) string { value, _ := c.Row[field].(string); return value }
		depth, _ := c.Row["agent_depth"].(float64)
		r := thinkingRecord{Model: text("model"), Effort: text("effort"), Speed: text("speed"), Provider: text("provider"), Harness: text("harness"),
			Project: text("project_name"), Machine: text("machine"), Preceding: text("preceding_tool"), Depth: int(depth)}
		thinkingRelabel(&r)
		if key, label := thinkingGroupKey(r, c.GroupBy); key != c.Key || label != c.Label {
			t.Errorf("%s %v: key %q label %q, want %q %q", c.GroupBy, c.Row, key, label, c.Key, c.Label)
		}
	}
}

// The Thinking dashboards read model_requests through query-v3
// (thinking-query.ts). The service answers each shape they send with the
// statistics the records give.
func TestThinkingQueryV3Shapes(t *testing.T) {
	c, config := testCatalog(t)
	seedThinking(t, c)
	records, err := c.thinkingRecords(t.Context(), thinkingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	type bucket struct {
		Keys         []any
		Value        any
		Distribution *struct {
			Summary *struct {
				N      int
				Values []float64
			}
		}
	}
	ask := func(params string, metrics ...map[string]any) map[string][]bucket {
		t.Helper()
		body := map[string]any{"version": 3, "profile": "qt-sqlite-v1", "now": time.Now().UnixMilli(), "rows": false, "source": map[string]any{"where": []any{}, "limit": 1},
			"result": map[string]any{"where": []any{}, "limit": 1}, "metrics": metrics}
		w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/model_requests/query-v3"+params, body)
		var result struct {
			Metrics []struct {
				ID, Error string
				Buckets   []bucket
			}
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Metrics == nil {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		out := map[string][]bucket{}
		for _, m := range result.Metrics {
			if m.Error != "" {
				t.Fatalf("%s: %s", m.ID, m.Error)
			}
			out[m.ID] = m.Buckets
		}
		return out
	}
	timed := []map[string]any{{"field": "gap_seconds", "op": "is_not_null", "value": ""}}
	quantiles := func(id, input string, groupBy []string, probabilities []float64, where []map[string]any) map[string]any {
		return map[string]any{"id": id, "op": "count", "groupBy": groupBy, "where": where, "distribution": map[string]any{"kind": "quantiles", "input": input, "probabilities": probabilities}}
	}
	gaps, reasoning := map[string][]float64{}, map[string][]float64{}
	var first, last time.Time
	for _, r := range records {
		if r.Gap != nil {
			gaps[r.Model] = append(gaps[r.Model], *r.Gap)
		}
		if r.Reasoning > 0 {
			reasoning[r.Model] = append(reasoning[r.Model], r.Reasoning)
		}
		if first.IsZero() || r.At.Before(first) {
			first = r.At
		}
		if r.At.After(last) {
			last = r.At
		}
	}
	got := ask("", quantiles("gaps", "[gap_seconds]", []string{"model"}, []float64{.1, .25, .5, .75, .9, .95}, nil),
		quantiles("reasoning", "[reasoning_output_tokens]", []string{"model"}, []float64{.1, .25, .5, .75, .9, .95}, []map[string]any{{"field": "reasoning_output_tokens", "op": ">", "value": "0"}}),
		map[string]any{"id": "first", "op": "min", "field": "requested_at", "groupBy": []string{}}, map[string]any{"id": "last", "op": "max", "field": "requested_at", "groupBy": []string{}})
	for id, values := range map[string]map[string][]float64{"gaps": gaps, "reasoning": reasoning} {
		for _, b := range got[id] {
			want := thinkingSummarize(slices.Clone(values[firstString(b.Keys[0])]))
			q := b.Distribution
			if want.N == 0 {
				if q != nil && q.Summary != nil {
					t.Fatalf("%s %v: %+v without values", id, b.Keys, q.Summary)
				}
				continue
			}
			if q == nil || q.Summary == nil || int(q.Summary.N) != want.N || q.Summary.Values[2] != *want.Median || math.Abs(q.Summary.Values[5]-*want.P95) > 1e-9 || math.Abs(q.Summary.Values[0]-*want.Low) > 1e-9 {
				t.Fatalf("%s %v: %+v, want %+v", id, b.Keys, b.Distribution, want)
			}
		}
	}
	for id, want := range map[string]time.Time{"first": first, "last": last} {
		at, err := time.Parse(time.RFC3339Nano, firstString(got[id][0].Value))
		if err != nil || !at.Equal(want) {
			t.Fatalf("%s request %v (%T), want %v", id, got[id][0].Value, got[id][0].Value, want)
		}
	}
	// Six-hour buckets in a named zone: percentiles and wait bands per period.
	zone, _ := time.LoadLocation("America/Denver")
	dateGrouping := map[string]any{"field": "requested_at", "interval": "hour", "every": 6, "timeZone": "America/Denver", "includeEmpty": false, "range": "observed"}
	got = ask("", map[string]any{"id": "median", "op": "count", "expression": "PERCENTILE([gap_seconds],0.5)", "groupBy": []string{"requested_at", "model"}, "dateGrouping": dateGrouping},
		map[string]any{"id": "bands", "op": "count", "groupBy": []string{"requested_at", "model", "gap_bin"}, "where": timed, "dateGrouping": dateGrouping})
	period := func(at time.Time) time.Time {
		at = at.In(zone)
		return time.Date(at.Year(), at.Month(), at.Day(), at.Hour()/6*6, 0, 0, 0, zone)
	}
	for _, b := range got["median"] {
		start, _ := time.Parse(time.RFC3339Nano, firstString(b.Keys[0]))
		values := []float64{}
		for _, r := range records {
			if r.Gap != nil && r.Model == firstString(b.Keys[1]) && period(r.At).Equal(start) {
				values = append(values, *r.Gap)
			}
		}
		if len(values) == 0 && b.Value != nil || len(values) > 0 && b.Value != thinkingMedian(values) {
			t.Fatalf("period %v: median %v of %v", b.Keys, b.Value, values)
		}
	}
	banded := 0
	for _, b := range got["bands"] {
		banded += int(integer(b.Value))
	}
	if want := len(slices.Concat(slices.Collect(maps.Values(gaps))...)); banded != want {
		t.Fatalf("bands hold %d of %d waits", banded, want)
	}
	// Time-of-day cells in the viewer's zone.
	kathmandu, _ := time.LoadLocation("Asia/Kathmandu")
	got = ask("?timezone=Asia%2FKathmandu", quantiles("cells", "[gap_seconds]", []string{"model", "day", "hour_of_day"}, []float64{.5}, nil))
	cells := 0
	for _, b := range got["cells"] {
		q := b.Distribution
		n := 0
		for _, r := range records {
			at := r.At.In(kathmandu)
			if r.Gap != nil && r.Model == firstString(b.Keys[0]) && at.Format("2006-01-02") == firstString(b.Keys[1]) && at.Hour() == int(floatOr(b.Keys[2])) {
				n++
			}
		}
		if q != nil && q.Summary != nil {
			cells += int(q.Summary.N)
		}
		if n > 0 && (q == nil || q.Summary == nil || int(q.Summary.N) != n) {
			t.Fatalf("cell %v: %+v, want %d waits", b.Keys, b.Distribution, n)
		}
	}
	if want := len(slices.Concat(slices.Collect(maps.Values(gaps))...)); cells != want {
		t.Fatalf("cells hold %d of %d waits", cells, want)
	}
}
