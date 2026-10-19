package archive

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
)

func parallelismEvents(values ...any) []parallelismEvent {
	events := []parallelismEvent{}
	for i := 0; i < len(values); i += 3 {
		at, _ := time.Parse(time.RFC3339, values[i+2].(string))
		events = append(events, parallelismEvent{role: values[i].(string), kind: values[i+1].(string), at: at.UnixMilli()})
	}
	return events
}

func spanTotal(spans parallelismSpans) int64 {
	total := int64(0)
	for _, span := range spans {
		total += span[1] - span[0]
	}
	return total
}

// The prototype's TimingTests (tools/test_concurrency_prototype.py).
func TestParallelismTimeline(t *testing.T) {
	activity := parallelismTimeline(parallelismEvents("user", "message", "2026-10-08T10:00:00Z", "assistant", "tool_call", "2026-10-08T10:00:10Z",
		"tool", "tool_result", "2026-10-08T10:01:00Z", "assistant", "message", "2026-10-08T10:01:15Z"))
	if spanTotal(activity.ModelSpans) != 25_000 || spanTotal(activity.Turns) != 75_000 || activity.Unexplained != 0 {
		t.Fatalf("tool waits: %+v", activity)
	}
	if activity := parallelismTimeline(parallelismEvents("user", "message", "2026-10-08T10:00:00Z")); len(activity.Turns)+len(activity.ModelSpans) != 0 || activity.Unexplained != 0 {
		t.Fatalf("unanswered prompt: %+v", activity)
	}
	activity = parallelismTimeline(parallelismEvents("user", "message", "2026-10-08T10:00:00Z", "assistant", "message", "2026-10-08T10:00:10Z",
		"assistant", "message", "2026-10-08T14:00:00Z", "assistant", "tool_call", "2026-10-08T14:00:10Z"))
	if spanTotal(activity.ModelSpans) != 10_000 || spanTotal(activity.Turns) != 20_000 {
		t.Fatalf("long silence: %+v", activity)
	}
	activity = parallelismTimeline(parallelismEvents("user", "message", "2026-10-08T10:00:00Z", "assistant", "message", "2026-10-08T10:15:00Z"))
	if len(activity.ModelSpans) != 0 || len(activity.Turns) != 1 || activity.Unexplained != 1 {
		t.Fatalf("queueing: %+v", activity)
	}
}

