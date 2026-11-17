package archive

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func TestTeamsV2InventoryWithholdsStaleEvidenceAndReportsPartial(t *testing.T) {
	useHost(t, "synthetic-host")
	support := t.TempDir()
	t.Setenv("PHAROS_SUPPORT_DIR", support)
	c, cfg := testCatalog(t)
	ctx := context.Background()
	cfg.Application = filepath.Join(support, "Synthetic.app")
	d, err := ensureLibraryDescriptor(cfg, "Synthetic library")
	if err != nil {
		t.Fatal(err)
	}
	key, err := teamsInstallationKey(cfg, "https://synthetic.invalid", true)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	appID := installationID(cfg.Application)
	raw, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://synthetic.invalid", "org_id": "00000000-0000-4000-8000-000000000001", "person_id": "00000000-0000-4000-8000-000000000002", "installation_id": "00000000-0000-4000-8000-000000000003", "library_id": d.ID, "library_instance": d.Instance, "application_id": appID, "private_key": base64.RawURLEncoding.EncodeToString(key), "source_token_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "metrics_default": map[string]any{"value": false, "policy_version": 1}})
	credential, err := teams.ParseCredential(raw, d.ID, d.Instance, appID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	if err = c.saveTeamsCredential(ctx, cfg, d, credential); err != nil {
		t.Fatal(err)
	}
	s, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, credential.OrgID, credential.InstallationID, teamsPayloadHash(key.Public().(ed25519.PublicKey)), true, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(ctx, d, s.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "codex")
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	data := codexLines("synthetic-thread", 1) + `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":123},"last_token_usage":{"total_tokens":123}}}}` + "\n"
	writeSourceFile(t, file, data)
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	cfg.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	var binding string
	var revision int64
	if err = c.DB.QueryRow(`SELECT binding_id FROM teams_source_bindings WHERE superseded_at=''`).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err = c.reviewTeamsSourceMapping(ctx, d, credential, binding, "work", revision, true); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.teamsObserveV2Codex(ctx, cfg, d, credential)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Complete || len(snapshot.Items) != 1 || snapshot.Items[0].Protection != "unknown" || snapshot.values[snapshot.Items[0].Token] != 123 {
		t.Fatalf("verified subset: %+v", snapshot)
	}
	otherRoot := filepath.Join(t.TempDir(), "other-codex")
	otherFile := filepath.Join(otherRoot, "sessions", "other.jsonl")
	writeSourceFile(t, otherFile, data)
	other := SourceConfig{Name: "other-codex", Kind: "codex", Path: otherRoot, Account: "other-label", Enabled: true}
	cfg.Sources = append(cfg.Sources, other)
	ingestSource(t, c, other)
	if err = c.DB.QueryRow(`SELECT binding_id FROM teams_source_bindings WHERE source_name=? AND superseded_at=''`, other.Name).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err = c.reviewTeamsSourceMapping(ctx, d, credential, binding, "work", revision, true); err != nil {
		t.Fatal(err)
	}
	var privateConversation string
	if err = c.DB.QueryRow(`SELECT id FROM conversations WHERE account='other-label' AND native_id='synthetic-thread'`).Scan(&privateConversation); err != nil {
		t.Fatal(err)
	}
	if _, err = c.setTeamsPrivate(ctx, privateConversation, true); err != nil {
		t.Fatal(err)
	}
	merged, err := c.teamsObserveV2Codex(ctx, cfg, d, credential)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Items) != 1 || merged.Items[0].Protection != "private" || merged.values[merged.Items[0].Token] != 123 {
		t.Fatalf("duplicate copies did not deduplicate/protect: %+v", merged)
	}
	config, _ := json.Marshal(map[string]any{"query_url": teamsExampleQuery, "approval_policy": "metrics_or_review", "question": "Synthetic tokens", "requester": map[string]any{"id": "person", "display_name": "Synthetic"}, "audiences": map[string]any{"response_readers": []any{map[string]any{"kind": "person", "id": "person"}}, "publications": []any{}, "exports": []any{}}, "min_app_version": "0.10.0", "retention_policy": map[string]any{"mode": "organization_controlled", "duration_days": nil, "policy_version": 1}, "result_schema": map[string]any{"columns": []any{map[string]any{"id": "total_tokens", "type": "integer", "role": "metric", "merge": "sum", "metric_id": "captured_tokens", "unit": "tokens"}}}})
	w := teams.Work{ID: "a4c09e42-d0a6-45f0-89ef-2a03b7cff630", VersionID: "3ed716e3-fca9-4299-b5f9-9f0d6c880fe0", LibraryID: d.ID, Kind: "deterministic", Period: json.RawMessage(`{"key":"once"}`), PopulationKey: "current", EvaluationRevision: 1, Config: config, ConfigSHA256: teamsPayloadHash(config), ExpiresAt: time.Now().Add(time.Hour)}
	lease, err := c.teamsSharingLease()
	if err != nil {
		t.Fatal(err)
	}
	policy := teams.MetricsDefault{Value: true, PolicyVersion: 1}
	upload, err := c.prepareTeamsV2Upload(ctx, w, d, credential, merged, policy, teams.SourceWatermark{}, "0.10.0")
	if err != nil {
		lease.Close()
		t.Fatal(err)
	}
	if upload.Mode != "automatic_metrics" || countRows(t, c, `SELECT COUNT(*) FROM teams_source_mapping_exposure`) != 2 {
		lease.Close()
		t.Fatal("source exposures were not reserved")
	}
	replay, err := c.prepareTeamsV2Upload(ctx, w, d, credential, merged, policy, teams.SourceWatermark{LocalRevision: upload.LocalRevision, ReservedInventoryRevision: upload.InventoryRevision, ReservedInventorySHA256: upload.InventoryHash, AcceptedInventoryRevision: upload.InventoryRevision, AcceptedInventorySHA256: upload.InventoryHash}, "0.10.0")
	lease.Close()
	if err != nil || replay.SnapshotID != upload.SnapshotID || string(replay.Seal) != string(upload.Seal) {
		t.Fatalf("uncertain upload changed bytes: %v", err)
	}
	sealedState := false
	server := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/v1/device/poll":
			json.NewEncoder(wr).Encode(teams.PollResponse{Work: []teams.Work{w}, Cancellations: []teams.Cancellation{}, MetricsDefault: policy})
		case strings.HasSuffix(r.URL.Path, "/source-watermark"):
			watermark := teams.SourceWatermark{}
			if sealedState {
				watermark = teams.SourceWatermark{LocalRevision: upload.LocalRevision, ReservedInventoryRevision: upload.InventoryRevision, ReservedInventorySHA256: upload.InventoryHash, AcceptedInventoryRevision: upload.InventoryRevision, AcceptedInventorySHA256: upload.InventoryHash}
			}
			json.NewEncoder(wr).Encode(watermark)
		case strings.HasSuffix(r.URL.Path, "/source-authorizations"):
			var authorization teams.SourceAuthorization
			if json.Unmarshal(body, &authorization) != nil {
				t.Error("authorization")
				return
			}
			now := time.Now().UTC()
			json.NewEncoder(wr).Encode(teams.SourceAuthorizationReceipt{SnapshotID: authorization.SnapshotID, AuthorizationSHA256: teamsPayloadHash(body), AcceptedAt: now, ExpiresAt: now.Add(time.Hour)})
		case strings.Contains(r.URL.Path, "/pages/"):
			page, e := sourcewire.DecodePage(body)
			if e != nil {
				t.Error(e)
				return
			}
			json.NewEncoder(wr).Encode(teams.SourcePageReceipt{SnapshotID: page.SnapshotID, Index: page.Index, PageSHA256: sourcewire.SHA256(body)})
		case strings.HasSuffix(r.URL.Path, "/seal"):
			seal, e := sourcewire.DecodeSeal(body)
			if e != nil {
				t.Error(e)
				return
			}
			sealedState = true
			json.NewEncoder(wr).Encode(teams.SourceSealReceipt{SnapshotID: seal.SnapshotID, InventoryRevision: seal.InventoryRevision, InventorySHA256: seal.InventorySHA256, LocalRevision: seal.LocalRevision, OwnershipRevision: 1, PrivacyRevision: 1})
		case strings.HasSuffix(r.URL.Path, "/source-bindings"):
			var request teams.SourceBindRequest
			if json.Unmarshal(body, &request) != nil {
				t.Error("bind")
				return
			}
			items := []sourcewire.Assignment{{Token: merged.Items[0].Token, Decision: "owned"}}
			h, _ := sourcewire.NewAssignmentHasher(sourcewire.AssignmentHeader{BindingID: request.BindingID, SnapshotID: request.SnapshotID, InventorySHA256: request.InventorySHA256, OwnershipRevision: 1, PrivacyRevision: 1})
			h.Add(items[0])
			digest, _, _ := h.Finish()
			json.NewEncoder(wr).Encode(teams.SourceBinding{SourceBindRequest: request, AssignmentSHA256: digest, AssignmentCount: 1, AssignmentPageCount: 1, BindingState: "ready", ProjectionState: "staged"})
		case strings.Contains(r.URL.Path, "/assignments/"):
			items := []sourcewire.Assignment{{Token: merged.Items[0].Token, Decision: "owned"}}
			h, _ := sourcewire.NewAssignmentHasher(sourcewire.AssignmentHeader{BindingID: upload.BindingID, SnapshotID: upload.SnapshotID, InventorySHA256: upload.InventoryHash, OwnershipRevision: 1, PrivacyRevision: 1})
			h.Add(items[0])
			digest, _, _ := h.Finish()
			json.NewEncoder(wr).Encode(map[string]any{"binding_id": upload.BindingID, "snapshot_id": upload.SnapshotID, "inventory_sha256": upload.InventoryHash, "ownership_revision": 1, "privacy_revision": 1, "assignment_sha256": digest, "index": 0, "page_count": 1, "items": items})
		case strings.HasSuffix(r.URL.Path, "/result"):
			var submitted teams.Submission
			if json.Unmarshal(body, &submitted) != nil {
				t.Error("submission")
				return
			}
			json.NewEncoder(wr).Encode(teams.Receipt{ID: w.ID, WorkID: w.ID, PayloadSHA256: submitted.PayloadSHA256, SubmissionSHA256: teamsPayloadHash(body), IdempotencyKey: submitted.IdempotencyKey, InstallationID: credential.InstallationID, ResultRevision: 1, AcceptedAt: time.Now().UTC(), Outcome: "completed", SourceBindingID: submitted.SourceBindingID, AssignmentSHA256: submitted.AssignmentSHA256, InventoryRevision: submitted.InventoryRevision, CatalogRevision: submitted.CatalogRevision, ProjectionPending: true, PublicationState: "staged"})
		default:
			http.NotFound(wr, r)
		}
	}))
	defer server.Close()
	client, err := teams.NewDeviceClient(server.URL, credential.InstallationID, key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Destroy()
	manifest := teams.CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}}
	override(t, &teamsWorkerCapabilities, func() teamsCapabilityManifest { return manifest })
	override(t, &teamsDeviceClient, func(_ string, installation string, signing ed25519.PrivateKey, _ bool) (*teams.DeviceClient, error) {
		return teams.NewDeviceClient(server.URL, installation, signing, true)
	})
	service := testServer(t, cfg, c)
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatalf("lifecycle check: %v", err)
	}
	service.teamsWorker.mu.Lock()
	workerItem := service.teamsWorker.items[w.ID]
	service.teamsWorker.mu.Unlock()
	if workerItem.State != "staged" {
		t.Fatalf("worker lost publication state: %q", workerItem.State)
	}
	registered, err := c.registerTeamsV2(ctx, client, d, credential, w, merged, manifest)
	if err != nil || registered.Upload.State != "bound" || len(registered.Assignments) != 1 {
		t.Fatalf("v2 registration: %+v %v", registered, err)
	}
	draft, err := c.stageTeamsV2Result(ctx, cfg, d, credential, registered)
	if err != nil || draft.State != "accepted" || !strings.Contains(string(draft.Payload), `"total_tokens":123`) {
		t.Fatalf("stage partial result %s %v", draft.Payload, err)
	}
	replayed, err := c.stageTeamsV2Result(ctx, cfg, d, credential, registered)
	if err != nil || replayed.PayloadHash != draft.PayloadHash || string(replayed.Payload) != string(draft.Payload) {
		t.Fatal("draft retry changed reviewed bytes", err)
	}
	receipt, err := c.sendTeamsV2Approved(ctx, cfg, client, d, credential, w.ID, manifest)
	if err != nil || receipt.PublicationState != "staged" || !receipt.ProjectionPending {
		t.Fatalf("synthetic v2 submit: %+v %v", receipt, err)
	}
	if n := countRows(t, c, `SELECT COUNT(*) FROM teams_send_log WHERE work_id=?`, w.ID); n != 1 {
		t.Fatal("accepted receipt not recorded")
	}
	cfg.Sources[0].Options = map[string]any{"changed": true}
	changed, err := c.teamsObserveV2Codex(ctx, cfg, d, credential)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.Items) != 1 || changed.Items[0].Protection != "unavailable" || len(changed.values) != 0 {
		t.Fatal("changed source configuration remained eligible")
	}
	cfg.Sources[0].Options = nil
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	stale, err := c.teamsObserveV2Codex(ctx, cfg, d, credential)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.Items) != 1 || stale.Items[0].Protection != "unavailable" || len(stale.values) != 0 || stale.Coverage.OverlapUnresolved == 0 {
		t.Fatalf("stale proof contributed: %+v", stale)
	}
}
