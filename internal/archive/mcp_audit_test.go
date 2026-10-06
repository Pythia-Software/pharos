package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func auditFixture(t *testing.T) *Catalog {
	t.Helper()
	catalog, _ := testCatalog(t)
	for _, statement := range []string{
		`INSERT INTO repositories(id,display_name,canonical_remote,created_at,updated_at) VALUES
			('repo','audit','https://github.com/acme/audit','t','t'),('other','other',NULL,'t','t')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at) VALUES
			('work','conductor','local','work','repo','Account investigation','2026-10-06'),
			('elsewhere','codex','local','elsewhere','other','Other work','2026-10-06')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,coverage) VALUES
			('conversation','work','claude','local','conversation','partial'),
			('other-conversation','elsewhere','codex','local','other-conversation','complete')`,
	} {
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return catalog
}

func addAuditMessage(t *testing.T, catalog *Catalog, id, conversation, role, kind, text string, order int, timestamp any) {
	t.Helper()
	if _, err := catalog.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,source_order,created_at,evidence_locator,content_hash)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, id, conversation, id, role, kind, text, order, timestamp, "fixture:"+id, id); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("INSERT INTO messages_fts(message_id,text) VALUES(?,?)", id, text); err != nil {
		t.Fatal(err)
	}
}

func auditCall(t *testing.T, catalog *Catalog, name string, args map[string]any) map[string]any {
	t.Helper()
	value, err := callMCP(catalog, name, args)
	if err != nil {
		t.Fatal(err)
	}
	return value.(map[string]any)
}

func TestMCPSearchMessagesPhrasesAndFilters(t *testing.T) {
	catalog := auditFixture(t)
	addAuditMessage(t, catalog, "exact", "conversation", "user", "message", "I created an AWS account today.", 1, "2026-05-05T15:10:38.071Z")
	addAuditMessage(t, catalog, "apart", "conversation", "assistant", "message", "The AWS billing account is pending.", 2, "2026-05-06T00:00:00.000Z")
	addAuditMessage(t, catalog, "metadata", "conversation", "system", "metadata", "AWS account raw metadata", 3, "2026-05-05T23:59:59.999Z")
	addAuditMessage(t, catalog, "undated", "conversation", "assistant", "tool_call", "AWS account inspection", 4, nil)
	addAuditMessage(t, catalog, "noise", "other-conversation", "user", "message", "AWS account unrelated", 1, "2026-05-05T12:00:00Z")
	addAuditMessage(t, catalog, "substring", "conversation", "user", "message", "Review the flaws in this plan.", 5, "2026-05-05T12:00:00Z")
	for _, test := range []struct {
		name string
		args map[string]any
		want int
	}{
		{"terms", map[string]any{"query": "AWS account", "repository": "audit"}, 4},
		{"quoted phrase", map[string]any{"query": `"AWS account"`, "repository": "audit"}, 3},
		{"phrase mode", map[string]any{"query": "AWS account", "match_mode": "phrase", "repository": "audit"}, 3},
		{"mixed phrase and term", map[string]any{"query": `"AWS account" created`}, 1},
		{"user role", map[string]any{"query": "AWS", "roles": []any{"user"}, "repository": "audit"}, 1},
		{"tool kind", map[string]any{"query": "AWS", "kinds": []string{"tool_call"}}, 1},
		{"whole day", map[string]any{"query": "AWS", "from": "2026-05-05", "to": "2026-05-05", "repository": "audit"}, 2},
		{"timestamp offset", map[string]any{"query": "AWS", "from": "2026-05-05T17:59:59-06:00", "to": "2026-05-05T23:59:59.999Z"}, 1},
		{"source", map[string]any{"query": "AWS", "source": "codex"}, 1},
		{"provider", map[string]any{"query": "AWS", "provider": "claude"}, 4},
		{"conversation", map[string]any{"query": "AWS", "conversation_id": "other-conversation"}, 1},
		{"no semantic fallback", map[string]any{"query": "unmatchedsignupword"}, 0},
		{"FTS operators are literal", map[string]any{"query": "AWS OR account"}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := auditCall(t, catalog, "search_messages", test.args)
			if integer(result["total"]) != int64(test.want) {
				t.Fatalf("want %d, got %#v", test.want, result)
			}
			for _, item := range result["items"].([]map[string]any) {
				if item["message_id"] == "substring" || item["evidence_locator"] == nil || item["coverage"] == nil || item["conversation_id"] == nil {
					t.Fatalf("missing evidence or substring match: %#v", item)
				}
				if _, present := item["created_at"]; !present && item["message_id"] != "undated" {
					t.Fatalf("missing timestamp: %#v", item)
				}
			}
		})
	}
	for _, args := range []map[string]any{
		{}, {"query": "!!!"}, {"query": `"AWS account`},
		{"query": "AWS", "match_mode": "semantic"},
		{"query": "AWS", "roles": "user"}, {"query": "AWS", "kinds": []any{}},
		{"query": "AWS", "roles": []any{true}}, {"query": "AWS", "kinds": []string{"all", "message"}},
		{"query": "AWS", "from": "not-a-date"}, {"query": "AWS", "from": "2026-05-06", "to": "2026-05-05"},
		{"query": "AWS", "repository": "unknown"},
	} {
		if _, err := callMCP(catalog, "search_messages", args); err == nil {
			t.Errorf("accepted invalid arguments: %#v", args)
		}
	}
}