// seedParallelism is a week in America/Denver around the 2026 fall-back night
// (Nov 1, 01:00-02:00 happens twice). See the expectations in the tests.
func seedParallelism(t *testing.T, c *Catalog) {
	t.Helper()
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := c.DB.Exec(statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	exec(`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo-a','alpha','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z'),
		('repo-b','beta','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`)
	for _, w := range [][]any{
		{"ws-a1", "claude", "repo-a", "Alpha one", "/code/alpha-1"}, {"ws-a2", "codex", "repo-a", "Alpha two", "/code/alpha-2"},
		{"ws-a3", "claude", "repo-a", "Alpha again", "/code/alpha-1/"}, {"ws-cond", "conductor", "repo-a", "Alpha (Conductor)", "/code/alpha-1"},
		{"ws-b1", "claude", "repo-b", "Beta one", "/code/beta-1"}, {"ws-b2", "claude", "repo-b", "Beta launched", "/code/beta-2"},
		{"ws-n1", "codex", nil, "Scratch", "/code/scratch"}, {"ws-old", "claude", "repo-a", "Old", "/code/old"},
	} {
		exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,location,activity_at,indexed_at) VALUES(?,?,'local',?,?,?,?,'2026-11-01T00:00:00.000Z','2026-11-01T00:00:00.000Z')`,
			w[0], w[1], w[0], w[2], w[3], w[4])
	}
	for _, v := range [][]any{
		{"cv-a1", "ws-a1", "claude", 0, nil}, {"cv-a2", "ws-a2", "codex", 0, nil}, {"cv-a3", "ws-a3", "claude", 0, nil},
		{"cv-mirror", "ws-cond", "claude", 0, nil}, {"cv-sub", "ws-a1", "claude", 1, "cv-a1"},
		{"cv-b1", "ws-b1", "claude", 0, nil}, {"cv-launch", "ws-b2", "claude", 0, nil}, {"cv-n1", "ws-n1", "codex", 0, nil},
	} {
		exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,agent_depth,parent_id,model,started_at,ended_at)
			VALUES(?,?,?,'local',?,?,?,'model-x','2026-10-28T00:00:00.000Z','2026-11-03T18:00:00.000Z')`, v[0], v[1], v[2], v[0], v[3], v[4])
	}
	exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at,ended_at) VALUES('cv-old','ws-old','claude','local','cv-old','2024-01-01T00:00:00.000Z','2024-01-01T01:00:00.000Z')`)
	exec(`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('cv-a1','cv-mirror','same_session',1,'{}')`)
	exec(`INSERT INTO conversation_launches(child_id,parent_id,tool_call_id,program) VALUES('cv-launch','cv-b1','call-1','claude')`)
	turns := map[string][]string{
		// Across the repeated hour: 00:30 MDT to 02:30 MST, three real hours.
		"cv-a1": {"user message 2026-11-01T06:30:00Z", "assistant message 2026-11-01T07:00:00Z", "assistant tool_call 2026-11-01T07:30:00Z",
			"tool tool_result 2026-11-01T07:50:00Z", "assistant message 2026-11-01T08:20:00Z", "assistant tool_call 2026-11-01T08:50:00Z",
			"tool tool_result 2026-11-01T09:10:00Z", "assistant message 2026-11-01T09:30:00Z",
			// Two turns 20 minutes apart: one thread at gaps of 30 and 60.
			"user message 2026-10-30T15:00:00Z", "assistant message 2026-10-30T15:10:00Z", "user message 2026-10-30T15:30:00Z", "assistant message 2026-10-30T15:50:00Z"},
		"cv-a2":     {"user message 2026-11-01T07:00:00Z", "assistant message 2026-11-01T08:00:00Z"},
		"cv-a3":     {"user message 2026-10-30T15:20:00Z", "assistant message 2026-10-30T15:40:00Z"},
		"cv-mirror": {"user message 2026-11-01T06:30:00Z", "assistant message 2026-11-01T09:30:00Z"},
		"cv-sub":    {"user message 2026-11-02T18:00:00Z", "assistant message 2026-11-02T19:00:00Z"},
		"cv-b1":     {"user message 2026-11-01T07:15:00Z", "assistant message 2026-11-01T07:45:00Z", "assistant message 2026-11-01T08:30:00Z"},
		"cv-launch": {"user message 2026-11-01T07:20:00Z", "assistant message 2026-11-01T07:40:00Z"},
		// Across local midnight, then a separate morning turn.
		"cv-n1":  {"user message 2026-10-31T05:30:00Z", "assistant message 2026-10-31T06:30:00Z", "user message 2026-11-03T16:00:00Z", "assistant message 2026-11-03T16:30:00Z"},
		"cv-old": {"user message 2024-01-01T00:00:00Z", "assistant message 2024-01-01T00:30:00Z"},
	}
	for conversation, messages := range turns {
		for i, text := range messages {
			parts := strings.Fields(text)
			at, _ := time.Parse(time.RFC3339, parts[2])
			exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,source_order,created_at) VALUES(?,?,?,?,?,'','h',?,?)`,
				fmt.Sprint(conversation, "-", i), conversation, fmt.Sprint(i), parts[0], parts[1], i, formatTime(at))
		}
	}
	// The 08:30 response belongs to an embedded sub-agent, not cv-b1's own turn.
	exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('emb','ws-b1','cv-b1','agent-1','subagent','claude')`)
	exec(`INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('emb','cv-b1-2',2)`)
}

func parallelismTestCatalog(t *testing.T) (*Catalog, Config) {
	t.Helper()
	zone, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skip(err)
	}
	previous := time.Local
	time.Local = zone
	t.Cleanup(func() { time.Local = previous })
	c, config := testCatalog(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, zone)
	c.now = func() time.Time { return now }
	seedParallelism(t, c)
	return c, config
}

// referenceUnits and referenceLevels port concurrencySpans(threads, "user",
// gap, counting) and the draft cards' levels() (web/src).
func referenceUnits(threads []concurrencyThread, gap int64, counting string) (map[string]parallelismSpans, map[string]string) {
	units, repositories := map[string]parallelismSpans{}, map[string]string{}
	for _, thread := range threads {
		if thread.AgentDepth > 0 {
			continue
		}
		unit := thread.WorkspaceID
		if counting == "agent" {
			unit = defaultString(thread.RootConversationID, thread.ID)
		}
		units[unit] = append(units[unit], mergeSpans(thread.Turns, gap*60_000)...)
		repositories[unit] = defaultString(thread.Repository, "No repository")
	}
	for unit, spans := range units {
		units[unit] = mergeSpans(spans, 0)
	}
	return units, repositories
}

func referenceLevels(units map[string]parallelismSpans, from, to int64) []float64 {
	type event struct{ at, delta int64 }
	events := []event{}
	for _, spans := range units {
		for _, span := range clipSpans(spans, from, to) {
			events = append(events, event{span[0], 1}, event{span[1], -1})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].at < events[j].at || events[i].at == events[j].at && events[i].delta < events[j].delta
	})
	result := make([]float64, len(units)+1)
	active, previous := int64(0), from
	for _, e := range events {
		if active > 0 && e.at > previous {
			result[active] += float64(e.at - previous)
		}
		active += e.delta
		previous = e.at
	}
	return result
}

func closeTo(a, b float64) bool { return math.Abs(a-b) <= 1e-6*max(1, math.Abs(a), math.Abs(b)) }

func dayBounds(day string) (int64, int64) {
	start, _ := time.ParseInLocation("2006-01-02", day, time.Local)
	return start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli()
}

// The four Parallelism cards as plain aggregations of the dataset's rows,
// checked against the draft cards' interval computations.
func TestParallelismCardIdentities(t *testing.T) {
	c, _ := parallelismTestCatalog(t)
	data, err := c.parallelismData(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	days := data.Days[len(data.Days)-7:]
	for _, counting := range []string{"workspace", "agent"} {
		for _, gap := range []int64{0, 15, 30, 60} {
			t.Run(fmt.Sprint(counting, gap), func(t *testing.T) {
				rows := parallelismRows(data, parallelismSettings{gap: gap, counting: counting})
				units, repositories := referenceUnits(data.Threads, gap, counting)
				sum := func(field string, keep func(map[string]any) bool) float64 {
					total := 0.0
					for _, row := range rows {
						if keep(row) {
							value, _ := number(row[field])
							total += value
						}
					}
					return total
				}
				inDays := func(from, to string) func(map[string]any) bool {
					return func(row map[string]any) bool { return row["day"].(string) >= from && row["day"].(string) <= to }
				}
				// (1) Hours by day and level band.
				for _, day := range days {
					from, to := dayBounds(day)
					levels := referenceLevels(units, from, to)
					want := map[string]float64{"1": levels[1], "2": 0, "3+": 0}
					if len(levels) > 2 {
						want["2"] = levels[2]
					}
					for level := 3; level < len(levels); level++ {
						want["3+"] += levels[level]
					}
					for band, value := range want {
						if got := sum("elapsed_ms", func(row map[string]any) bool { return row["day"] == day && row["level_band"] == band }); !closeTo(got, value) {
							t.Fatalf("%s band %s: %v, want %v", day, band, got, value)
						}
					}
					parallel := want["2"] + want["3+"]
					if got := sum("parallel_elapsed_ms", inDays(day, day)); !closeTo(got, parallel) {
						t.Fatalf("%s parallel: %v, want %v", day, got, parallel)
					}
				}
				// (2) Time-weighted agents at once over trailing seven days.
				for end := 6; end < len(data.Days); end += 97 {
					from, _ := dayBounds(data.Days[end-6])
					_, to := dayBounds(data.Days[end])
					levels := referenceLevels(units, from, to)
					area, active := 0.0, 0.0
					for level, value := range levels {
						area, active = area+value*float64(level), active+value
					}
					keep := inDays(data.Days[end-6], data.Days[end])
					if !closeTo(sum("unit_ms", keep), area) || !closeTo(sum("elapsed_ms", keep), active) {
						t.Fatalf("window to %s: %v/%v, want %v/%v", data.Days[end], sum("unit_ms", keep), sum("elapsed_ms", keep), area, active)
					}
				}
				for _, end := range days {
					from, _ := dayBounds(data.Days[slices.Index(data.Days, end)-6])
					_, to := dayBounds(end)
					levels := referenceLevels(units, from, to)
					area := 0.0
					for level, value := range levels {
						area += value * float64(level)
					}
					if got := sum("unit_ms", inDays(data.Days[slices.Index(data.Days, end)-6], end)); !closeTo(got, area) {
						t.Fatalf("window to %s: %v, want %v", end, got, area)
					}
				}
				// (3) Concurrency by local hour of day: every real hour of each day,
				// so a daylight-saving day contributes 23 or 25 hours.
				area := map[string]float64{}
				for _, day := range days {
					from, to := dayBounds(day)
					for start := from; start < to; start += 3_600_000 {
						levels := referenceLevels(units, start, min(start+3_600_000, to))
						for level, value := range levels {
							area[time.UnixMilli(start).In(time.Local).Format("15")] += value * float64(level)
						}
					}
				}
				for hour := range 24 {
					key := fmt.Sprintf("%02d", hour)
					if got := sum("unit_ms", func(row map[string]any) bool { return row["hour_of_day"] == key && inDays(days[0], days[6])(row) }); !closeTo(got, area[key]) {
						t.Fatalf("hour %s: %v, want %v", key, got, area[key])
					}
				}
				// (4) By repository: agent time, elapsed with any active, units.
				from, _ := dayBounds(days[0])
				_, to := dayBounds(days[6])
				for _, repository := range []string{"alpha", "beta", "No repository"} {
					scoped := map[string]parallelismSpans{}
					for unit, spans := range units {
						if repositories[unit] == repository && len(clipSpans(spans, from, to)) > 0 {
							scoped[unit] = spans
						}
					}
					agentTime, elapsed := 0.0, 0.0
					for _, spans := range scoped {
						agentTime += float64(spanTotal(clipSpans(spans, from, to)))
					}
					for _, value := range referenceLevels(scoped, from, to) {
						elapsed += value
					}
					keep := func(row map[string]any) bool {
						return defaultString(firstString(row["repository_name"]), "No repository") == repository && inDays(days[0], days[6])(row)
					}
					distinct := map[any]bool{}
					for _, row := range rows {
						if keep(row) {
							distinct[row["unit_id"]] = true
						}
					}
					if !closeTo(sum("unit_ms", keep), agentTime) || !closeTo(sum("repo_elapsed_ms", keep), elapsed) || len(distinct) != len(scoped) {
						t.Fatalf("%s: %v %v %d, want %v %v %d", repository, sum("unit_ms", keep), sum("repo_elapsed_ms", keep), len(distinct), agentTime, elapsed, len(scoped))
					}
				}
			})
		}
	}
}

func TestParallelismThreadsAndRows(t *testing.T) {
	c, _ := parallelismTestCatalog(t)
	data, err := c.parallelismData(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	threads := map[string]concurrencyThread{}
	for _, thread := range data.Threads {
		threads[thread.ID] = thread
	}
	user := []string{}
	for id, thread := range threads {
		if thread.AgentDepth == 0 {
			user = append(user, id)
		}
	}
	slices.Sort(user)
	if !reflect.DeepEqual(user, []string{"cv-a1", "cv-a2", "cv-a3", "cv-b1", "cv-n1"}) || data.Coverage.MirrorsRemoved != 1 || data.Coverage.UnexplainedWait == 0 {
		t.Fatalf("threads %v, coverage %+v", user, data.Coverage)
	}
	at := func(text string) int64 { value, _ := time.Parse(time.RFC3339, text); return value.UnixMilli() }
	if b1 := threads["cv-b1"]; !reflect.DeepEqual(b1.Turns, parallelismSpans{{at("2026-11-01T07:15:00Z"), at("2026-11-01T07:45:00Z")}}) {
		t.Fatalf("embedded sub-agent messages extended the turn: %v", b1.Turns)
	}
	// A launched run is delegated, inside its root's workspace.
	if launch := threads["cv-launch"]; launch.AgentDepth == 0 || launch.RootConversationID != "cv-b1" || launch.WorkspaceID != "/code/beta-1" {
		t.Fatalf("launched run: %+v", launch)
	}
	if a3 := threads["cv-a3"]; a3.WorkspaceID != "/code/alpha-1" || a3.WorkspaceName != "alpha-1" || a3.Repository != "alpha" || a3.WorkID != "ws-a3" {
		t.Fatalf("checkout identity: %+v", a3)
	}
	byBand := func(rows []map[string]any, day string) map[string]float64 {
		result := map[string]float64{}
		for _, row := range rows {
			if row["day"] == day {
				result[row["level_band"].(string)] += row["elapsed_ms"].(float64) / 60_000
			}
		}
		return result
	}
	rows := parallelismRows(data, parallelismSettings{gap: 60, counting: "workspace"})
	if got := byBand(rows, "2026-11-01"); !reflect.DeepEqual(got, map[string]float64{"1": 120, "2": 30, "3+": 30}) {
		t.Fatalf("fall-back day minutes by band: %v", got)
	}
	// The repeated 01:00 hour is two rows, one per offset.
	offsets := map[string]bool{}
	for _, row := range rows {
		if row["day"] == "2026-11-01" && row["hour_of_day"] == "01" {
			offsets[row["started_at"].(string)] = true
		}
	}
	if !offsets["2026-11-01T01:00:00-06:00"] || !offsets["2026-11-01T01:00:00-07:00"] || len(offsets) != 2 {
		t.Fatalf("repeated hour: %v", offsets)
	}
	// Midnight splits a thread between days.
	if got := byBand(rows, "2026-10-30")["1"]; got != 30+50 {
		t.Fatalf("Oct 30 minutes: %v", got)
	}
	// Gaps: one checkout's two conversations; a 20-minute gap joins at 30.
	for _, check := range []struct {
		settings parallelismSettings
		want     map[string]float64
	}{
		{parallelismSettings{15, "workspace"}, map[string]float64{"1": 30 + 40}},
		{parallelismSettings{30, "agent"}, map[string]float64{"1": 30 + 30, "2": 20}},
	} {
		if got := byBand(parallelismRows(data, check.settings), "2026-10-30"); !reflect.DeepEqual(got, check.want) {
			t.Fatalf("%+v: %v", check.settings, got)
		}
	}
	for _, row := range parallelismRows(data, parallelismSettings{60, "agent"}) {
		if row["unit_id"] == "cv-a1" && (row["conversation_id"] != "cv-a1" || row["unit_label"] != "Alpha one" || row["provider"] != "claude" || row["model"] != "model-x") {
			t.Fatalf("agent unit: %v", row)
		}
	}
}

func TestParallelismSettingsAndHTTP(t *testing.T) {
	c, config := parallelismTestCatalog(t)
	server := testServer(t, config, c)
	rows, err := c.parallelismQueryRows(t.Context(), nil)
	if err != nil || len(rows) == 0 || rows[0]["counting"] != "workspace" || rows[0]["gap_minutes"] != int64(60) {
		t.Fatal(len(rows), err)
	}
	// Preference writes don't advance the catalog generation; they apply at once.
	if err := c.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{parallelismSettingsKey: map[string]any{"mode": "user", "gap": 15, "counting": "agent"}}}); err != nil {
		t.Fatal(err)
	}
	if rows, err = c.parallelismQueryRows(t.Context(), nil); err != nil || rows[0]["counting"] != "agent" || rows[0]["gap_minutes"] != int64(15) {
		t.Fatal(rows[0], err)
	}
	if err := c.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{parallelismSettingsKey: map[string]any{"gap": 45, "counting": "other"}}}); err != nil {
		t.Fatal(err)
	}
	if settings := c.parallelismSettings(t.Context()); settings != (parallelismSettings{60, "workspace"}) {
		t.Fatal(settings)
	}
	w := queryExplorationCall(server, config.APIToken, http.MethodPost, "/api/query/parallelism/rows-v2", `{"version":2,"profile":"qt-sqlite-v1","select":["id","unit_label","elapsed_hours"],"limit":1}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unit_label"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if rows, err = c.parallelismQueryRows(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	id := rows[len(rows)-1]["id"].(string)
	if w := queryExplorationCall(server, config.APIToken, http.MethodGet, "/api/query/parallelism/item?id="+url.QueryEscape(id), ""); w.Code != 200 || strings.Contains(w.Body.String(), `"row":null`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = queryExplorationCall(server, config.APIToken, http.MethodPost, "/api/query/parallelism/metrics",
		`{"version":2,"profile":"qt-sqlite-v1","limit":50,"metrics":[{"id":"wall","expression":"SUM([elapsed_hours])","groupBy":["level_band"]}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"3+"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// A started_at window pre-selects staged rows without changing results:
	// the fall-back day's 25 hours have 3 active (180 minutes).
	for _, where := range []string{
		`[{"field":"started_at","op":">=","value":"2026-11-01T00:00:00-06:00"},{"field":"started_at","op":"<","value":"2026-11-02T00:00:00-07:00"}]`,
		`[{"field":"started_at","op":">","value":"2026-10-31T23:59:59-06:00"},{"field":"day","op":"=","value":"2026-11-01"}]`,
		`[{"field":"day","op":"=","value":"2026-11-01"},{"any":[{"field":"started_at","op":">=","value":"2026-11-01T01:00:00-07:00"},{"field":"started_at","op":"<","value":"2026-11-01T01:00:00-07:00"}]}]`,
	} {
		w = queryExplorationCall(server, config.APIToken, http.MethodPost, "/api/query/parallelism/metrics",
			`{"version":2,"profile":"qt-sqlite-v1","limit":50,"where":`+where+`,"metrics":[{"id":"wall","expression":"SUM([elapsed_ms])/60000"}]}`)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"value":180`) {
			t.Fatal(where, w.Code, w.Body.String())
		}
	}
	if from, to := parallelismWindow([]querytable.WhereTerm{{Field: "started_at", Op: ">=", Value: "-7d"}}); from != math.MinInt64 || to != math.MaxInt64 {
		t.Fatal("an unresolved relative bound narrowed the window")
	}
}

