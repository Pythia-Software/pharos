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

func TestMCPWorkDetailSectionsAndFieldContinuation(t *testing.T) {
	catalog := auditFixture(t)
	large := strings.Repeat("界 café \"quoted\"\n", 60000)
	if _, err := catalog.DB.Exec("UPDATE workspaces SET purpose=? WHERE id='work'", large); err != nil {
		t.Fatal(err)
	}
	identity := auditCall(t, catalog, "get_work_detail", map[string]any{"workspace_id": "work"})
	if identity["section"] != "identity" || len(jsonText(identity)) > 5400 || !strings.Contains(jsonText(identity), "truncated_fields") {
		t.Fatalf("unbounded identity or missing recovery: %s", jsonText(identity))
	}
	var recovered strings.Builder
	offset := 0
	for {
		chunk := auditCall(t, catalog, "get_work_detail", map[string]any{"workspace_id": "work", "field": "purpose", "text_offset": offset, "max_output_tokens": 4000})
		if len(jsonText(chunk)) > 12000 {
			t.Fatal("field chunk exceeded budget")
		}
		recovered.WriteString(asString(chunk["text"]))
		if chunk["next_text_offset"] == nil {
			break
		}
		next := int(integer(chunk["next_text_offset"]))
		if next <= offset {
			t.Fatal("field continuation failed to advance")
		}
		offset = next
	}
	if recovered.String() != large {
		t.Fatal("field continuation lost or repeated Unicode text")
	}
	for _, section := range workSectionNames() {
		page := auditCall(t, catalog, "get_work_detail", map[string]any{"workspace_id": "work", "section": section, "max_output_tokens": 4000})
		if page["section"] != section || len(jsonText(page)) > 12000 {
			t.Fatalf("invalid section %s: %s", section, jsonText(page))
		}
	}
	for _, args := range []map[string]any{
		{"workspace_id": "missing"}, {"workspace_id": "work", "section": "missing"},
		{"workspace_id": "work", "field": "not-a-field"}, {"workspace_id": "work", "field": "terminal"},
		{"workspace_id": "work", "field": "purpose", "offset": 100},
	} {
		if _, err := callMCP(catalog, "get_work_detail", args); err == nil {
			t.Fatalf("expected error: %#v", args)
		}
	}
}