func TestMCPSearchMessagesPunctuation(t *testing.T) {
	catalog := auditFixture(t)
	addAuditMessage(t, catalog, "punctuation", "conversation", "user", "message", "AWS - account and S3 / IAM", 1, nil)
	for _, query := range []string{"AWS account", "AWS - account", "S3 / IAM", "AWS / ... - account", "- AWS account /", `"AWS - account"`, `"S3 / IAM"`} {
		for _, mode := range []string{"terms", "phrase"} {
			t.Run(query+"/"+mode, func(t *testing.T) {
				result := auditCall(t, catalog, "search_messages", map[string]any{"query": query, "match_mode": mode})
				if integer(result["total"]) != 1 || result["items"].([]map[string]any)[0]["message_id"] != "punctuation" {
					t.Fatalf("punctuation hid indexed evidence: %#v", result)
				}
			})
		}
	}
	for _, query := range []string{"-", "/", "...", "_", "::", "- / ...", `"..."`} {
		for _, mode := range []string{"terms", "phrase"} {
			_, err := callMCP(catalog, "search_messages", map[string]any{"query": query, "match_mode": mode})
			if err == nil || err.Error() != "query must contain searchable words" {
				t.Errorf("punctuation-only query %q in %s mode: got %v", query, mode, err)
			}
		}
	}
}

func TestMCPSearchMessagesUnicode(t *testing.T) {
	catalog := auditFixture(t)
	addAuditMessage(t, catalog, "unicode", "conversation", "user", "message", "café naïve 中文 東京 Ελληνικά Москва re\u0301sume\u0301 AWS", 1, nil)
	for _, query := range []string{"café", "naïve", "cafe", "café naïve", `"café naïve"`, "中文", "東京", `"中文 東京"`, "Ελληνικά", "Москва", "re\u0301sume\u0301", `"café naïve" AWS`} {
		for _, mode := range []string{"terms", "phrase"} {
			if mode == "phrase" && query == `"café naïve" AWS` {
				continue
			}
			t.Run(query+"/"+mode, func(t *testing.T) {
				result := auditCall(t, catalog, "search_messages", map[string]any{"query": query, "match_mode": mode})
				if integer(result["total"]) != 1 || result["items"].([]map[string]any)[0]["message_id"] != "unicode" {
					t.Fatalf("Unicode query silently missed evidence: %#v", result)
				}
			})
		}
	}
}