func TestParallelismIncremental(t *testing.T) {
	c, _ := parallelismTestCatalog(t)
	_, _, cutoff := parallelismPeriod(c.clock())
	if _, err := c.parallelismActivities(t.Context(), cutoff); err != nil {
		t.Fatal(err)
	}
	if dirty := countRows(t, c, "SELECT COUNT(*) FROM parallelism_dirty"); dirty != 0 {
		t.Fatalf("dirty conversations remain: %d", dirty)
	}
	if stored := countRows(t, c, "SELECT COUNT(*) FROM parallelism_activity"); stored != 8 {
		t.Fatalf("stored %d conversations; want all but the one outside the period", stored)
	}
	// A clean conversation's stored spans are read, never re-derived.
	marker := parallelismSpans{{1793548800000, 1793552400000}, {1793556000000, 1793556000001}}
	if _, err := c.DB.Exec("UPDATE parallelism_activity SET turns=? WHERE conversation_id='cv-n1'", encodeSpans(marker)); err != nil {
		t.Fatal(err)
	}
	// Damaged bytes are derived again rather than failing the page.
	if _, err := c.DB.Exec("UPDATE parallelism_activity SET model_spans=x'ff' WHERE conversation_id='cv-a2'"); err != nil {
		t.Fatal(err)
	}
	activities, err := c.parallelismActivities(t.Context(), cutoff)
	if err != nil || !reflect.DeepEqual(activities["cv-n1"].Turns, marker) || len(activities["cv-a2"].Turns) != 1 {
		t.Fatal(activities["cv-n1"], activities["cv-a2"], err)
	}
	// A new message marks only its conversation, which is derived again.
	if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('cv-n1-x','cv-n1','x','assistant','message','','h','2026-11-03T16:50:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if dirty := countRows(t, c, "SELECT COUNT(*) FROM parallelism_dirty WHERE conversation_id='cv-n1'"); dirty != 1 || countRows(t, c, "SELECT COUNT(*) FROM parallelism_dirty") != 1 {
		t.Fatal("message insert did not mark only its conversation")
	}
	// A page sees it at once, through a new catalog generation.
	data, err := c.parallelismData(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, thread := range data.Threads {
		if thread.ID == "cv-n1" && thread.Turns[len(thread.Turns)-1][1] != time.Date(2026, 11, 3, 16, 50, 0, 0, time.UTC).UnixMilli() {
			t.Fatalf("new message not timed: %v", thread.Turns)
		}
	}
	// Ownership and timestamp changes mark their conversations too.
	for _, statement := range []string{
		"DELETE FROM agent_session_messages WHERE message_id='cv-b1-2'",
		"UPDATE messages SET created_at='2026-11-01T07:50:00.000Z' WHERE id='cv-a2-1'",
		"UPDATE agent_sessions SET native_id='main' WHERE id='emb'",
	} {
		if _, err := c.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if dirty := countRows(t, c, "SELECT COUNT(*) FROM parallelism_dirty WHERE conversation_id IN ('cv-b1','cv-a2')"); dirty != 2 {
		t.Fatalf("dirty: %d", dirty)
	}
	if count, err := c.refreshParallelism(t.Context(), 100, true); err != nil || count < 2 {
		t.Fatal(count, err)
	}
	// Stored spans equal a full derivation.
	activities, err = c.parallelismActivities(t.Context(), cutoff)
	if err != nil {
		t.Fatal(err)
	}
	for id, stored := range activities {
		fresh, err := readParallelismActivity(t.Context(), c.DB, id)
		if err != nil || !reflect.DeepEqual(fresh, stored) {
			t.Fatalf("%s: stored %v, derived %v (%v)", id, stored, fresh, err)
		}
	}
	// New timing rules derive every conversation again.
	if _, err := c.DB.Exec("UPDATE meta SET value='0' WHERE key='parallelism_activity_version'"); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureParallelismActivity(); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM parallelism_activity") != 0 || countRows(t, c, "SELECT COUNT(*) FROM parallelism_dirty") != countRows(t, c, "SELECT COUNT(*) FROM conversations") ||
		countRows(t, c, "SELECT COUNT(*) FROM meta WHERE key='parallelism_activity_version' AND value=?", parallelismActivityVersion) != 1 {
		t.Fatal("version change did not mark every conversation")
	}
	// Maintenance leaves the work to a page that is already doing it.
	c.parallelismMu.Lock()
	count, err := c.refreshParallelism(t.Context(), 100, false)
	c.parallelismMu.Unlock()
	if count != 0 || err != nil {
		t.Fatal(count, err)
	}
}

func TestParallelismEmptyCatalog(t *testing.T) {
	c, _ := testCatalog(t)
	rows, err := c.parallelismQueryRows(context.Background(), nil)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}