func TestMCPWorkDetailPaginationBeyondLegacyLimits(t *testing.T) {
	catalog := auditFixture(t)
	if _, err := catalog.DB.Exec(`INSERT INTO change_sets(id,workspace_id,classification) VALUES('changes','work','diff')`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 125; index++ {
		if _, err := catalog.DB.Exec(`INSERT INTO change_files(change_set_id,path,status,tracked,evidence_locator) VALUES('changes',?,'modified',1,?)`, fmt.Sprintf("file-%03d.go", index), strings.Repeat("evidence", 500)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	offset := 0
	for {
		page := auditCall(t, catalog, "get_work_detail", map[string]any{"workspace_id": "work", "section": "changes", "offset": offset, "limit": 100, "max_output_tokens": 1000})
		if integer(page["total"]) != 125 || len(jsonText(page)) > 3000 {
			t.Fatalf("bad page: %s", jsonText(page))
		}
		for _, item := range page["items"].([]map[string]any) {
			path := firstString(item["path"])
			if seen[path] {
				t.Fatal("duplicate item")
			}
			seen[path] = true
		}
		if page["next_offset"] == nil {
			break
		}
		next := int(integer(page["next_offset"]))
		if next <= offset {
			t.Fatal("nonadvancing page")
		}
		offset = next
	}
	if len(seen) != 125 {
		t.Fatalf("lost change rows: %d", len(seen))
	}
}

func provenanceFixture(t *testing.T) *Catalog {
	t.Helper()
	catalog := auditFixture(t)
	for _, statement := range []string{
		`UPDATE workspaces SET location='/src/audit/.claude/worktrees/agent-a123',branch='worktree-agent-a123',head_ref='abcdef1234' WHERE id='work'`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,agent_path,origin,started_at,ended_at)
		 VALUES('child','work','claude','local','agent-a123','conversation',1,'agent-a123','/capture/parent/subagents/agent-a123.jsonl','2026-10-01','2026-10-02')`,
	} {
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	addAuditMessage(t, catalog, "assignment", "child", "user", "message", "Implement repository identity merging", 1, "2026-10-01T01:00:00Z")
	addAuditMessage(t, catalog, "use", "child", "assistant", "tool_call", "git -C /src/audit/.claude/worktrees/agent-a123 status", 2, "2026-10-01T02:00:00Z")
	addAuditMessage(t, catalog, "report", "child", "assistant", "message", "Finished. Branch: `worktree-agent-a123`. Commit: `8a399e7`. Tests pass.", 3, "2026-10-02T01:00:00Z")
	return catalog
}

func TestMCPTraceWorktreeLiteralLookupAndLineage(t *testing.T) {
	catalog := provenanceFixture(t)
	for _, target := range []string{"agent-a123", "/src/audit/.claude/worktrees/agent-a123", "worktree-agent-a123"} {
		result := auditCall(t, catalog, "trace_worktree", map[string]any{"query": target, "repository": "audit", "max_output_tokens": 4000})
		if result["search_mode"] != "literal" || integer(result["total"]) == 0 {
			t.Fatalf("missing literal evidence: %s", jsonText(result))
		}
		var child map[string]any
		for _, item := range result["items"].([]map[string]any) {
			if item["conversation_id"] == "child" {
				child = item
			}
		}
		if child == nil {
			t.Fatalf("missing child: %s", jsonText(result))
		}
		lineage := child["lineage"].(map[string]any)
		if lineage["parent_conversation_id"] != "conversation" || lineage["parent_native_id"] != "conversation" || lineage["assignment_message_id"] != "assignment" || lineage["agent_id"] != "agent-a123" {
			t.Fatalf("wrong structured lineage: %s", jsonText(lineage))
		}
		if child["relationship"] != "recorded_reference" || child["report_message_id"] != "report" || !strings.Contains(jsonText(child["reported_git_references"]), "8a399e7") || !strings.Contains(jsonText(child["reported_git_references"]), "worktree-agent-a123") {
			t.Fatalf("missing reports or overstated creation: %s", jsonText(child))
		}
	}
	for _, query := range []string{"agent-not-recorded", "agent%a123", "_a123", "AGENT-A123"} {
		result := auditCall(t, catalog, "trace_worktree", map[string]any{"query": query})
		if integer(result["total"]) != 0 || len(result["items"].([]map[string]any)) != 0 {
			t.Fatalf("nonliteral or semantic fallback for %q: %s", query, jsonText(result))
		}
	}
	overview := auditCall(t, catalog, "get_conversation_overview", map[string]any{"conversation_id": "child", "max_output_tokens": 4000})
	if overview["lineage"].(map[string]any)["parent_conversation_id"] != "conversation" {
		t.Fatal("overview lacks lineage")
	}
	if _, err := callMCP(catalog, "trace_worktree", map[string]any{"query": "agent-a123", "repository": "missing"}); err == nil {
		t.Fatal("unknown repository silently accepted")
	}
	if _, err := callMCP(catalog, "trace_worktree", map[string]any{"query": " "}); err == nil {
		t.Fatal("empty target silently accepted")
	}
	if _, err := callMCP(catalog, "trace_worktree", map[string]any{"query": "%"}); err == nil {
		t.Fatal("unsearchable transcript target silently accepted")
	}
}

func TestMCPTraceWorktreePrioritizesMetadataAndUsesIndexedFallback(t *testing.T) {
	catalog := provenanceFixture(t)
	addAuditMessage(t, catalog, "unrelated-reference", "other-conversation", "user", "message", "Discussion of agent-a123", 1, "2026-10-06")
	metadata := auditCall(t, catalog, "trace_worktree", map[string]any{"query": "agent-a123", "max_output_tokens": 4000})
	if metadata["search_scope"] != "entity_metadata" || metadata["lookup_strategy"] != "metadata_first" || metadata["reference_search_access"] == nil {
		t.Fatalf("metadata-first lookup not explicit: %s", jsonText(metadata))
	}
	for _, item := range metadata["items"].([]map[string]any) {
		if item["conversation_id"] == "other-conversation" {
			t.Fatal("known entity lookup included an unrelated transcript reference")
		}
	}
	addAuditMessage(t, catalog, "transcript-only", "other-conversation", "assistant", "message", "Created /src/worktrees/transcript-only-agent", 2, "2026-10-06")
	indexed := auditCall(t, catalog, "trace_worktree", map[string]any{"query": "transcript-only-agent", "max_output_tokens": 4000})
	if indexed["search_scope"] != "indexed_message_phrases" || integer(indexed["total"]) != 1 {
		t.Fatalf("indexed literal fallback missing: %s", jsonText(indexed))
	}
	if _, err := catalog.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash)
		VALUES('not-indexed','other-conversation','not-indexed','assistant','message','unindexed-agent','not-indexed')`); err != nil {
		t.Fatal(err)
	}
	unindexed := auditCall(t, catalog, "trace_worktree", map[string]any{"query": "unindexed-agent"})
	if integer(unindexed["total"]) != 0 || unindexed["search_scope"] != "indexed_message_phrases" {
		t.Fatal("fallback unexpectedly scanned unindexed message text")
	}
}

func TestMCPTraceWorktreeMetadataWithoutMessages(t *testing.T) {
	catalog := auditFixture(t)
	if _, err := catalog.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,location,branch,indexed_at)
		VALUES('metadata','conductor','local','metadata','Metadata only','/src/agent-metadata','only-branch','2026-10-06')`); err != nil {
		t.Fatal(err)
	}
	result := auditCall(t, catalog, "trace_worktree", map[string]any{"query": "agent-metadata"})
	items := result["items"].([]map[string]any)
	if len(items) != 1 || items[0]["workspace_id"] != "metadata" || items[0]["conversation_id"] != nil {
		t.Fatalf("metadata-only worktree missing: %s", jsonText(result))
	}
}

func TestMCPResultFreshnessIsAttributedToEvidenceSource(t *testing.T) {
	catalog := provenanceFixture(t)
	host := currentHost().ID
	recent := time.Now().UTC().Format(time.RFC3339Nano)
	old := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339Nano)
	for _, source := range []string{"fresh", "broken", "unrelated"} {
		if _, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_success_at,updated_at) VALUES(?,?,'claude','read','complete',?,?)`, host, source, recent, recent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_success_at,error,updated_at)
		VALUES('remote-host','fresh','claude','read','complete',?,'remote failure',?)`, recent, recent); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,error) VALUES(?,'broken',?,?,'capture failed'),(?,'unrelated',?,?,'other failure')`, host, recent, old, host, recent, old); err != nil {
		t.Fatal(err)
	}
	for _, sighting := range []struct{ conversation, source string }{{"child", "broken"}, {"conversation", "fresh"}} {
		if _, err := catalog.DB.Exec(`INSERT INTO conversation_sightings(conversation_id,host_id,origin,source_name,first_seen_at,last_seen_at) VALUES(?,?,'fixture',?,?,?)`, sighting.conversation, host, sighting.source, old, recent); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	catalog.setCaptureRoot(root)
	directory := filepath.Join(root, host, "broken")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(captureManifest{Version: 1, UpdatedAt: recent, LastDataAt: old})
	if err := os.WriteFile(filepath.Join(directory, captureManifestName), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	addAuditMessage(t, catalog, "parent-evidence", "conversation", "user", "message", "Repository identity merging", 1, recent)
	result := auditCall(t, catalog, "search_messages", map[string]any{"query": "identity merging", "max_output_tokens": 4000})
	if result["freshness"].(map[string]any)["status"] != "stale" {
		t.Fatal("global freshness lost sync errors")
	}
	for _, item := range result["items"].([]map[string]any) {
		freshness := item["freshness"].(map[string]any)
		sources := freshness["sources"].([]map[string]any)
		if len(sources) != 1 {
			t.Fatal("unrelated source attributed to result")
		}
		if item["conversation_id"] == "child" {
			if freshness["status"] != "stale" || sources[0]["source"] != "broken" || sources[0]["last_successful_index_at"] != recent || sources[0]["source_last_capture_at"] != old || sources[0]["evidence_capture_at"] != nil {
				t.Fatalf("capture, sync and index conflated: %s", jsonText(freshness))
			}
		} else if freshness["status"] != "current" || sources[0]["source"] != "fresh" {
			t.Fatalf("unrelated staleness affected fresh result: %s", jsonText(freshness))
		}
	}
	unknown := auditCall(t, catalog, "get_conversation_overview", map[string]any{"conversation_id": "other-conversation", "max_output_tokens": 4000})
	if unknown["freshness"].(map[string]any)["status"] != "unknown" {
		t.Fatal("missing source mapping claimed current")
	}
	if _, err := catalog.DB.Exec(`INSERT INTO workspace_sightings(workspace_id,host_id,source_name,first_seen_at,last_seen_at) VALUES('work',?,'broken',?,?)`, host, old, recent); err != nil {
		t.Fatal(err)
	}
	work := auditCall(t, catalog, "search_work", map[string]any{})
	if work["search_mode"] != "ranked_lexical_and_semantic" || work["freshness"].(map[string]any)["status"] != "stale" {
		t.Fatalf("work search mode or global freshness missing: %s", jsonText(work))
	}
	found := false
	for _, item := range work["items"].([]map[string]any) {
		if item["workspace_id"] == "work" {
			found = true
			if item["freshness"].(map[string]any)["status"] != "stale" {
				t.Fatal("work search lacks evidence-source freshness")
			}
		}
	}
	if !found {
		t.Fatal("work search lost the workspace")
	}
}

func TestMCPProvenanceSourcePreviewsHaveRecoverableAttribution(t *testing.T) {
	catalog := provenanceFixture(t)
	for index := 0; index < 6; index++ {
		if _, err := catalog.DB.Exec(`INSERT INTO conversation_sightings(conversation_id,host_id,origin,source_name,first_seen_at,last_seen_at)
			VALUES('child','host',?,?,'2026-10-01','2026-10-06')`, fmt.Sprintf("origin-%d", index), fmt.Sprintf("source-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	result := auditCall(t, catalog, "get_conversation_overview", map[string]any{"conversation_id": "child", "max_output_tokens": 4000})
	freshness := result["freshness"].(map[string]any)
	if integer(freshness["source_count"]) != 6 || len(freshness["sources"].([]map[string]any)) != 4 || freshness["sources_truncated"] != true {
		t.Fatalf("unbounded or silently incomplete source preview: %s", jsonText(result))
	}
	access := freshness["source_attribution_access"].(map[string]any)
	page := auditCall(t, catalog, "get_work_detail", map[string]any{"workspace_id": access["workspace_id"], "section": access["section"], "limit": 1})
	if integer(page["total"]) != 6 || page["next_offset"] == nil {
		t.Fatalf("source attribution cannot be recovered: %s", jsonText(page))
	}
	for _, budget := range []int{200, 500, 1000} {
		value, err := callMCP(catalog, "get_conversation_overview", map[string]any{"conversation_id": "child", "max_output_tokens": budget})
		if err == nil && len(jsonText(value)) > budget*3 {
			t.Fatal("overview exceeded explicit budget")
		}
	}
}

func TestMCPOverviewPreservesPrimaryPreviewsBeforeOptionalProvenance(t *testing.T) {
	catalog := provenanceFixture(t)
	request := strings.Repeat("request text ", 50)
	response := strings.Repeat("reply text!! ", 50)
	if _, err := catalog.DB.Exec(`INSERT INTO conversation_documents(conversation_id,initiation,initiation_message_id,outcome,outcome_message_id,vector_json,model,indexed_at)
		VALUES('child',?,'assignment',?,'report','[]','fixture',?)`, request, response, now()); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`INSERT INTO conversation_sightings(conversation_id,host_id,origin,source_name,first_seen_at,last_seen_at)
		VALUES('child',?,'fixture','fixture',?,?)`, currentHost().ID, now(), now()); err != nil {
		t.Fatal(err)
	}
	result := auditCall(t, catalog, "get_conversation_overview", map[string]any{"conversation_id": "child"})
	for field, text := range map[string]string{"request": request, "last_response": response} {
		preview := result[field].(map[string]any)
		if preview["text"] != clipText(text, 500) || firstString(preview["message_id"]) == "" {
			t.Fatalf("optional metadata displaced %s preview: %s", field, jsonText(result))
		}
	}
	if result["provenance_truncated"] != true || len(jsonText(result)) > 2100 {
		t.Fatalf("default overview did not shed optional metadata within budget: %s", jsonText(result))
	}
	full := auditCall(t, catalog, "get_conversation_overview", map[string]any{"conversation_id": "child", "max_output_tokens": 4000})
	if full["provenance_truncated"] == true || full["lineage"] == nil || full["freshness"] == nil {
		t.Fatal("large overview budget unexpectedly lost provenance")
	}
}

func TestMCPProvenanceToolDiscoveryAndArgumentLogging(t *testing.T) {
	catalog := provenanceFixture(t)
	listed := handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if !strings.Contains(jsonText(listed), `"name":"trace_worktree"`) || !strings.Contains(jsonText(listed), `"conversation_sightings"`) {
		t.Fatal("new tool or bounded sections missing from MCP discovery")
	}
	args := map[string]any{"workspace_id": "work", "section": "identity", "field": "title", "text_offset": 0, "secret": "not logged"}
	response := handleMCP(catalog, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "get_work_detail", "arguments": args}})
	if _, failed := toolTextOf(response); failed {
		t.Fatalf("bounded details failed through MCP: %#v", response)
	}
	var logged string
	if err := catalog.DB.QueryRow("SELECT arguments_json FROM mcp_calls WHERE tool_name='get_work_detail'").Scan(&logged); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged, `"section":"identity"`) || !strings.Contains(logged, `"field":"title"`) || strings.Contains(logged, "secret") {
		t.Fatalf("new arguments missing or unsafe: %s", logged)
	}
}

