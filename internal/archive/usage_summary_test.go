package archive

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestUsageHistory(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	read := func() any { return libraryStatusCall(t, server, "/api/usage/history")["oldest_conversation_at"] }
	if got := read(); got != nil {
		t.Fatalf("empty history: %v", got)
	}
	execTx(t, catalog.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w-history','codex','local','history','History','2026-10-22')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) VALUES
			('recent','w-history','codex','local','recent','2026-10-01T00:00:00Z'),
			('missing','w-history','codex','local','missing',NULL),
			('invalid','w-history','codex','local','invalid','unknown'),
			('blank','w-history','codex','local','blank',''),
			('utc','w-history','codex','local','utc','2025-02-21T23:45:00Z'),
			('offset','w-history','codex','local','offset','2025-02-22T01:30:00+02:00')`)
	if got := read(); got != "2025-02-22T01:30:00+02:00" {
		t.Fatalf("oldest conversation: %v", got)
	}
	execTx(t, catalog.DB, `UPDATE conversations SET started_at='2023-01-01T00:00:00Z' WHERE id='recent'`)
	if got := read(); got != "2023-01-01T00:00:00Z" {
		t.Fatalf("history after older import: %v", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := catalog.UsageHistory(ctx); err != context.Canceled {
		t.Fatalf("canceled history: %v", err)
	}
}

func TestUsageSummaryWindows(t *testing.T) {
	catalog, _ := testCatalog(t)
	// Usage lands in the 23:00 hour (1020 tokens) and the 01:00 hour (2030).
	ingestUsageFixture(t, catalog)
	now := time.Date(2026, 9, 21, 2, 15, 0, 0, time.UTC)
	catalog.now = func() time.Time { return now }
	// Keep the authorship rebuild from replacing the rows below.
	catalog.background = func(OperationSpec, func(context.Context, *Op) error) bool { return false }
	for _, message := range []struct {
		id     string
		sentAt time.Time
		typed  int64
	}{
		{"recent", now.Add(-30 * time.Minute), 12},
		{"earlier", now.Add(-5 * time.Hour), 30},
		{"old", now.Add(-40 * 24 * time.Hour), 100},
		{"machine", now.Add(-10 * time.Minute), 0},
	} {
		if _, err := catalog.DB.Exec(`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,typed_chars,typed_words,spans_json)
			VALUES(?,?,?,?,?,?,?,?,'[]')`, message.id, "c", "w", message.sentAt.Format(time.RFC3339), message.sentAt.Format("2006-01-02"), 100, message.typed*5, message.typed); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := catalog.UsageSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	windows := map[string]*usageSummaryWindow{}
	for _, window := range summary["windows"].([]*usageSummaryWindow) {
		windows[window.Key] = window
	}
	for key, want := range map[string]struct{ words, messages, tokens int64 }{
		// 01:15 starts the window a quarter into the 01:00 hour: 3/4 of 2030.
		"1h":  {12, 1, 1523},
		"6h":  {42, 2, 3050},
		"30d": {42, 2, 3050},
		"all": {142, 3, 3050},
	} {
		got := windows[key]
		if got == nil || got.HumanWords != want.words || got.Messages != want.messages || got.Tokens != want.tokens {
			t.Fatalf("%s: got %+v, want %+v", key, got, want)
		}
	}
	if len(windows) != 7 {
		t.Fatalf("windows: %#v", windows)
	}
}

func TestUsageSummaryLedgerMatchesUsagePage(t *testing.T) {
	catalog, _ := testCatalog(t)
	catalog.background = func(OperationSpec, func(context.Context, *Op) error) bool { return false }
	for _, item := range []struct {
		id, source, model string
		total             int64
	}{
		{"native", "codex", " gpt-6-sol ", 100},
		{"wrapper", "conductor", "gpt-6-sol", 999},
		{"zero-native", "codex", "gpt-6-sol", 0},
		{"zero-wrapper", "conductor", "", 7},
		{"unknown", "claude", "", 11},
	} {
		if _, err := catalog.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at)
			VALUES(?,?,'local',?,?,'2026-09-23')`, item.id, item.source, item.id, item.id); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.DB.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id)
			VALUES(?,?,'codex','local',?)`, "c-"+item.id, item.id, item.id); err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.DB.Exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model)
			VALUES(?,?,?,'main','root','codex',?)`, "s-"+item.id, item.id, "c-"+item.id, nilIfEmpty(item.model)); err != nil {
			t.Fatal(err)
		}
		// Separate effort/attribution buckets must retain per-row rounding.
		for _, effort := range []string{"high", "low"} {
			if _, err := catalog.DB.Exec(`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,
				total_tokens,uncached_input_tokens,cache_read_input_tokens,cache_creation_input_tokens,
				cache_creation_1h_input_tokens,output_tokens,unclassified_tokens)
				VALUES(?,?,' ',?,'request',?,1,2,3,1,2,1)`, "s-"+item.id, "2026-09-21T01:00:00Z", effort, item.total); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := catalog.DB.Exec(`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json)
		VALUES('c-native','c-wrapper','mirror',1,'{}'),('c-zero-native','c-zero-wrapper','mirror',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	// The representative has older usage than its wrapper. Choosing mirrors
	// after restricting a query to a recent window would count the wrapper.
	if _, err := catalog.DB.Exec(`UPDATE agent_session_usage SET usage_hour='2026-08-01T01:00:00Z' WHERE agent_session_id='s-native'`); err != nil {
		t.Fatal(err)
	}
	legacy, err := catalog.representativeLedger()
	if err != nil {
		t.Fatal(err)
	}
	want := make([]usageSummaryEntry, 0, len(legacy))
	for _, row := range legacy {
		tokens := map[string]int64{}
		for _, field := range []string{"total_tokens", "uncached_input_tokens", "cache_read_input_tokens",
			"cache_creation_input_tokens", "cache_creation_1h_input_tokens", "output_tokens", "unclassified_tokens"} {
			tokens[field] = integer(row[field])
		}
		want = append(want, usageSummaryEntry{workspaceID: firstString(row["workspace_id"]), hour: firstString(row["usage_hour"]), model: ledgerModel(row), tokens: tokens})
	}
	got, err := catalog.usageSummaryLedger(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	order := func(rows []usageSummaryEntry) {
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].workspaceID+rows[i].hour+rows[i].model < rows[j].workspaceID+rows[j].hour+rows[j].model
		})
	}
	order(want)
	order(got)
	if len(got) != 6 || !reflect.DeepEqual(got, want) {
		t.Fatalf("summary ledger differs from Usage: got %+v, want %+v", got, want)
	}
	catalog.now = func() time.Time { return time.Date(2026, 9, 21, 2, 15, 0, 0, time.UTC) }
	summary, err := catalog.UsageSummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	windows := summary["windows"].([]*usageSummaryWindow)
	// Two 7-token rows and two 11-token rows, each rounded independently.
	if windows[0].Tokens != 26 || windows[6].Tokens != 236 {
		t.Fatalf("fractional/mirrored totals: 1h=%d all=%d", windows[0].Tokens, windows[6].Tokens)
	}
}

func TestUsageSummaryEmptyAndCanceled(t *testing.T) {
	catalog, _ := testCatalog(t)
	catalog.background = func(OperationSpec, func(context.Context, *Op) error) bool { return false }
	summary, err := catalog.UsageSummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range summary["windows"].([]*usageSummaryWindow) {
		if window.Tokens != 0 || window.Messages != 0 || window.HumanWords != 0 || window.CostUSD != nil || window.CarbonTokens != nil {
			t.Fatalf("empty window: %+v", window)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := catalog.UsageSummary(ctx); err != context.Canceled {
		t.Fatalf("canceled summary: %v", err)
	}
}

func BenchmarkUsageSummary(b *testing.B) {
	catalog, _ := testCatalog(b)
	catalog.background = func(OperationSpec, func(context.Context, *Op) error) bool { return false }
	catalog.now = func() time.Time { return time.Date(2026, 9, 21, 2, 15, 0, 0, time.UTC) }
	// Approximately the real catalog's 46,500 hourly ledger entries, with
	// workspace text large enough to reveal accidental full-record reads.
	if _, err := catalog.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,purpose,indexed_at)
		VALUES('w','codex','local','w','Work',?,'2026-09-21');
		INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','codex','local','c')`, strings.Repeat("purpose ", 128*1024)); err != nil {
		b.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<500)
		INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model)
		SELECT 's'||x,'w','c','s'||x,'root','codex','gpt-6-sol' FROM n;
		WITH RECURSIVE hours(h) AS (VALUES(0) UNION ALL SELECT h+1 FROM hours WHERE h<95)
		INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,total_tokens,uncached_input_tokens,output_tokens)
		SELECT a.id,strftime('%Y-%m-%dT%H:00:00Z','2026-09-21',printf('-%d hours',h)),'','','request',101,99,2
		FROM agent_sessions a CROSS JOIN hours`); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := catalog.UsageSummary(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func TestUsageSummaryTodayUsesLocalMidnight(t *testing.T) {
	catalog, _ := testCatalog(t)
	catalog.background = func(OperationSpec, func(context.Context, *Op) error) bool { return false }
	ingestUsageFixture(t, catalog)
	for _, item := range []struct{ zone, now, from string }{
		{"America/Denver", "2026-03-08T10:00:00Z", "2026-03-08T07:00:00Z"},
		{"America/Denver", "2026-11-01T10:00:00Z", "2026-11-01T06:00:00Z"},
		{"Asia/Kathmandu", "2026-10-29T05:00:00Z", "2026-10-28T18:15:00Z"},
	} {
		t.Run(item.zone+item.now, func(t *testing.T) {
			zone, err := time.LoadLocation(item.zone)
			if err != nil {
				t.Fatal(err)
			}
			override(t, &time.Local, zone)
			now, _ := time.Parse(time.RFC3339, item.now)
			from, _ := time.Parse(time.RFC3339, item.from)
			catalog.now = func() time.Time { return now }
			if _, err := catalog.DB.Exec("DELETE FROM message_authorship"); err != nil {
				t.Fatal(err)
			}
			for index, at := range []time.Time{from.Add(-time.Second), from, now.Add(-time.Second), now, now.Add(time.Hour)} {
				if _, err := catalog.DB.Exec(`INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,typed_chars,typed_words,spans_json) VALUES(?,?,?,?,?,10,10,2,'[]')`, fmt.Sprint(index), "c", "w", at.UTC().Format(time.RFC3339), at.In(zone).Format("2006-01-02")); err != nil {
					t.Fatal(err)
				}
			}
			summary, err := catalog.UsageSummary(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, window := range summary["windows"].([]*usageSummaryWindow) {
				if window.Key != "today" {
					continue
				}
				if window.From != item.from || window.Until != item.now || window.HumanWords != 4 || window.Messages != 2 {
					t.Fatalf("Today: %+v", window)
				}
			}
		})
	}
}