func TestMCPSearchMessagesExhaustivePagination(t *testing.T) {
	catalog := auditFixture(t)
	for index := range 620 {
		addAuditMessage(t, catalog, fmt.Sprintf("wanted-%04d", index), "conversation", "user", "message", "AWS account", index, "2026-05-05T12:00:00Z")
		addAuditMessage(t, catalog, fmt.Sprintf("noise-%04d", index), "other-conversation", "user", "message", "AWS account", index, "2026-05-05T12:00:00Z")
	}
	seen := map[string]bool{}
	offset := 0
	for {
		result := auditCall(t, catalog, "search_messages", map[string]any{
			"query": `"AWS account"`, "repository": "audit", "limit": 100, "offset": offset, "max_output_tokens": 1200,
		})
		if integer(result["total"]) != 620 || len(jsonText(result)) > 3600 {
			t.Fatalf("cutoff or exceeded budget: %#v", result)
		}
		items := result["items"].([]map[string]any)
		for _, item := range items {
			id := firstString(item["message_id"])
			if seen[id] || !strings.HasPrefix(id, "wanted-") {
				t.Fatalf("duplicate or foreign repository result: %s", id)
			}
			seen[id] = true
		}
		if result["next_offset"] == nil {
			break
		}
		next := int(integer(result["next_offset"]))
		if next != offset+len(items) || next <= offset {
			t.Fatalf("pagination did not advance by delivered items: %#v", result)
		}
		offset = next
	}
	if len(seen) != 620 {
		t.Fatalf("missed messages beyond candidate cutoff: %d", len(seen))
	}
	result := auditCall(t, catalog, "search_messages", map[string]any{"query": "AWS", "repository": "audit", "offset": 1000})
	if len(result["items"].([]map[string]any)) != 0 || result["next_offset"] != nil {
		t.Fatalf("past-end page: %#v", result)
	}
	if _, err := callMCP(catalog, "search_messages", map[string]any{"query": "AWS", "max_output_tokens": 200}); err != nil && !strings.Contains(err.Error(), "increase the output budget") {
		t.Fatal(err)
	}
}

func TestMCPAnchoredMessageWindowContinuation(t *testing.T) {
	catalog := auditFixture(t)
	for order := 0; order < 20; order++ {
		kind, role := "message", "user"
		if order%2 == 1 {
			kind, role = "tool_call", "assistant"
		}
		addAuditMessage(t, catalog, fmt.Sprintf("m%d", order), "conversation", role, kind, strings.Repeat("evidence ", 4), order, nil)
	}
	for _, tool := range []string{"get_conversation_messages", "get_conversation_excerpt"} {
		for _, scenario := range []struct {
			name   string
			args   map[string]any
			start  int
			stride int
			kinds  string
		}{
			{"anchored default", map[string]any{"around_message_id": "m11", "limit": 4}, 9, 1, `["all"]`},
			{"unanchored default", map[string]any{"limit": 4}, 0, 2, `["message"]`},
			{"explicit kinds", map[string]any{"around_message_id": "m11", "limit": 4, "kinds": []any{"tool_call"}}, 7, 2, `["tool_call"]`},
			{"role filter", map[string]any{"around_message_id": "m11", "limit": 4, "roles": []string{"assistant"}}, 7, 2, `["all"]`},
			{"budget shortened", map[string]any{"around_message_id": "m11", "limit": 4, "max_output_tokens": 200}, 9, 1, `["all"]`},
		} {
			t.Run(tool+"/"+scenario.name, func(t *testing.T) {
				args := scenario.args
				args["conversation_id"] = "conversation"
				expected := scenario.start
				for pageNumber := 0; ; pageNumber++ {
					if pageNumber > 20 {
						t.Fatal("continuation did not reach the end")
					}
					page := auditCall(t, catalog, tool, args)
					if jsonText(page["kinds"]) != scenario.kinds || jsonText(page["roles"]) != jsonText(args["roles"]) {
						t.Fatalf("effective filters missing: %s", jsonText(page))
					}
					items := page["items"].([]map[string]any)
					if len(items) == 0 || len(jsonText(page)) > mcpBudget(args, 1200)*3 {
						t.Fatalf("empty or oversized continuation: %s", jsonText(page))
					}
					if pageNumber == 0 && scenario.name == "budget shortened" && len(items) >= 4 {
						t.Fatalf("fixture did not shorten the page: %s", jsonText(page))
					}
					for _, item := range items {
						if item["message_id"] != fmt.Sprintf("m%d", expected) {
							t.Fatalf("expected m%d, got %s", expected, jsonText(item))
						}
						expected += scenario.stride
					}
					if page["next_offset"] == nil {
						if expected < 20 {
							t.Fatalf("continuation stopped at m%d", expected)
						}
						break
					}
					if integer(page["next_offset"]) != integer(page["offset"])+int64(len(items)) {
						t.Fatalf("offset skipped returned items: %s", jsonText(page))
					}
					delete(args, "around_message_id")
					args["offset"], args["kinds"] = page["next_offset"], page["kinds"]
					if page["roles"] != nil {
						args["roles"] = page["roles"]
					}
				}
			})
		}
	}
}

