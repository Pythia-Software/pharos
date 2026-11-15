package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// seedAgentEventFacts is a main conversation with two turns, one a person
// prompted and one an automation did, and a subagent its delegating agent
// prompted. Four agent messages were read; one has no time of its own.
func seedAgentEventFacts(t *testing.T, catalog *Catalog) {
	t.Helper()
	execTx(t, catalog.DB,
		`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo','alpha','2026-01-01','2026-01-01')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at,repository_id) VALUES('w','claude','local','w','Work','2026-10-21T00:00:00.000Z','repo')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,model,effort,harness,started_at) VALUES('c','w','claude','local','c','opus','high','claude-code','2026-10-21T14:00:00.000Z')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,model,parent_id,started_at) VALUES('s','w','claude','local','s','sonnet','c','2026-10-21T15:30:00.000Z')`,
	)
	tx := seedTx(t, catalog.DB)
	for _, m := range []struct {
		id, conversation, role, kind string
		order                        int
		at                           any
	}{
		{"p1", "c", "user", "message", 0, "2026-10-21T15:00:00.000Z"},
		{"r1", "c", "assistant", "message", 1, "2026-10-21T15:01:00.000Z"},
		{"t1", "c", "assistant", "reasoning", 2, "2026-10-21T15:01:30.000Z"},
		{"p2", "c", "user", "message", 3, "2026-10-21T15:02:00.000Z"},
		{"r2", "c", "assistant", "message", 4, nil},
		{"d1", "s", "agent", "message", 0, "2026-10-21T15:30:00.000Z"},
		{"r3", "s", "assistant", "message", 1, "2026-10-21T15:31:00.000Z"},
	} {
		if _, err := tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash,source_order,selected) VALUES(?,?,?,?,?,'text',?,?,?,1)`,
			m.id, m.conversation, m.id, m.role, m.kind, m.at, m.id, m.order); err != nil {
			t.Fatal(err)
		}
	}
	for id, typed := range map[string]int{"p1": 5, "p2": 0} {
		if _, err := tx.Exec(`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,spans_json,typed_words,words) VALUES(?,'c','w','2026-10-21T15:00:00.000Z','2026-10-21',20,'[]',?,?)`, id, typed, typed+3); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []struct {
		id, conversation, source string
		at                       any
		events                   int
	}{{"r1", "c", "reply", "2026-10-21T15:01:00.000Z", 2}, {"t1", "c", "thinking", "2026-10-21T15:01:30.000Z", 0}, {"r2", "c", "reply", nil, 1}, {"r3", "s", "reply", "2026-10-21T15:31:00.000Z", 1}} {
		if _, err := tx.Exec(`INSERT INTO agent_event_messages(message_id,conversation_id,source,occurred_at,day,candidates,events,model,lexicon,imported_at) VALUES(?,?,?,?,'2026-10-21',1,?,'v','l','2026-10-22T00:00:00.000Z')`,
			m.id, m.conversation, m.source, m.at, m.events); err != nil {
			t.Fatal(err)
		}
	}
	for i, e := range []struct {
		message, conversation, family string
		probability                   float64
	}{{"r1", "c", "obstacle", 0.9}, {"r1", "c", "obstacle", 0.8}, {"r2", "c", "workaround", 0.7}, {"r2", "c", "obstacle", 0.4}, {"r3", "s", "stuck", 0.6}} {
		if _, err := tx.Exec(`INSERT INTO agent_events(message_id,conversation_id,source,family,probability,sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges,model)
 VALUES(?,?,'reply',?,?,?,?,'s','','','[]','[]','v')`, e.message, e.conversation, e.family, e.probability, i*10, i*10+5); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
}

// Each message read gets its turn, who prompted that turn, the typed length
// of a person's prompt, and its events by kind at the threshold. A refresh
// rebuilds only conversations whose inputs changed.
func TestAgentEventFactsPlaceMessagesInTurns(t *testing.T) {
	ctx := context.Background()
	catalog, _ := testCatalog(t)
	seedAgentEventFacts(t, catalog)
	if rebuilt, err := catalog.refreshAgentEventFacts(ctx); err != nil || rebuilt != 2 {
		t.Fatalf("refresh = %d, %v", rebuilt, err)
	}
	type fact struct {
		Turn, PromptedBy, TimeSource, At, Model, Effort string
		Words                                           sql.NullInt64
		Subagent                                        bool
	}
	got := map[string]fact{}
	rows, err := catalog.DB.Query(`SELECT message_id,turn_id,prompted_by,time_source,COALESCE(occurred_at,''),model,effort,prompt_words,subagent FROM agent_event_facts`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var f fact
		if err := rows.Scan(&id, &f.Turn, &f.PromptedBy, &f.TimeSource, &f.At, &f.Model, &f.Effort, &f.Words, &f.Subagent); err != nil {
			t.Fatal(err)
		}
		got[id] = f
	}
	rows.Close()
	words := func(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }
	want := map[string]fact{
		"r1": {"p1", "person", "message", "2026-10-21T15:01:00.000Z", "opus", "high", words(5), false},
		"t1": {"p1", "person", "message", "2026-10-21T15:01:30.000Z", "opus", "high", words(5), false},
		// No time of its own: its conversation's start stands in.
		"r2": {"p2", "automated", "conversation", "2026-10-21T14:00:00.000Z", "opus", "high", sql.NullInt64{}, false},
		"r3": {"d1", "agent", "message", "2026-10-21T15:31:00.000Z", "sonnet", "", sql.NullInt64{}, true},
	}
	if len(got) != len(want) {
		t.Fatalf("facts = %+v", got)
	}
	for id, f := range want {
		if got[id] != f {
			t.Errorf("%s = %+v, want %+v", id, got[id], f)
		}
	}
	kinds := map[string]int{}
	rows, err = catalog.DB.Query(`SELECT message_id||':'||family, events FROM agent_event_fact_kinds`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			t.Fatal(err)
		}
		kinds[key] = n
	}
	rows.Close()
	if len(kinds) != 3 || kinds["r1:obstacle"] != 2 || kinds["r2:workaround"] != 1 || kinds["r3:stuck"] != 1 {
		t.Fatalf("kinds = %v (the 0.4 obstacle is below the threshold)", kinds)
	}

	if rebuilt, err := catalog.refreshAgentEventFacts(ctx); err != nil || rebuilt != 0 {
		t.Fatalf("an unchanged refresh rebuilt %d, %v", rebuilt, err)
	}
	// A person's prompt classified again, and a message the pass dropped:
	// the message leaves at once, and only the main conversation is rebuilt.
	execTx(t, catalog.DB, `UPDATE message_authorship SET typed_words=4 WHERE message_id='p2'`, `DELETE FROM agent_event_messages WHERE message_id='t1'`)
	var left int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_event_facts WHERE message_id='t1'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("a dropped message stayed in the facts (%d, %v)", left, err)
	}
	if rebuilt, err := catalog.refreshAgentEventFacts(ctx); err != nil || rebuilt != 1 {
		t.Fatalf("refresh after a change = %d, %v", rebuilt, err)
	}
	var promptedBy string
	var typed int
	if err := catalog.DB.QueryRow(`SELECT prompted_by, prompt_words FROM agent_event_facts WHERE message_id='r2'`).Scan(&promptedBy, &typed); err != nil || promptedBy != "person" || typed != 4 {
		t.Fatalf("r2 after its prompt was classified again: %s %d %v", promptedBy, typed, err)
	}
	// A conversation the pass no longer holds leaves the facts.
	execTx(t, catalog.DB, `DELETE FROM agent_event_messages WHERE conversation_id='s'`)
	if _, err := catalog.refreshAgentEventFacts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM agent_event_facts WHERE conversation_id='s')+(SELECT COUNT(*) FROM agent_event_fact_conversations WHERE conversation_id='s')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("a gone conversation stayed (%d, %v)", left, err)
	}
}

// The rates dataset crosses every message read with every kind, so a kind
// filter or breakdown narrows the events and never the messages, turns or
// conversations a rate divides by.
func TestAgentEventRatesKeepTheirDenominators(t *testing.T) {
	catalog, config := testCatalog(t)
	seedAgentEventFacts(t, catalog)
	if _, err := catalog.refreshAgentEventFacts(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	metrics := func(where []qt.WhereTerm, groupBy []string, specs ...qt.AggSpec) []qt.MetricResult {
		t.Helper()
		for i := range specs {
			specs[i].Where, specs[i].GroupBy = where, groupBy
		}
		var result queryFixtureMetricsResult
		w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/agent_event_rates/metrics", metricRequest(specs...))
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, metric := range result.Metrics {
			if metric.Error != "" {
				t.Fatalf("%s: %s", metric.ID, metric.Error)
			}
		}
		return result.Metrics
	}
	value := func(metric qt.MetricResult, keys ...string) float64 {
		for _, bucket := range metric.Buckets {
			match := len(bucket.Keys) == len(keys)
			for i, key := range keys {
				match = match && firstString(bucket.Keys[i]) == key
			}
			if match {
				return floatOr(bucket.Value)
			}
		}
		return math.NaN()
	}
	// The dashboard's rates carry confidence intervals (Poisson for a rate,
	// Wilson for a share), computed from the same parts.
	rate := qt.AggSpec{ID: "rate", Expression: "SUM([events])/NULLIF(COUNT_DISTINCT([message_id])/1000,0)", Interval: &qt.MetricInterval{Kind: "rate"}}
	turns := qt.AggSpec{ID: "turns", Expression: "COUNT_DISTINCT(IF([events]>0,[turn_id],NULL))/NULLIF(COUNT_DISTINCT([turn_id]),0)", Interval: &qt.MetricInterval{Kind: "proportion"}}
	read := qt.AggSpec{ID: "read", Op: "count_distinct", Field: "message_id"}
	bounded := func(metric qt.MetricResult) {
		t.Helper()
		for _, bucket := range metric.Buckets {
			v := floatOr(bucket.Value)
			if bucket.IntervalError != "" || bucket.Lower == nil || bucket.Upper == nil || *bucket.Lower > v || v > *bucket.Upper || *bucket.Lower == *bucket.Upper {
				t.Fatalf("%s %v: value %v, interval %v–%v %q", metric.ID, bucket.Keys, v, bucket.Lower, bucket.Upper, bucket.IntervalError)
			}
		}
	}

	all := metrics(nil, nil, rate, turns, read)
	// Four events of reported kinds over four messages, in all three turns.
	if got := value(all[0]); got != 1000 || value(all[1]) != 1 || value(all[2]) != 4 {
		t.Fatalf("all kinds: rate %v, turns %v, read %v", got, value(all[1]), value(all[2]))
	}
	obstacles := metrics([]qt.WhereTerm{{Field: "family", Op: "=", Value: "obstacle"}}, nil, rate, turns, read)
	if value(obstacles[0]) != 500 || value(obstacles[1]) != 1.0/3 || value(obstacles[2]) != 4 {
		t.Fatalf("obstacles: rate %v, turns %v, read %v", value(obstacles[0]), value(obstacles[1]), value(obstacles[2]))
	}
	bounded(obstacles[0])
	bounded(obstacles[1])
	// Wilson's 95% interval for one turn of three.
	if lower, upper := *obstacles[1].Buckets[0].Lower, *obstacles[1].Buckets[0].Upper; math.Abs(lower-0.0615) > 0.001 || math.Abs(upper-0.7923) > 0.001 {
		t.Fatalf("one turn of three: %v–%v", lower, upper)
	}
	byKind := metrics(nil, []string{"family"}, rate, read)
	if value(byKind[0], "obstacle") != 500 || value(byKind[0], "stuck") != 250 || value(byKind[0], "discovery") != 0 || value(byKind[1], "discovery") != 4 {
		t.Fatalf("by kind: %+v %+v", byKind[0].Buckets, byKind[1].Buckets)
	}
	byPrompt := metrics(nil, []string{"prompted_by"}, rate, read)
	if value(byPrompt[0], "person") != 1000 || value(byPrompt[1], "person") != 2 || value(byPrompt[0], "agent") != 1000 || value(byPrompt[0], "automated") != 1000 {
		t.Fatalf("by who prompted: %+v %+v", byPrompt[0].Buckets, byPrompt[1].Buckets)
	}
	lengths := metrics([]qt.WhereTerm{{Field: "prompted_by", Op: "=", Value: "person"}}, []string{"prompt_length"}, read)
	if value(lengths[0], "1-10") != 2 || len(lengths[0].Buckets) != 1 {
		t.Fatalf("prompt lengths: %+v", lengths[0].Buckets)
	}
	repositories := metrics(nil, []string{"repository_name", "agent_role"}, read)
	if value(repositories[0], "alpha", "main") != 3 || value(repositories[0], "alpha", "subagent") != 1 {
		t.Fatalf("repositories: %+v", repositories[0].Buckets)
	}

	// A table of rates counts a row per message and kind. Kind and prompter
	// filters are applied by the source, and a count by kind alone is the
	// messages times the kinds kept, without reading a row per pair.
	friction := 0
	for _, family := range agentEventFamilies {
		if family.Group == "Friction" {
			friction++
		}
	}
	term := func(field, op, value string) qt.WhereTerm { return qt.WhereTerm{Field: field, Op: op, Value: value} }
	for _, c := range []struct {
		name  string
		where []qt.WhereTerm
		want  int
		fast  bool
	}{
		{"every kind", nil, 4 * len(agentEventFamilies), true},
		{"one kind", []qt.WhereTerm{term("family", "=", "obstacle")}, 4, true},
		{"a group but one kind", []qt.WhereTerm{term("family_group", "=", "Friction"), term("family", "!=", "obstacle")}, 4 * (friction - 1), true},
		{"a kind for one prompter", []qt.WhereTerm{term("family", "=", "stuck"), term("prompted_by", "=", "person")}, 2, false},
		{"other prompters and kinds", []qt.WhereTerm{term("family", "!=", "stuck"), term("prompted_by", "!=", "person")}, 2 * (len(agentEventFamilies) - 1), false},
	} {
		before := catalog.queryCounts.snapshot()
		var result qt.QueryResultV3
		w := apiCall(t, server, config.APIToken, http.MethodPost, "/api/query/agent_event_rates/query-v3", ungroupedRowsRequest(time.Now(), c.where...))
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Total != int64(c.want) {
			t.Fatalf("%s: total %d, want %d (%d %s)", c.name, result.Total, c.want, w.Code, w.Body.String())
		}
		for _, row := range result.Rows {
			id := firstString(row["id"])
			for _, where := range c.where {
				if where.Field == "family" && strings.HasSuffix(id, ":"+where.Value) != (where.Op == "=") {
					t.Fatalf("%s: row %s", c.name, id)
				}
			}
		}
		if after := catalog.queryCounts.snapshot(); (after.Fast > before.Fast) != c.fast {
			t.Fatalf("%s: counted from the messages = %v, want %v", c.name, after.Fast > before.Fast, c.fast)
		}
	}
}
