package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	proofManifest := teams.CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}}
	upload, err := c.prepareTeamsV2Upload(ctx, w, d, credential, merged, policy, teams.SourceWatermark{}, proofManifest)
	if err != nil {
		lease.Close()
		t.Fatal(err)
	}
	if upload.Mode != "automatic_metrics" || countRows(t, c, `SELECT COUNT(*) FROM teams_source_mapping_exposure`) != 2 {
		lease.Close()
		t.Fatal("source exposures were not reserved")
	}
	replay, err := c.prepareTeamsV2Upload(ctx, w, d, credential, merged, policy, teams.SourceWatermark{LocalRevision: upload.LocalRevision, ReservedInventoryRevision: upload.InventoryRevision, ReservedInventorySHA256: upload.InventoryHash, AcceptedInventoryRevision: upload.InventoryRevision, AcceptedInventorySHA256: upload.InventoryHash}, proofManifest)
	lease.Close()
	if err != nil || replay.SnapshotID != upload.SnapshotID || string(replay.Seal) != string(upload.Seal) {
		t.Fatalf("uncertain upload changed bytes: %v", err)
	}
	sealedState := false
	var accepted teams.Receipt
	var firstSubmission []byte
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
			if firstSubmission == nil {
				firstSubmission = append([]byte(nil), body...)
				http.Error(wr, "synthetic interrupted delivery", http.StatusServiceUnavailable)
				return
			}
			if !bytes.Equal(firstSubmission, body) {
				t.Error("retry changed reviewed submission bytes")
			}
			var submitted teams.Submission
			if json.Unmarshal(body, &submitted) != nil {
				t.Error("submission")
				return
			}
			accepted = teams.Receipt{ID: w.ID, WorkID: w.ID, PayloadSHA256: submitted.PayloadSHA256, SubmissionSHA256: teamsPayloadHash(body), IdempotencyKey: submitted.IdempotencyKey, InstallationID: credential.InstallationID, ResultRevision: 1, AcceptedAt: time.Now().UTC(), Outcome: "completed", SourceBindingID: submitted.SourceBindingID, AssignmentSHA256: submitted.AssignmentSHA256, InventoryRevision: submitted.InventoryRevision, CatalogRevision: submitted.CatalogRevision, ProjectionPending: true, PublicationState: "staged"}
			json.NewEncoder(wr).Encode(accepted)
		case strings.HasSuffix(r.URL.Path, "/receipt"):
			if accepted.ID == "" {
				wr.Header().Set("Content-Type", "application/json")
				wr.WriteHeader(http.StatusNotFound)
				_, _ = wr.Write([]byte(`{"error":{"code":"receipt_not_found"}}`))
				return
			}
			accepted.PublicationState = "published"
			json.NewEncoder(wr).Encode(accepted)
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
	override(t, &teamsLoadSourceConfig, func(string) (Config, error) { return cfg, nil })
	service := testServer(t, cfg, c)
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatalf("lifecycle check: %v", err)
	}
	interrupted, err := readTeamsV2Draft(ctx, c.reader(), w.ID)
	if err != nil || interrupted.State != "needs_reconcile" || !bytes.Equal(interrupted.Submission, firstSubmission) {
		t.Fatalf("interrupted submission was not retained: %v", err)
	}
	var reviewed teams.Submission
	if json.Unmarshal(firstSubmission, &reviewed) != nil || reviewed.ConfigSHA256 != w.ConfigSHA256 || reviewed.PayloadSHA256 != interrupted.PayloadHash || !bytes.Equal(reviewed.Payload, interrupted.Payload) {
		t.Fatal("submitted bytes differ from the reviewed configuration or payload")
	}
	if _, err = c.sendTeamsV2Approved(ctx, cfg, client, d, credential, w.ID, manifest); err != nil {
		t.Fatalf("retry after missing receipt: %v", err)
	}
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatalf("refresh after retry: %v", err)
	}
	service.teamsWorker.mu.Lock()
	workerItem := service.teamsWorker.items[w.ID]
	service.teamsWorker.mu.Unlock()
	if workerItem.State != "published" {
		t.Fatalf("worker lost publication state: %q", workerItem.State)
	}
	if _, err := c.registerTeamsV2(ctx, cfg, client, d, credential, w, merged, manifest); !errors.Is(err, errTeamsSnapshotChanged) {
		t.Fatalf("registration replaced a persisted draft: %v", err)
	}
	draft, err := readTeamsV2Draft(ctx, c.reader(), w.ID)
	if err != nil || draft.State != "accepted" || !strings.Contains(string(draft.Payload), `"total_tokens":123`) {
		t.Fatalf("draft: %v", err)
	}
	// Offline non-series actions must reject even when the underlying work and
	// draft are otherwise valid for the same live account.
	if _, err = c.DB.Exec(`UPDATE teams_v2_results SET state='awaiting_review' WHERE work_id=?`, w.ID); err != nil {
		t.Fatal(err)
	}
	currentSettings, err := readTeamsSettings(ctx, c.reader())
	if err != nil {
		t.Fatal(err)
	}
	service.teamsWorker.mu.Lock()
	service.teamsWorker.settings = currentSettings
	service.teamsWorker.settingsValid = true
	service.teamsWorker.viewScope = teamsCredentialScope(d, credential)
	itemOffline := service.teamsWorker.items[w.ID]
	itemOffline.Work = w
	itemOffline.State = "awaiting_source_consent"
	service.teamsWorker.items[w.ID] = itemOffline
	service.teamsWorker.scope = ""
	service.teamsWorker.mu.Unlock()
	offlineSource, _ := json.Marshal(map[string]any{"workId": w.ID, "settingsRevision": currentSettings.SettingsRevision})
	recOffline := httptest.NewRecorder()
	service.teamsWorkerHTTP(recOffline, httptest.NewRequest(http.MethodPost, "/api/teams/worker/allow-source", bytes.NewReader(offlineSource)))
	if recOffline.Code == http.StatusOK {
		t.Fatal("offline source consent accepted")
	}
	service.teamsWorker.mu.Lock()
	itemOffline.State = "awaiting_review"
	service.teamsWorker.items[w.ID] = itemOffline
	service.teamsWorker.mu.Unlock()
	offlineApprove, _ := json.Marshal(map[string]any{"workId": w.ID, "payloadHash": draft.PayloadHash})
	recOffline = httptest.NewRecorder()
	service.teamsWorkerHTTP(recOffline, httptest.NewRequest(http.MethodPost, "/api/teams/worker/approve-result", bytes.NewReader(offlineApprove)))
	if recOffline.Code == http.StatusOK {
		t.Fatal("offline result approval accepted")
	}
	var afterOffline string
	if err = c.DB.QueryRow(`SELECT state FROM teams_v2_results WHERE work_id=?`, w.ID).Scan(&afterOffline); err != nil || afterOffline != "awaiting_review" {
		t.Fatalf("offline approval changed draft: %q %v", afterOffline, err)
	}
	var viewSource, viewPrivacy int64
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&viewSource); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&viewPrivacy); err != nil {
		t.Fatal(err)
	}
	viewGen, err := readCacheGenerations(ctx, c.reader())
	if err != nil {
		t.Fatal(err)
	}
	viewKey, err := service.teamsWorkViewBinding(ctx, teamsCredentialScope(d, credential), w, currentSettings, viewSource, viewPrivacy, viewGen.all)
	if err != nil {
		t.Fatal(err)
	}
	service.teamsWorker.mu.Lock()
	service.teamsWorker.bindings = map[string]string{w.ID: viewKey}
	service.teamsWorker.scope = teamsCredentialScope(d, credential)
	itemOffline.State = "awaiting_source_consent"
	service.teamsWorker.items[w.ID] = itemOffline
	service.teamsWorker.mu.Unlock()
	// A privacy change after a card was prepared invalidates both foreground actions.
	if _, err = c.DB.Exec(`UPDATE teams_privacy_revision SET revision=revision+1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	staleGrant := httptest.NewRecorder()
	service.teamsWorkerHTTP(staleGrant, httptest.NewRequest(http.MethodPost, "/api/teams/worker/allow-source", bytes.NewReader(offlineSource)))
	if staleGrant.Code == http.StatusOK {
		t.Fatal("stale privacy source consent accepted")
	}
	service.teamsWorker.mu.Lock()
	itemOffline.State = "awaiting_review"
	service.teamsWorker.items[w.ID] = itemOffline
	service.teamsWorker.mu.Unlock()
	staleApproval := httptest.NewRecorder()
	service.teamsWorkerHTTP(staleApproval, httptest.NewRequest(http.MethodPost, "/api/teams/worker/approve-result", bytes.NewReader(offlineApprove)))
	if staleApproval.Code == http.StatusOK {
		t.Fatal("stale privacy approval accepted")
	}
	if _, err = c.DB.Exec(`UPDATE teams_privacy_revision SET revision=? WHERE id=1`, viewPrivacy); err != nil {
		t.Fatal(err)
	}
	service.teamsWorker.mu.Lock()
	itemOffline.State = "awaiting_source_consent"
	service.teamsWorker.items[w.ID] = itemOffline
	service.teamsWorker.mu.Unlock()
	liveSource := httptest.NewRecorder()
	service.teamsWorkerHTTP(liveSource, httptest.NewRequest(http.MethodPost, "/api/teams/worker/allow-source", bytes.NewReader(offlineSource)))
	if liveSource.Code != http.StatusOK {
		t.Fatalf("live source consent denied: %d %s", liveSource.Code, liveSource.Body.String())
	}
	if _, err = c.DB.Exec(`DELETE FROM teams_request_grants WHERE version_id=?`, w.VersionID); err != nil {
		t.Fatal(err)
	}
	service.teamsWorker.mu.Lock()
	itemOffline.State = "awaiting_review"
	service.teamsWorker.items[w.ID] = itemOffline
	service.teamsWorker.mu.Unlock()
	liveApproval := httptest.NewRecorder()
	service.teamsWorkerHTTP(liveApproval, httptest.NewRequest(http.MethodPost, "/api/teams/worker/approve-result", bytes.NewReader(offlineApprove)))
	if liveApproval.Code != http.StatusOK {
		t.Fatalf("live approval denied: %d %s", liveApproval.Code, liveApproval.Body.String())
	}
	if _, err = c.DB.Exec(`UPDATE teams_v2_results SET state='accepted' WHERE work_id=?`, w.ID); err != nil {
		t.Fatal(err)
	}
	// Both real review handlers must reject stale displayed series configs.
	seriesConfig, err := os.ReadFile("testdata/teams-report-series/manual_series_config.json")
	if err != nil {
		t.Fatal(err)
	}
	seriesHash := teamsPayloadHash(seriesConfig)
	if _, err = c.DB.Exec(`UPDATE teams_v2_results SET config=?,state='awaiting_review' WHERE work_id=?`, seriesConfig, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`UPDATE teams_v2_uploads SET config_sha256=? WHERE work_id=?`, seriesHash, w.ID); err != nil {
		t.Fatal(err)
	}
	seriesWork := w
	seriesWork.Config, seriesWork.ConfigSHA256 = seriesConfig, seriesHash
	service.teamsWorker.mu.Lock()
	item := service.teamsWorker.items[w.ID]
	item.Work, item.State = seriesWork, "awaiting_source_consent"
	service.teamsWorker.items[w.ID] = item
	service.teamsWorker.mu.Unlock()
	missingSource, _ := json.Marshal(map[string]any{"workId": w.ID, "settingsRevision": s.SettingsRevision, "configHash": seriesHash})
	missingRec := httptest.NewRecorder()
	service.teamsWorkerHTTP(missingRec, httptest.NewRequest(http.MethodPost, "/api/teams/worker/allow-source", bytes.NewReader(missingSource)))
	if missingRec.Code != http.StatusConflict {
		t.Fatal("source consent accepted missing labels")
	}
	service.teamsWorker.mu.Lock()
	item = service.teamsWorker.items[w.ID]
	item.Labels = &teams.ReaderLabels{Version: 1, ConfigSHA256: seriesHash, Entries: []teams.ReaderLabel{{PersonID: "0b000000-0000-4000-8000-000000000001", Status: "active", DisplayName: "Reader"}, {PersonID: "0c000000-0000-4000-8000-000000000001", Status: "active", DisplayName: "Synthetic"}}}
	item.LabelsAt = time.Now()
	item.LabelsScope = teamsCredentialScope(d, credential)
	service.teamsWorker.scope = item.LabelsScope
	service.teamsWorker.items[w.ID] = item
	if teamsReaderLabelToken(item, item.LabelsScope, service.teamsWorker.generation) == "" {
		t.Fatal("valid source labels not ready")
	}
	service.teamsWorker.mu.Unlock()
	staleSource, _ := json.Marshal(map[string]any{"workId": w.ID, "settingsRevision": s.SettingsRevision, "configHash": seriesHash, "labelsToken": "stale"})
	staleRec := httptest.NewRecorder()
	service.teamsWorkerHTTP(staleRec, httptest.NewRequest(http.MethodPost, "/api/teams/worker/allow-source", bytes.NewReader(staleSource)))
	if staleRec.Code != http.StatusConflict {
		t.Fatal("source consent accepted stale labels")
	}
	for _, path := range []string{"/api/teams/worker/allow-source", "/api/teams/worker/approve-result"} {
		for _, hash := range []string{"", strings.Repeat("0", 64), teamsPayloadHash(config)} {
			body, _ := json.Marshal(map[string]any{"workId": w.ID, "settingsRevision": s.SettingsRevision, "payloadHash": draft.PayloadHash, "configHash": hash})
			if strings.HasSuffix(path, "allow-source") {
				body, _ = json.Marshal(map[string]any{"workId": w.ID, "settingsRevision": s.SettingsRevision, "configHash": hash})
			} else {
				body, _ = json.Marshal(map[string]any{"workId": w.ID, "payloadHash": draft.PayloadHash, "configHash": hash})
			}
			rec := httptest.NewRecorder()
			service.teamsWorkerHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
			if rec.Code != http.StatusConflict {
				t.Fatalf("%s accepted stale config: %d", path, rec.Code)
			}
		}
	}
	if err = c.reviewTeamsV2Draft(ctx, d, w.ID, draft.PayloadHash); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("legacy review accepted series: %v", err)
	}
	service.teamsWorker.mu.Lock()
	item = service.teamsWorker.items[w.ID]
	item.State = "awaiting_review"
	item.Labels = nil
	item.LabelsAt = time.Time{}
	item.LabelsScope = ""
	service.teamsWorker.scope = teamsCredentialScope(d, credential)
	service.teamsWorker.items[w.ID] = item
	service.teamsWorker.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"workId": w.ID, "payloadHash": draft.PayloadHash, "configHash": seriesHash})
	rec := httptest.NewRecorder()
	service.teamsWorkerHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/teams/worker/approve-result", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("missing reader labels accepted: %d", rec.Code)
	}
	service.teamsWorker.mu.Lock()
	item = service.teamsWorker.items[w.ID]
	item.State = "awaiting_review"
	item.Labels = &teams.ReaderLabels{Version: 1, ConfigSHA256: seriesHash, Entries: []teams.ReaderLabel{{PersonID: "0b000000-0000-4000-8000-000000000001", Status: "active", DisplayName: "Reader"}, {PersonID: "0c000000-0000-4000-8000-000000000001", Status: "active", DisplayName: "Synthetic"}}}
	item.LabelsAt = time.Now()
	item.LabelsScope = teamsCredentialScope(d, credential)
	service.teamsWorker.scope = item.LabelsScope
	service.teamsWorker.items[w.ID] = item
	token := teamsReaderLabelToken(item, item.LabelsScope, service.teamsWorker.generation)
	service.teamsWorker.mu.Unlock()
	wrong, _ := json.Marshal(map[string]any{"workId": w.ID, "configHash": strings.Repeat("0", 64)})
	denied := httptest.NewRecorder()
	service.teamsWorkerHTTP(denied, httptest.NewRequest(http.MethodPost, "/api/teams/worker/reader-labels", bytes.NewReader(wrong)))
	if denied.Code != http.StatusConflict {
		t.Fatalf("stale lookup accepted: %d", denied.Code)
	}
	service.teamsWorker.mu.Lock()
	unchanged := teamsReaderLabelToken(service.teamsWorker.items[w.ID], teamsCredentialScope(d, credential), service.teamsWorker.generation)
	service.teamsWorker.mu.Unlock()
	if unchanged != token {
		t.Fatal("stale lookup erased current labels")
	}
	service.teamsWorker.mu.Lock()
	item = service.teamsWorker.items[w.ID]
	item.Labels.Entries[0].DisplayName = "Renamed"
	service.teamsWorker.items[w.ID] = item
	service.teamsWorker.mu.Unlock()
	staleBody, _ := json.Marshal(map[string]any{"workId": w.ID, "payloadHash": draft.PayloadHash, "configHash": seriesHash, "labelsToken": token})
	denied = httptest.NewRecorder()
	service.teamsWorkerHTTP(denied, httptest.NewRequest(http.MethodPost, "/api/teams/worker/approve-result", bytes.NewReader(staleBody)))
	if denied.Code != http.StatusConflict {
		t.Fatal("stale displayed names accepted")
	}
	service.teamsWorker.mu.Lock()
	item = service.teamsWorker.items[w.ID]
	token = teamsReaderLabelToken(item, teamsCredentialScope(d, credential), service.teamsWorker.generation)
	service.teamsWorker.mu.Unlock()
	omitted, _ := json.Marshal(map[string]any{"workId": w.ID, "payloadHash": draft.PayloadHash, "labelsToken": token})
	denied = httptest.NewRecorder()
	service.teamsWorkerHTTP(denied, httptest.NewRequest(http.MethodPost, "/api/teams/worker/approve-result", bytes.NewReader(omitted)))
	if denied.Code != http.StatusConflict {
		t.Fatal("series approval accepted without config hash")
	}
	currentSettings, err = readTeamsSettings(ctx, c.reader())
	if err != nil {
		t.Fatal(err)
	}
	viewGen, err = readCacheGenerations(ctx, c.reader())
	if err != nil {
		t.Fatal(err)
	}
	viewKey, err = service.teamsWorkViewBinding(ctx, teamsCredentialScope(d, credential), seriesWork, currentSettings, viewSource, viewPrivacy, viewGen.all)
	if err != nil {
		t.Fatal(err)
	}
	service.teamsWorker.mu.Lock()
	service.teamsWorker.bindings[w.ID] = viewKey
	service.teamsWorker.mu.Unlock()
	body, _ = json.Marshal(map[string]any{"workId": w.ID, "payloadHash": draft.PayloadHash, "configHash": seriesHash, "labelsToken": token})
	rec = httptest.NewRecorder()
	service.teamsWorkerHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/teams/worker/approve-result", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("matching series review was rejected: %d %s", rec.Code, rec.Body.String())
	}
	if _, err = c.DB.Exec(`UPDATE teams_v2_results SET config=?,state='accepted' WHERE work_id=?`, config, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`UPDATE teams_v2_uploads SET config_sha256=? WHERE work_id=?`, w.ConfigSHA256, w.ID); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the server accepted but before the receipt was
	// stored, followed by an unrelated source update and a damaged/replaced
	// registration. Reconciliation uses the original signed submission.
	execTx(t, c.DB, `UPDATE teams_v2_results SET state='needs_reconcile',receipt_json=''`, `DELETE FROM teams_send_log`, `UPDATE teams_v2_uploads SET state='sealed',binding_json='{}'`, `UPDATE teams_source_revision SET revision=revision+1 WHERE id=1`)
	receipt, err := c.reconcileTeamsV2Receipt(ctx, client, d, credential, w.ID)
	if err != nil || receipt.PublicationState != "published" {
		t.Fatalf("receipt recovery: %+v %v", receipt, err)
	}
	if _, err = c.reconcileTeamsV2Receipt(ctx, client, d, credential, w.ID); err != nil {
		t.Fatalf("receipt refresh: %v", err)
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
