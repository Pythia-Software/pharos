package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
)

// status reports the pending scope and writes nothing; run is bounded,
// resumable and idempotent, and leaves transcripts, parents and usage alone.
func TestExecutionEvidenceCLI(t *testing.T) {
	catalog, _ := testCatalog(t)
	for _, native := range []string{"thread-a", "thread-b", "thread-c"} {
		ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: native, SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
			{NativeID: native, Provider: "codex", Account: "local", Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "Hi " + native}}},
		}})
	}
	ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: "composer-1", SourceKind: "cursor", Account: "local", Conversations: []ConversationRecord{
		{NativeID: "composer-1", Provider: "cursor", Account: "local", Messages: []MessageRecord{{NativeID: "b1", Role: "user", Kind: "message", Text: "Hello"}}},
	}})
	// A catalog from before typed evidence: no state for anyone.
	if _, err := catalog.DB.Exec(`DELETE FROM execution_claims; DELETE FROM execution_evidence_state`); err != nil {
		t.Fatal(err)
	}
	fingerprint := func() string {
		t.Helper()
		rows, err := queryMaps(catalog.DB, `SELECT (SELECT value FROM query_cache_generation WHERE id=1) g,
		 (SELECT group_concat(id||COALESCE(parent_id,''),',') FROM conversations) c,
		 (SELECT COUNT(*) FROM messages) m, (SELECT COALESCE(SUM(total_tokens),0) FROM agent_sessions) u,
		 (SELECT group_concat(key||'='||value,',') FROM (SELECT key,value FROM meta ORDER BY key)) meta,
		 (SELECT value FROM execution_source_generation WHERE id=1) e,
		 (SELECT group_concat(name,',') FROM (SELECT name FROM sqlite_master ORDER BY name)) schema`)
		if err != nil {
			t.Fatal(err)
		}
		// Dirty marks, in the ledgers this catalog has created.
		for _, table := range []string{"message_labels_dirty", "agent_events_dirty"} {
			if dirty, err := queryMaps(catalog.DB, `SELECT COUNT(*) n FROM `+table); err == nil {
				rows = append(rows, dirty...)
			}
		}
		return jsonText(rows)
	}
	run := func(args ...string) map[string]any {
		t.Helper()
		var out, errs bytes.Buffer
		if err := runExecutionEvidenceCLI(context.Background(), catalog, nil, args, &out, &errs); err != nil {
			t.Fatalf("%v: %v (%s)", args, err, errs.String())
		}
		var value map[string]any
		if err := json.Unmarshal(out.Bytes(), &value); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out.String())
		}
		return value
	}
	before := fingerprint()
	status := run("status")
	if status["pending"] != float64(4) || status["conversations"] != float64(4) {
		t.Fatalf("status = %v", status)
	}
	if fingerprint() != before {
		t.Fatal("status wrote to the catalog")
	}
	reads := status["provider_read"].([]any)
	if cursor := reads[0].(map[string]any); cursor["provider"] != "cursor" || cursor["conversations"] != float64(1) || cursor["extractor"] != cursorExtractor {
		t.Fatalf("provider reads = %v", reads)
	}
	if len(reads) != 4 || reads[1].(map[string]any)["extractor"] != copilotExtractor || reads[2].(map[string]any)["extractor"] != vscodeCopilotExtractor ||
		reads[3].(map[string]any)["provider"] != "codex" || reads[3].(map[string]any)["conversations"] != float64(3) {
		t.Fatalf("provider reads = %v", reads)
	}

	first := run("run", "--batch", "1", "--limit", "3")
	if first["derived"] != float64(3) || first["status"].(map[string]any)["pending"] != float64(1) {
		t.Fatalf("bounded run = %v", first)
	}
	rest := run("run")
	after := rest["status"].(map[string]any)
	if rest["derived"] != float64(1) || after["pending"] != float64(0) || after["retained"] != float64(4) {
		t.Fatalf("resumed run = %v", rest)
	}
	// A retained-row run leaves Cursor's copy evidence to a provider read.
	if cursor := after["provider_read"].([]any)[0].(map[string]any); cursor["conversations"] != float64(1) {
		t.Fatalf("after the run, provider reads = %v", cursor)
	}
	// Parents no record names are counted by the rule that chose them.
	if fallbacks, ok := after["unavailable"].(map[string]any)["parent_fallbacks"].(map[string]any); !ok || len(fallbacks) != 0 {
		t.Fatalf("parent fallbacks = %v", after["unavailable"])
	}
	var generation string
	if err := catalog.DB.QueryRow(`SELECT value FROM meta WHERE key='execution_evidence_generation'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if again := run("run"); again["derived"] != float64(0) {
		t.Fatalf("idempotent run = %v", again)
	}
	var unchanged string
	if err := catalog.DB.QueryRow(`SELECT value FROM meta WHERE key='execution_evidence_generation'`).Scan(&unchanged); err != nil || unchanged != generation {
		t.Fatalf("a run with nothing pending changed evidence: %s → %s (%v)", generation, unchanged, err)
	}
	var stored int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&stored); err != nil || stored != 4 {
		t.Fatalf("messages = %d (%v)", stored, err)
	}
	if err := runExecutionEvidenceCLI(context.Background(), catalog, nil, []string{"run", "--batch", "0"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("a zero batch was accepted")
	}
}

// A batch that holds the writer past the budget halves the next one, and
// quick batches grow back to --batch.
func TestExecutionEvidenceBackfillBudget(t *testing.T) {
	catalog, _ := testCatalog(t)
	for _, native := range []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7"} {
		ingestRecordTx(t, catalog, WorkspaceRecord{SourceID: native, SourceKind: "codex", Account: "local", Conversations: []ConversationRecord{
			{NativeID: native, Provider: "codex", Account: "local", Messages: []MessageRecord{{NativeID: "u", Role: "user", Kind: "message", Text: "Hi " + native}}},
		}})
	}
	reset := func() {
		t.Helper()
		if _, err := catalog.DB.Exec(`DELETE FROM execution_evidence_state`); err != nil {
			t.Fatal(err)
		}
	}
	batches := func() []int {
		t.Helper()
		sizes, last := []int{}, 0
		done, err := catalog.backfillExecutionEvidenceBounded(context.Background(), 4, 0, func(done int) {
			sizes, last = append(sizes, done-last), done
		})
		if err != nil || done != 7 {
			t.Fatalf("derived %d (%v)", done, err)
		}
		return sizes
	}
	reset()
	override(t, &executionBackfillBudget, 0)
	if sizes := batches(); jsonText(sizes) != "[4,2,1]" {
		t.Fatalf("over budget, batches = %v", sizes)
	}
	reset()
	override(t, &executionBackfillBudget, time.Hour)
	if sizes := batches(); jsonText(sizes) != "[4,3]" {
		t.Fatalf("within budget, batches = %v", sizes)
	}
}
