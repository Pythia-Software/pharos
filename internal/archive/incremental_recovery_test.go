package archive

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Tests of recovery jobs: retained repair and full recapture
// (incremental_recovery.go).

func TestRetainedRepairBypassesLedgerFingerprintsAndTokenMetrics(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	appendSourceFile(t, source.Path, `{"type":"response_item","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"printf repair\"}","call_id":"repair-call"}}`+"\n"+
		`{"type":"response_item","timestamp":"2026-09-01T01:00:01Z","payload":{"type":"function_call_output","call_id":"repair-call","output":"repair"}}`+"\n"+
		`{"type":"event_msg","timestamp":"2026-09-01T01:00:02Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"output_tokens":40,"total_tokens":1040},"last_token_usage":{"input_tokens":1000,"output_tokens":40,"total_tokens":1040}}}}`+"\n")
	stamp := time.Now().Add(-time.Minute)
	if err := os.Chtimes(source.Path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "before", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatal(result)
	}
	if countRows(t, server.Catalog, "SELECT COUNT(*) FROM tool_calls") == 0 || countRows(t, server.Catalog, "SELECT COUNT(*) FROM tool_ledger_inputs") == 0 {
		t.Fatal("fixture must contain tool calls and their no-op fingerprint")
	}
	for _, statement := range []string{
		"DELETE FROM tool_calls",
		"UPDATE metrics SET value=999999 WHERE name='input_tokens'",
		"INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at) VALUES('retained-repair','retained','[]','running','now','now')",
	} {
		if _, err := server.Catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(source.Path, source.Path+".held"); err != nil {
		t.Fatal(err)
	}
	if err := server.Catalog.repairRetained(t.Context(), "retained-repair", []string{source.Name}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source.Path+".held", source.Path); err != nil {
		t.Fatal(err)
	}
	if result := server.Catalog.auditUnit(t.Context(), unit, "after", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("retained repair did not reconstruct the damaged derivations: %+v", result)
	}
}

func TestFullRecoveryCapturesSourceNotPreviouslyCaptured(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	server := automaticTestServer(t, source)
	if err := os.MkdirAll(server.Config().CaptureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	// An earlier update of the same source that stopped: completing the
	// recovery supersedes it.
	execTx(t, server.Catalog.DB, "INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at,error) VALUES('stopped','update','[\""+source.Name+"\"]','interrupted','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z','interrupted')")
	response := httptest.NewRecorder()
	server.startRecovery(response, httptest.NewRequest("POST", "/api/sync/recovery", nil), map[string]any{"mode": "full", "sources": []any{source.Name}})
	if response.Code != 202 {
		t.Fatalf("recovery rejected a source with no capture yet: %s", response.Body.String())
	}
	var receipt struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var state, detail string
		if err := server.Catalog.DB.QueryRow("SELECT state,COALESCE(error,'') FROM sync_recovery_jobs WHERE id=?", receipt.JobID).Scan(&state, &detail); err != nil {
			t.Fatal(err)
		}
		if state != "running" {
			if state != "complete" {
				t.Fatalf("recovery failed: %s: %s", state, detail)
			}
			if manifest := readCaptureManifest(t, server.Config(), source.Name); len(manifest.Files) != 1 || manifest.Files[0].RecoveryID != receipt.JobID {
				t.Fatal("recovery did not freshly capture the new source")
			}
			if got := countRows(t, server.Catalog, "SELECT COUNT(*) FROM sync_recovery_jobs WHERE id='stopped' AND state='superseded'"); got != 1 {
				t.Fatal("the completed recovery left the earlier stopped update to resume")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("recovery did not finish")
}

func TestCompletedRecoverySupersedesTheJobsItCovers(t *testing.T) {
	catalog, _ := testCatalog(t)
	execTx(t, catalog.DB,
		"INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at) VALUES('done-update','update','[\"codex\",\"tl1\"]','complete','2026-01-02T00:00:00.000Z','2026-01-03T00:00:00.000Z')",
		"INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at) VALUES('done-all','full','[]','complete','2026-01-02T00:00:00.000Z','2026-01-03T00:00:00.000Z')",
		"INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at) VALUES('update-codex','update','[\"codex\"]','interrupted','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')",
		"INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at) VALUES('update-all','update','null','failed','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')",
		"INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at) VALUES('update-wider','update','[\"codex\",\"claude\"]','failed','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')",
		"INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at) VALUES('full-codex','full','[\"codex\"]','failed','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')",
		"INSERT INTO sync_recovery_jobs(id,mode,scope_json,state,created_at,updated_at) VALUES('update-later','update','[\"codex\"]','interrupted','2026-01-02T12:00:00.000Z','2026-01-02T12:00:00.000Z')",
	)
	states := func() map[string]string {
		rows, err := queryMaps(catalog.DB, "SELECT id,state FROM sync_recovery_jobs")
		if err != nil {
			t.Fatal(err)
		}
		states := map[string]string{}
		for _, row := range rows {
			states[firstString(row["id"])] = firstString(row["state"])
		}
		return states
	}
	// An update of two sources redoes an earlier update within them, not one
	// of every source, of a wider set, or a full recovery, nor one started later.
	catalog.supersedeRecoveryJobs(t.Context(), "done-update", "update", []string{"codex", "tl1"})
	if got := states(); got["update-codex"] != "superseded" || got["update-all"] != "failed" || got["update-wider"] != "failed" || got["full-codex"] != "failed" || got["update-later"] != "interrupted" {
		t.Fatalf("after a two-source update: %v", got)
	}
	// A full recovery of every source redoes every earlier recovery.
	catalog.supersedeRecoveryJobs(t.Context(), "done-all", "full", nil)
	if got := states(); got["update-all"] != "superseded" || got["update-wider"] != "superseded" || got["full-codex"] != "superseded" || got["update-later"] != "interrupted" || got["done-update"] != "complete" {
		t.Fatalf("after a full recovery: %v", got)
	}
}