func TestMCPReadableMessageWindows(t *testing.T) {
	catalog := auditFixture(t)
	for order, message := range []struct{ role, kind, text string }{
		{"system", "metadata", strings.Repeat("raw signature ", 500)},
		{"user", "message", "Please investigate AWS"},
		{"assistant", "tool_call", "aws command"},
		{"tool", "tool_result", "command output"},
		{"assistant", "message", "Found the account discussion"},
		{"assistant", "result", strings.Repeat("duplicate envelope ", 500)},
		{"user", "message", "Thanks"},
	} {
		addAuditMessage(t, catalog, fmt.Sprintf("message-%d", order), "conversation", message.role, message.kind, message.text, order, nil)
	}
	read := func(args map[string]any) map[string]any {
		args["conversation_id"] = "conversation"
		return auditCall(t, catalog, "get_conversation_messages", args)
	}
	result := read(map[string]any{"limit": 2})
	items := result["items"].([]map[string]any)
	if len(items) != 2 || items[0]["message_id"] != "message-1" || items[1]["message_id"] != "message-4" || integer(result["next_offset"]) != 2 {
		t.Fatalf("default window includes raw data: %#v", result)
	}
	result = read(map[string]any{"offset": result["next_offset"], "limit": 2})
	if items = result["items"].([]map[string]any); len(items) != 1 || items[0]["message_id"] != "message-6" || result["next_offset"] != nil {
		t.Fatalf("filtered continuation: %#v", result)
	}
	result = read(map[string]any{"roles": []any{"user"}, "limit": 12})
	if len(result["items"].([]map[string]any)) != 2 {
		t.Fatalf("role filter: %#v", result)
	}
	result = read(map[string]any{"kinds": []string{"tool_call", "tool_result"}, "limit": 12})
	if len(result["items"].([]map[string]any)) != 2 {
		t.Fatalf("tool filters: %#v", result)
	}
	result = read(map[string]any{"kinds": []any{"all"}, "limit": 1})
	if result["items"].([]map[string]any)[0]["kind"] != "metadata" {
		t.Fatalf("raw opt-in: %#v", result)
	}
	for _, anchor := range []string{"message-3", "message-4"} {
		result = read(map[string]any{"around_message_id": anchor, "limit": 2, "kinds": []string{"message"}})
		items = result["items"].([]map[string]any)
		if len(items) != 2 || items[0]["message_id"] != "message-1" || items[1]["message_id"] != "message-4" {
			t.Fatalf("filtered anchor %s: %#v", anchor, result)
		}
	}
	for _, tool := range []string{"get_conversation_messages", "get_conversation_excerpt"} {
		for _, anchor := range []string{"message-2", "message-3"} {
			result = auditCall(t, catalog, tool, map[string]any{"conversation_id": "conversation", "around_message_id": anchor})
			visible := map[string]bool{}
			for _, item := range result["items"].([]map[string]any) {
				visible[firstString(item["message_id"])] = true
			}
			if !visible["message-2"] || !visible["message-3"] {
				t.Fatalf("documented %s evidence read excluded call or result: %#v", tool, result)
			}
		}
	}
	result = read(map[string]any{"message_id": "message-0", "max_output_tokens": 500})
	if result["kind"] != "metadata" || result["next_text_offset"] == nil {
		t.Fatalf("direct raw-message read: %#v", result)
	}
	result = read(map[string]any{"message_id": "message-0", "text_offset": result["next_text_offset"], "max_output_tokens": 500})
	if integer(result["text_offset"]) == 0 || !strings.Contains(firstString(result["text"]), "signature") {
		t.Fatalf("raw text continuation: %#v", result)
	}
	if _, err := callMCP(catalog, "get_conversation_messages", map[string]any{"conversation_id": "conversation", "message_id": "message-0", "kinds": []string{"message"}}); err == nil {
		t.Fatal("explicit message filter ignored")
	}
	alias := auditCall(t, catalog, "get_conversation_excerpt", map[string]any{"conversation_id": "conversation", "roles": []string{"user"}})
	if len(alias["items"].([]map[string]any)) != 2 {
		t.Fatalf("alias did not apply filters: %#v", alias)
	}
}

