package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestTeamsSharingGateBlocksConcurrentPrivacyMutation(t *testing.T) {
	c, _ := testCatalog(t)
	lease, err := c.teamsSharingLease()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.setTeamsPrivate(context.Background(), "synthetic", true); !errors.Is(err, errTeamsSharingBusy) {
		lease.Close()
		t.Fatalf("privacy mutation bypassed active send gate: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := c.teamsSharingLease()
	if err != nil {
		t.Fatalf("gate remained held: %v", err)
	}
	next.Close()
}

func TestTeamsOutboxReviewBindsExactPayloadAndRevisions(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	key := make([]byte, 32)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','synthetic-account','w','Synthetic','2026-01-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','codex','synthetic-account','c')`,
		`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('s','w','c','main','root','codex')`,
		`INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,total_tokens) VALUES('s','2026-01-01T00:00:00Z','m','','request',123)`)
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	settings, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), true, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.teamsSourceSnapshot(ctx, key, teamsSyntheticResolver)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compileTeamsQuery(teamsExampleQuery)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]any{"query_url": teamsExampleQuery, "approval_policy": "metrics_or_review", "question": "Synthetic tokens", "requester": map[string]any{"id": "person", "display_name": "Synthetic"}, "audiences": map[string]any{"response_readers": []any{map[string]any{"kind": "person", "id": "person"}}, "publications": []any{}, "exports": []any{}}, "min_app_version": "1.0.0", "retention_policy": map[string]any{"mode": "organization_controlled", "duration_days": nil, "policy_version": 1}, "result_schema": map[string]any{"columns": []any{map[string]any{"id": "total_tokens", "type": "integer", "role": "metric", "merge": "sum", "metric_id": "captured_tokens", "unit": "tokens"}}}})
	workID := "a4c09e42-d0a6-45f0-89ef-2a03b7cff630"
	versionID := "3ed716e3-fca9-4299-b5f9-9f0d6c880fe0"
	idem := "8134b012-9ab5-4dd4-b3ec-1d5f9e474de7"
	w := teams.Work{ID: workID, VersionID: versionID, LibraryID: d.ID, Kind: "deterministic", Period: json.RawMessage(`{"key":"once"}`), PopulationKey: "current", EvaluationRevision: 1, Config: config, ConfigSHA256: teamsPayloadHash(config), ExpiresAt: time.Now().Add(time.Hour)}
	for _, suffix := range []string{`,"tools":[]}`, `,"query_url":"other"}`, `,"Approval_Policy":"review"}`} {
		unsupported := w
		unsupported.Config = append(append([]byte(nil), config[:len(config)-1]...), suffix...)
		unsupported.ConfigSHA256 = teamsPayloadHash(unsupported.Config)
		if _, err := teamsPlanFromWork(unsupported); !errors.Is(err, errTeamsQueryUnsupported) {
			t.Fatalf("unknown or duplicate work config accepted: %v", err)
		}
	}
	policy := teams.MetricsDefault{Value: true, PolicyVersion: 1}
	if _, err = c.prepareTeamsManifest(ctx, w, d, snapshot, policy); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("opt-out registered without grant: %v", err)
	}
	if err = c.grantTeamsQuestion(ctx, w, d, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	prepared, err := c.prepareTeamsManifest(ctx, w, d, snapshot, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, manifestHash, err := prepared.Manifest.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	sources := teams.SourcesResponse{ManifestSHA256: manifestHash, OwnershipRevision: 3, OwnedSourceTokens: []string{snapshot.Entries[0].Token}}
	registration, err := newTeamsRegistration(prepared.Manifest, sources, teams.PrivacySnapshot{PrivacyRevision: 4, PrivateSourceTokens: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.teamsExecuteNumeric(ctx, plan, key, teamsSyntheticResolver, snapshot, registration)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(teams.Payload{Outcome: "completed", Form: "snapshot", ObservedAt: time.Now().UTC(), ComputedAt: time.Now().UTC(), Coverage: teams.Coverage{SourceState: "complete", Measurement: "measured", Caveats: []string{}}, Rows: []teams.Row{{Key: []json.RawMessage{}, Values: map[string]json.RawMessage{"total_tokens": json.RawMessage(`123`)}}}, DeleteKeys: [][]json.RawMessage{}})
	staged, err := c.stageTeamsNumeric(ctx, w, d, snapshot, result, prepared, idem, payload)
	if err != nil || staged.State != "awaiting_review" {
		t.Fatalf("stage: %+v %v", staged, err)
	}
	if _, err = c.inspectTeamsApproved(ctx, d, workID, 4, 3, policy); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("unreviewed draft allowed: %v", err)
	}
	if _, err = c.reviewTeamsDraft(ctx, d, workID, "wrong", 4, 3, policy); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("wrong payload reviewed: %v", err)
	}
	if _, err = c.reviewTeamsDraft(ctx, d, workID, staged.PayloadHash, 4, 3, policy); err != nil {
		t.Fatal(err)
	}
	approved, err := c.inspectTeamsApproved(ctx, d, workID, 4, 3, policy)
	if err != nil || string(approved.Payload) != string(payload) {
		t.Fatalf("approved: %+v %v", approved, err)
	}
	if _, err = c.inspectTeamsApproved(ctx, d, workID, 5, 3, policy); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("changed shared privacy: %v", err)
	}
	if _, err = c.setTeamsPrivate(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.inspectTeamsApproved(ctx, d, workID, 4, 3, policy); !errors.Is(err, errTeamsSnapshotChanged) {
		t.Fatalf("changed local privacy: %v", err)
	}
	bad := append([]byte(nil), payload...)
	bad[len(bad)-2] = 'x'
	if _, err = c.stageTeamsNumeric(ctx, w, d, snapshot, result, prepared, idem, bad); err == nil {
		t.Fatal("bad payload staged")
	}
	// Recompute after the policy change; review a fresh draft, then preserve
	// the exact attempted request through an uncertain delivery and receipt.
	snapshot, err = c.teamsSourceSnapshot(ctx, key, teamsSyntheticResolver)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err = c.prepareTeamsManifest(ctx, w, d, snapshot, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, manifestHash, err = prepared.Manifest.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	sources = teams.SourcesResponse{ManifestSHA256: manifestHash, OwnershipRevision: 3, OwnedSourceTokens: []string{snapshot.Entries[0].Token}}
	registration, err = newTeamsRegistration(prepared.Manifest, sources, teams.PrivacySnapshot{PrivacyRevision: 4, PrivateSourceTokens: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err = c.teamsExecuteNumeric(ctx, plan, key, teamsSyntheticResolver, snapshot, registration)
	if err != nil {
		t.Fatal(err)
	}
	staged, err = c.stageTeamsNumeric(ctx, w, d, snapshot, result, prepared, idem, payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.reviewTeamsDraft(ctx, d, workID, staged.PayloadHash, 4, 3, policy); err != nil {
		t.Fatal(err)
	}
	reserved, err := c.reserveTeamsReviewed(ctx, d, workID, 4, 3, policy)
	if err != nil || reserved.State != "sending" {
		t.Fatalf("reserve: %+v %v", reserved, err)
	}
	var submitted teams.Submission
	if json.Unmarshal(reserved.Submission, &submitted) != nil || string(submitted.Payload) != string(payload) || submitted.PayloadSHA256 != staged.PayloadHash {
		t.Fatal("submitted payload changed")
	}
	if err = c.recoverTeamsInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.markTeamsSendOutcome(ctx, workID, "unconfirmed", "authorization"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.stageTeamsNumeric(ctx, w, d, snapshot, result, prepared, idem, payload); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("unconfirmed submission was replaced: %v", err)
	}
	receipt := teams.Receipt{ID: "ec6d6104-a140-4bc3-a81d-d7ba9ac76d82", WorkID: workID, PayloadSHA256: staged.PayloadHash, SubmissionSHA256: reserved.SubmissionHash, IdempotencyKey: reserved.IdempotencyKey, InstallationID: reserved.InstallationID, ResultRevision: 1, AcceptedAt: time.Now().UTC(), Outcome: "completed"}
	wrong := receipt
	wrong.PayloadSHA256 = teamsPayloadHash([]byte("other"))
	if err = c.recordTeamsReceipt(ctx, d, "synthetic-org", "00000000-0000-4000-8000-000000000003", workID, wrong); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("wrong receipt: %v", err)
	}
	wrong = receipt
	wrong.SubmissionSHA256 = teamsPayloadHash([]byte("another envelope"))
	if err = c.recordTeamsReceipt(ctx, d, "synthetic-org", "00000000-0000-4000-8000-000000000003", workID, wrong); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("different envelope receipt accepted: %v", err)
	}
	if err = c.recordTeamsReceipt(ctx, d, "synthetic-org", "00000000-0000-4000-8000-000000000003", workID, receipt); err != nil {
		t.Fatal(err)
	}
	if err = c.recordTeamsReceipt(ctx, d, "synthetic-org", "00000000-0000-4000-8000-000000000003", workID, receipt); err != nil {
		t.Fatalf("receipt retry: %v", err)
	}
	var sent []byte
	if err = c.DB.QueryRow(`SELECT submission FROM teams_send_log WHERE work_id=?`, workID).Scan(&sent); err != nil || string(sent) != string(reserved.Submission) {
		t.Fatalf("local log: %v", err)
	}
	target, _ := testCatalog(t)
	if err = mergeHistory(ctx, target, c.Path); err != nil {
		t.Fatal(err)
	}
	var copied []byte
	if err = target.DB.QueryRow(`SELECT submission FROM teams_send_log WHERE work_id=?`, workID).Scan(&copied); err != nil || string(copied) != string(sent) {
		t.Fatalf("transferred log: %v", err)
	}
	var active int
	if err = target.DB.QueryRow(`SELECT COUNT(*) FROM teams_outbox`).Scan(&active); err != nil || active != 0 {
		t.Fatalf("active outbox transferred: %d %v", active, err)
	}
	// Automatic statistics need an acknowledged current org policy and the
	// local opt-in. A policy change or opt-out blocks a staged answer.
	if _, err = c.setTeamsLocalSettings(ctx, d.ID, d.Instance, "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), true, false, 1); err != nil {
		t.Fatal(err)
	}
	snapshot, err = c.teamsSourceSnapshot(ctx, key, teamsSyntheticResolver)
	if err != nil {
		t.Fatal(err)
	}
	w.ID = "2b29fdba-c7d9-4df6-a85f-8a39ab5f8741"
	prepared, err = c.prepareTeamsManifest(ctx, w, d, snapshot, policy)
	if err != nil || prepared.Mode != "automatic_metrics" {
		t.Fatalf("automatic: %+v %v", prepared, err)
	}
	_, manifestHash, err = prepared.Manifest.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	sources = teams.SourcesResponse{ManifestSHA256: manifestHash, OwnershipRevision: 3, OwnedSourceTokens: []string{snapshot.Entries[0].Token}}
	registration, err = newTeamsRegistration(prepared.Manifest, sources, teams.PrivacySnapshot{PrivacyRevision: 4, PrivateSourceTokens: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err = c.teamsExecuteNumeric(ctx, plan, key, teamsSyntheticResolver, snapshot, registration)
	if err != nil {
		t.Fatal(err)
	}
	staged, err = c.stageTeamsNumeric(ctx, w, d, snapshot, result, prepared, idem, payload)
	if err != nil || staged.State != "approved" {
		t.Fatalf("automatic stage: %+v %v", staged, err)
	}
	if _, err = c.inspectTeamsApproved(ctx, d, w.ID, 4, 3, teams.MetricsDefault{Value: false, PolicyVersion: 1}); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("disabled org default allowed: %v", err)
	}
	if _, err = c.inspectTeamsApproved(ctx, d, w.ID, 4, 3, policy); err != nil {
		t.Fatal(err)
	}
	if _, err = c.setTeamsLocalSettings(ctx, d.ID, d.Instance, "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), true, true, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = c.inspectTeamsApproved(ctx, d, w.ID, 4, 3, policy); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("opt-out allowed: %v", err)
	}
}

func TestTeamsMoveDisarmsPendingWork(t *testing.T) {
	a, b := lifecycleLibraries(t)
	abandoned, err := os.MkdirTemp(filepath.Dir(a.CatalogPath), ".pharos-transfer-source-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(abandoned, "history.sqlite3"), []byte("synthetic stale snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(abandoned, ".pharos-transfer-stage"), []byte("pharos-transfer-source-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := reopenCatalog(t, a.CatalogPath)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('ledger-w','test','local','ledger-w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('ledger-c','ledger-w','test','local','ledger-c'),('ledger-current','ledger-w','test','local','ledger-current')`)
	if _, err := c.setTeamsPrivate(t.Context(), "ledger-c", true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.setTeamsPrivate(t.Context(), "ledger-c", false); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pending", "flagged", "pending", "clear"} {
		if _, err := c.setTeamsClassification(t.Context(), "ledger-c", "synthetic-flag", state); err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []string{"pending", "flagged"} {
		if _, err := c.setTeamsClassification(t.Context(), "ledger-current", "current-flag", state); err != nil {
			t.Fatal(err)
		}
	}
	execTx(t, c.DB, `UPDATE teams_local_settings SET library_id='synthetic',instance_id='instance',org_id='org',enabled=1`,
		`INSERT INTO teams_source_bindings(binding_id,provenance_sha256,host_id,source_name,source_kind,provider,account_label,source_root,created_at) VALUES('binding','hash','host','source','codex','codex','local','/synthetic','now')`,
		`INSERT INTO teams_source_mapping_revisions(revision,binding_id,library_id,instance_id,origin,org_id,person_id,installation_id,provider,mode,reviewed_at) VALUES(1,'binding','synthetic','instance','https://synthetic.invalid','org','person','installation','codex','work','now')`,
		`INSERT INTO teams_source_mapping_current(binding_id,revision) VALUES('binding',1)`,
		`INSERT INTO teams_source_mapping_exposure(binding_id,mapping_revision,exposed_at) VALUES('binding',1,'now')`,
		`INSERT INTO teams_outbox(work_id,version_id,library_id,instance_id,org_id,installation_id,config,config_hash,payload,payload_hash,expires_at,idempotency_key,state,local_revision,generation,privacy_revision,settings_revision,shared_privacy_revision,ownership_revision,evaluation_revision,baseline_revision,manifest,manifest_hash,owned_hash,approval_mode,policy_version) VALUES('pending','version','synthetic','instance','org','installation','{}','hash','synthetic-unsent-secret-marker','hash','2099-01-01T00:00:00Z','key','approved',1,1,1,1,1,1,1,0,'{}','hash','hash','reviewed',0)`,
		`INSERT INTO teams_v2_uploads(work_id,version_id,library_id,instance_id,org_id,installation_id,config_sha256,snapshot_id,binding_id,inventory_revision,inventory_sha256,local_revision,evaluation_revision,settings_revision,source_revision,privacy_revision,generation,mode,policy_version,authorization_json,pages_json,seal_json,state) VALUES('content','version','synthetic','instance','org','installation','hash','snapshot','binding',1,'hash',1,1,1,1,1,1,'reviewed',0,'{}','[]','{}','bound')`,
		`INSERT INTO teams_content_runs(work_id,config,binding_json,selected_json,state,payload,payload_sha256) VALUES('content','{}','{}','[]','awaiting_review','synthetic-content-draft-marker','hash')`)
	c.Close()
	checkedStage := false
	if _, err := TransferLibrary(t.Context(), a, b, true, func(stage string) {
		if stage != "Copying retained evidence" {
			return
		}
		sourceStages, err := filepath.Glob(filepath.Join(filepath.Dir(a.CatalogPath), ".pharos-transfer-source-*"))
		if err != nil || len(sourceStages) != 0 {
			t.Errorf("source snapshot still present during evidence walk: %v", err)
			return
		}
		paths, err := filepath.Glob(filepath.Join(b.CaptureRoot, ".imports", ".stage-*", "history.sqlite3"))
		if err != nil || len(paths) != 1 {
			t.Errorf("target staging unavailable: %v", err)
			return
		}
		data, err := os.ReadFile(paths[0])
		if err != nil || bytes.Contains(data, []byte("synthetic-unsent-secret-marker")) || bytes.Contains(data, []byte("synthetic-content-draft-marker")) {
			t.Errorf("unsent payload reached target staging: %v", err)
			return
		}
		checkedStage = true
	}); err != nil {
		t.Fatal(err)
	}
	if !checkedStage {
		t.Fatal("target staging was not checked")
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("abandoned source snapshot remains: %v", err)
	}
	source := reopenCatalog(t, a.CatalogPath)
	defer source.Close()
	var enabled bool
	var state string
	if err := source.DB.QueryRow(`SELECT enabled FROM teams_local_settings WHERE id=1`).Scan(&enabled); err != nil || enabled {
		t.Fatalf("enabled after move: %v %v", enabled, err)
	}
	if err := source.DB.QueryRow(`SELECT state FROM teams_outbox WHERE work_id='pending'`).Scan(&state); err != nil || state != "approved" {
		t.Fatalf("pending after move: %s %v", state, err)
	}
	if err := source.DB.QueryRow(`SELECT state FROM teams_content_runs WHERE work_id='content'`).Scan(&state); err != nil || state != "awaiting_review" {
		t.Fatalf("content draft lost on source: %s %v", state, err)
	}
	if n := countRows(t, source, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='ledger-c' AND kind='positive_flag' AND evidence='synthetic-flag' AND active=1`); n != 1 {
		t.Fatalf("source flag lost: %d", n)
	}
	if n := countRows(t, source, `SELECT count(*) FROM teams_protection_assertions a JOIN teams_protection_actions x ON x.action_id=a.retired_by_action_id WHERE a.conversation_id='ledger-c' AND a.kind='explicit_private' AND a.active=0 AND x.retired_count=1`); n != 1 {
		t.Fatalf("source retirement lost: %d", n)
	}
	if n := countRows(t, source, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='ledger-current' AND kind='positive_flag' AND evidence='current-flag' AND active=1`); n != 1 {
		t.Fatalf("current source flag duplicated or lost: %d", n)
	}
	if n := countRows(t, source, `SELECT count(*) FROM teams_protection_baseline WHERE state='held'`); n != 1 {
		t.Fatal("source baseline not held")
	}
	if n := countRows(t, source, `SELECT count(*) FROM teams_protection_observations`); n != 0 {
		t.Fatal("source observations retained")
	}
	if n := countRows(t, source, `SELECT COUNT(*) FROM teams_source_mapping_exposure WHERE binding_id='binding' AND mapping_revision=1`); n != 1 {
		t.Fatal("source exposure fence was lost during move")
	}
	dest := reopenCatalog(t, b.CatalogPath)
	defer dest.Close()
	if n := countRows(t, dest, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='ledger-c' AND kind='legacy_hold' AND evidence='unreconciled_import' AND active=1`); n != 1 {
		t.Fatalf("destination import hold missing: %d", n)
	}
	if n := countRows(t, dest, `SELECT count(*) FROM teams_protection_baseline WHERE state='held'`); n != 1 {
		t.Fatal("destination baseline not held")
	}
	var n int
	if err := dest.DB.QueryRow(`SELECT COUNT(*) FROM teams_outbox`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("outbox moved: %d %v", n, err)
	}
	if err := dest.DB.QueryRow(`SELECT COUNT(*) FROM teams_content_runs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("content draft moved: %d %v", n, err)
	}
	if n := countRows(t, dest, `SELECT COUNT(*) FROM teams_source_mapping_current`); n != 0 {
		t.Fatal("source account authorization moved to another library")
	}
}

func TestTeamsNumericPayloadRejectsNullAndUntrustedCaveat(t *testing.T) {
	plan := teamsQueryPlan{Metrics: []teamsMetric{{ID: "total", Field: "total_tokens"}}}
	result := teamsNumericResult{Values: map[string]int64{"total": 0}, Measured: true}
	payload := teams.Payload{Outcome: "completed", Form: "snapshot", ObservedAt: time.Now(), ComputedAt: time.Now(), Coverage: teams.Coverage{SourceState: "complete", Measurement: "measured", Caveats: []string{}}, Rows: []teams.Row{{Key: []json.RawMessage{}, Values: map[string]json.RawMessage{"total": json.RawMessage(`0`)}}}, DeleteKeys: [][]json.RawMessage{}}
	check := func(want bool) {
		raw, _ := json.Marshal(payload)
		if got := validateTeamsNumericPayload(plan, result, raw) == nil; got != want {
			t.Fatalf("payload validity = %v, want %v", got, want)
		}
	}
	check(true)
	raw, _ := json.Marshal(payload)
	alias := bytes.Replace(raw, []byte(`"caveats":[]`), []byte(`"Caveats":["hidden"],"caveats":[]`), 1)
	if bytes.Equal(alias, raw) || validateTeamsNumericPayload(plan, result, alias) == nil {
		t.Fatal("case alias in payload accepted")
	}
	payload.Rows[0].Values["total"] = json.RawMessage(`null`)
	check(false)
	payload.Rows[0].Values["total"] = json.RawMessage(`0`)
	payload.Coverage.Caveats = []string{"untrusted content"}
	check(false)
}