func TestMCPMessageWindowAutomaticallyReturnsPartialEvidence(t *testing.T) {
	catalog := auditFixture(t)
	text := strings.Repeat("界\"\n\\", 1000)
	addAuditMessage(t, catalog, "large-message", "conversation", "assistant", "tool_call", text, 1, "2026-10-06")
	page := auditCall(t, catalog, "get_conversation_messages", map[string]any{"conversation_id": "conversation", "around_message_id": "large-message", "limit": 1, "max_output_tokens": 200})
	items := page["items"].([]map[string]any)
	if len(items) != 1 || len(jsonText(page)) > 600 || items[0]["next_text_offset"] == nil || firstString(items[0]["text"]) == "" {
		t.Fatalf("partial window did not recover: %s", jsonText(page))
	}
	var recovered strings.Builder
	recovered.WriteString(asString(items[0]["text"]))
	offset := int(integer(items[0]["next_text_offset"]))
	for {
		chunk := auditCall(t, catalog, "get_conversation_messages", map[string]any{"conversation_id": "conversation", "message_id": "large-message", "text_offset": offset, "max_output_tokens": 200})
		recovered.WriteString(asString(chunk["text"]))
		if chunk["next_text_offset"] == nil {
			break
		}
		next := int(integer(chunk["next_text_offset"]))
		if next <= offset || len(jsonText(chunk)) > 600 {
			t.Fatal("direct text chunk is stuck or oversized")
		}
		offset = next
	}
	if recovered.String() != text {
		t.Fatal("partial evidence lost or repeated text")
	}
}