func TestMCPArchiveStatus(t *testing.T) {
	catalog, config := testCatalog(t)
	host := currentHost()
	recent := now()
	old := formatTime(time.Now().Add(-48 * time.Hour))
	for _, source := range []struct {
		name, coverage, at, failure string
		pending                     int
	}{
		{"fresh", "complete", recent, "", 0},
		{"old", "complete", old, "", 0},
		{"partial", "partial", recent, "missing input", 2},
	} {
		if _, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_attempt_at,last_success_at,error,pending_count,updated_at)
			VALUES(?,?,'codex','read',?,?,?,?,?,?)`, host.ID, source.name, source.coverage, recent, source.at, source.failure, source.pending, recent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,succeeded_at,indexed_at) VALUES(?,'sync-only',?,?)`, host.ID, recent, recent); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,error) VALUES(?,'fresh',?,?,'capture failed')`, host.ID, recent, old); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_success_at,updated_at)
		VALUES('other-host','remote','claude','read','complete',?,?)`, old, recent); err != nil {
		t.Fatal(err)
	}
	catalog.setCaptureRoot(config.CaptureRoot)
	manifestDir := filepath.Join(config.CaptureRoot, host.ID, "fresh")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(captureManifest{Version: 1, UpdatedAt: recent, LastDataAt: old})
	if err := os.WriteFile(filepath.Join(manifestDir, captureManifestName), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	result := auditCall(t, catalog, "get_archive_status", map[string]any{"max_output_tokens": 4000})
	if result["status"] != "stale" || integer(result["stale_sources"]) != 4 || integer(result["other_host_stale_sources"]) != 1 || integer(result["total"]) != 5 {
		t.Fatalf("status summary: %#v", result)
	}
	bySource := map[string]map[string]any{}
	for _, item := range result["items"].([]map[string]any) {
		bySource[firstString(item["source"])] = item
	}
	if bySource["fresh"]["last_capture_at"] != old || bySource["fresh"]["last_index_at"] != recent || bySource["fresh"]["sync_error"] != "capture failed" {
		t.Fatalf("capture/index/sync evidence: %#v", bySource["fresh"])
	}
	if !strings.Contains(jsonText(bySource["old"]["stale_reasons"]), "last_index_older_than_24h") || !strings.Contains(jsonText(bySource["partial"]["stale_reasons"]), "pending_records") || !strings.Contains(jsonText(bySource["sync-only"]["stale_reasons"]), "unknown_coverage") {
		t.Fatalf("coverage reasons: %#v", bySource)
	}
	filtered := auditCall(t, catalog, "get_archive_status", map[string]any{"host_id": "other-host", "stale_only": true})
	if integer(filtered["total"]) != 1 || integer(filtered["stale_sources"]) != 4 {
		t.Fatalf("filtered list changed global summary: %#v", filtered)
	}
	first := auditCall(t, catalog, "get_archive_status", map[string]any{"limit": 1})
	second := auditCall(t, catalog, "get_archive_status", map[string]any{"limit": 1, "offset": first["next_offset"]})
	if first["items"].([]map[string]any)[0]["source"] == second["items"].([]map[string]any)[0]["source"] {
		t.Fatal("status pagination repeated a source")
	}
	catalog.setCaptureRoot("")
	unknown := auditCall(t, catalog, "get_archive_status", map[string]any{"source": "fresh"})
	if unknown["capture_times_available"] != false || unknown["items"].([]map[string]any)[0]["last_capture_at"] != nil {
		t.Fatalf("invented capture time: %#v", unknown)
	}
}

func TestMCPArchiveStatusEmptyAndCurrent(t *testing.T) {
	catalog, _ := testCatalog(t)
	result := auditCall(t, catalog, "get_archive_status", map[string]any{})
	if result["status"] != "stale" || integer(result["total"]) != 0 || result["next_offset"] != nil {
		t.Fatalf("empty archive reported current: %#v", result)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_success_at,updated_at)
		VALUES(?,'current','codex','read','complete',?,?)`, currentHost().ID, now(), now()); err != nil {
		t.Fatal(err)
	}
	result = auditCall(t, catalog, "get_archive_status", map[string]any{"stale_only": true})
	if result["status"] != "current" || integer(result["total"]) != 0 {
		t.Fatalf("current sources not recognized: %#v", result)
	}
}

func TestMCPArchiveStatusDoesNotCallPartialIndexSuccessful(t *testing.T) {
	catalog, _ := testCatalog(t)
	host := currentHost()
	recent := now()
	old := formatTime(time.Now().Add(-48 * time.Hour))
	if _, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_success_at,updated_at)
		VALUES(?,'partial-index','codex','read','partial',?,?)`, host.ID, old, recent); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,succeeded_at,indexed_at,error)
		VALUES(?,'partial-index',?,?,'index interrupted')`, host.ID, old, recent); err != nil {
		t.Fatal(err)
	}
	result := auditCall(t, catalog, "get_archive_status", map[string]any{})
	item := result["items"].([]map[string]any)[0]
	if item["last_index_at"] != recent || item["last_successful_index_at"] != old || item["last_sync_at"] != old || item["stale"] != true {
		t.Fatalf("partial write presented as successful indexing: %#v", item)
	}
}

func TestMCPArchiveStatusSuccessfulNoOpSyncRefreshesLag(t *testing.T) {
	for _, test := range []struct {
		name, coverage, indexError, syncError, reason string
		pending                                       int
		recentSync                                    bool
		automaticIndex                                bool
	}{
		{name: "successful no-op sync", coverage: "complete", recentSync: true},
		{name: "successful no-op sync with old automatic index", coverage: "complete", recentSync: true, automaticIndex: true},
		{name: "failed attempt does not refresh lag", coverage: "complete", syncError: "capture failed", reason: "last_index_older_than_24h"},
		{name: "old successful sync remains stale", coverage: "complete", reason: "last_index_older_than_24h"},
		{name: "no-op sync retains incomplete coverage", coverage: "partial", recentSync: true, reason: "incomplete_or_unknown_coverage"},
		{name: "no-op sync retains pending records", coverage: "complete", recentSync: true, pending: 2, reason: "pending_records"},
		{name: "no-op sync retains index errors", coverage: "complete", recentSync: true, indexError: "index interrupted", reason: "index_error"},
		{name: "recent success does not hide later sync failure", coverage: "complete", recentSync: true, syncError: "capture failed", reason: "sync_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := auditFixture(t)
			host := currentHost().ID
			recent := now()
			old := formatTime(time.Now().Add(-48 * time.Hour))
			if _, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_success_at,error,pending_count,updated_at)
				VALUES(?,'fixture','codex','read',?,?,?,?,?)`, host, test.coverage, old, test.indexError, test.pending, recent); err != nil {
				t.Fatal(err)
			}
			syncAt := old
			if test.recentSync {
				syncAt = recent
			}
			var automaticIndexAt any
			if test.automaticIndex {
				automaticIndexAt = old
			}
			if _, err := catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,indexed_at,error)
				VALUES(?,'fixture',?,?,?,?)`, host, recent, syncAt, automaticIndexAt, test.syncError); err != nil {
				t.Fatal(err)
			}
			status := auditCall(t, catalog, "get_archive_status", map[string]any{})
			item := status["items"].([]map[string]any)[0]
			wantStatus := "current"
			if test.reason != "" {
				wantStatus = "stale"
			}
			if status["status"] != wantStatus || item["stale"] != (test.reason != "") || test.reason != "" && !strings.Contains(jsonText(item["stale_reasons"]), test.reason) {
				t.Fatalf("unexpected no-op sync freshness: %s", jsonText(status))
			}
			if item["last_index_at"] != old || item["last_successful_index_at"] != old || item["last_sync_at"] != syncAt || item["last_checked_at"] != syncAt {
				t.Fatalf("freshness check rewrote indexing history: %s", jsonText(item))
			}
			if test.recentSync && (integer(item["lag_seconds"]) >= 120 || strings.Contains(jsonText(item["stale_reasons"]), "last_index_older_than_24h")) {
				t.Fatalf("successful no-op sync did not reset age check: %s", jsonText(item))
			}
			for _, tool := range []string{"search_messages", "search_conversations", "search_work"} {
				result := auditCall(t, catalog, tool, map[string]any{"query": "AWS"})
				freshness := result["freshness"].(map[string]any)
				if freshness["status"] != wantStatus || freshness["stale_sources"] != status["stale_sources"] {
					t.Fatalf("%s disagrees with archive status: %s", tool, jsonText(result))
				}
			}
		})
	}
}

func TestMCPAuditFreshnessAgreesAcrossTools(t *testing.T) {
	for _, test := range []struct {
		name, syncError, syncHost, status string
		manual, oldIndex, automatic       bool
		stale                             int
	}{
		{name: "current manual index", manual: true, status: "current"},
		{name: "sync failure", manual: true, automatic: true, syncError: "capture failed", status: "stale", stale: 1},
		{name: "automatic-only source", automatic: true, status: "stale", stale: 1},
		{name: "newer automatic index", manual: true, oldIndex: true, automatic: true, status: "current"},
		{name: "other-host error", manual: true, automatic: true, syncError: "capture failed", syncHost: "remote-host", status: "current"},
		{name: "no known sources", status: "stale"},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := auditFixture(t)
			host := currentHost()
			recent := now()
			if test.manual {
				indexed := recent
				if test.oldIndex {
					indexed = formatTime(time.Now().Add(-48 * time.Hour))
				}
				if _, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_success_at,updated_at)
					VALUES(?,'fixture','codex','read','complete',?,?)`, host.ID, indexed, recent); err != nil {
					t.Fatal(err)
				}
			}
			if test.automatic {
				syncHost := defaultString(test.syncHost, host.ID)
				if _, err := catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,succeeded_at,indexed_at,error)
					VALUES(?,'fixture',?,?,?)`, syncHost, recent, recent, test.syncError); err != nil {
					t.Fatal(err)
				}
			}
			search := auditCall(t, catalog, "search_messages", map[string]any{"query": "AWS"})
			freshness := search["freshness"].(map[string]any)
			for _, args := range []map[string]any{{}, {"source": "unknown", "host_id": "remote-host", "stale_only": true}} {
				status := auditCall(t, catalog, "get_archive_status", args)
				if freshness["status"] != status["status"] || freshness["stale_sources"] != status["stale_sources"] || status["status"] != test.status || integer(status["stale_sources"]) != int64(test.stale) {
					t.Fatalf("freshness disagreement: search=%#v status=%#v", freshness, status)
				}
			}
		})
	}
}

func TestMCPAuditToolsExposedAndLogged(t *testing.T) {
	catalog := auditFixture(t)
	listed := handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	tools := listed["result"].(map[string]any)["tools"].([]map[string]any)
	found := map[string]bool{}
	for _, tool := range tools {
		name := firstString(tool["name"])
		if name == "search_messages" || name == "get_archive_status" {
			found[name] = true
		}
		if name == "get_conversation_messages" || name == "get_conversation_excerpt" {
			properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
			if properties["roles"] == nil || properties["kinds"] == nil {
				t.Fatalf("filters missing from %s schema", name)
			}
		}
	}
	if !found["search_messages"] || !found["get_archive_status"] {
		t.Fatalf("audit tools missing from MCP discovery: %#v", found)
	}
	args := map[string]any{"query": "AWS account", "match_mode": "phrase", "roles": []any{"user"}, "kinds": []any{"message"}, "secret": "not logged"}
	response := handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "search_messages", "arguments": args}})
	if _, failed := toolTextOf(response); failed {
		t.Fatalf("audit tool failed through MCP: %#v", response)
	}
	var summary string
	if err := catalog.DB.QueryRow("SELECT arguments_json FROM mcp_calls WHERE tool_name='search_messages'").Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, `"match_mode":"phrase"`) || !strings.Contains(summary, `"roles":["user"]`) || strings.Contains(summary, "secret") {
		t.Fatalf("audit arguments not safely recorded: %s", summary)
	}
}
