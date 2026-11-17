package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

// This peer is deliberately synthetic: it checks the public wire, ownership
// and local consent path without contacting a hosted service or real library.
func TestTeamsSyntheticNumericFederation(t *testing.T) {
	ctx := context.Background()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := "00000000-0000-4000-8000-000000000003"
	ids := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}
	versionID := "3ed716e3-fca9-4299-b5f9-9f0d6c880fe0"
	config, _ := json.Marshal(map[string]any{"approval_policy": "metrics_or_review", "question": "Synthetic tokens", "requester": map[string]any{"id": "person", "display_name": "Synthetic"}, "audiences": map[string]any{"response_readers": []any{map[string]any{"kind": "person", "id": "person"}}, "publications": []any{}, "exports": []any{}}, "min_app_version": "1.0.0", "retention_policy": map[string]any{"mode": "organization_controlled", "duration_days": nil, "policy_version": 1}, "query_url": teamsExampleQuery, "result_schema": map[string]any{"columns": []any{map[string]any{"id": "total_tokens", "type": "integer", "role": "metric", "merge": "sum", "metric_id": "captured_tokens", "unit": "tokens"}}}})
	works := make([]teams.Work, 2)
	catalogs := make([]*Catalog, 2)
	descriptors := make([]LibraryDescriptor, 2)
	snapshots := make([]teamsSourceSnapshot, 2)
	prepared := make([]teamsPreparedManifest, 2)
	sourceKey := make([]byte, 32)
	policy := teams.MetricsDefault{Value: true, PolicyVersion: 1}
	for i := range works {
		c, _ := testCatalog(t)
		catalogs[i] = c
		d := LibraryDescriptor{ID: []string{"library-a", "library-b"}[i], Instance: []string{"instance-a", "instance-b"}[i]}
		descriptors[i] = d
		execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','synthetic-account','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','codex','synthetic-account','synthetic-source')`, `INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('s','w','c','main','root','codex')`, `INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,total_tokens) VALUES('s','2026-01-01T00:00:00Z','m','','request',123)`)
		if i == 1 {
			if _, err = c.setTeamsPrivate(ctx, "c", true); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = c.setTeamsLocalSettings(ctx, d.ID, d.Instance, "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), true, false, 1); err != nil {
			t.Fatal(err)
		}
		works[i] = teams.Work{ID: ids[i], RequestID: "ec6d6104-a140-4bc3-a81d-d7ba9ac76d82", VersionID: versionID, LibraryID: d.ID, Kind: "deterministic", Config: config, ConfigSHA256: teamsPayloadHash(config), Period: json.RawMessage(`{"key":"once"}`), PopulationKey: "current", ExpiresAt: time.Now().Add(time.Hour), EvaluationRevision: 1}
		snapshots[i], err = c.teamsSourceSnapshot(ctx, sourceKey, teamsSyntheticResolver)
		if err != nil {
			t.Fatal(err)
		}
		prepared[i], err = c.prepareTeamsManifest(ctx, works[i], d, snapshots[i], policy)
		if err != nil {
			t.Fatal(err)
		}
	}
	if snapshots[0].Entries[0].Token != snapshots[1].Entries[0].Token {
		t.Fatal("overlap token changed")
	}
	var mu sync.Mutex
	owner := ""
	var posted int64
	var savedReceipt teams.Receipt
	var cancelled atomic.Bool
	var loseResponse atomic.Bool
	var receiptAbsent atomic.Bool
	var sourceRegistrations atomic.Int64
	var firstSubmission []byte
	nonces := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		nonce := r.Header.Get("X-Pharos-Nonce")
		sig, e := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		digest := teamsPayloadHash(raw)
		message := strings.Join([]string{"pharos-device-v1", deviceID, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), nonce, digest}, "\n")
		mu.Lock()
		defer mu.Unlock()
		if e != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(message), sig) || nonces[nonce] {
			http.Error(w, "bad signature", 401)
			return
		}
		nonces[nonce] = true
		switch {
		case r.URL.Path == "/v1/device/poll":
			cancellations := []teams.Cancellation{}
			if cancelled.Load() {
				cancellations = append(cancellations, teams.Cancellation{WorkID: ids[0], CancelledAt: time.Now()})
			}
			json.NewEncoder(w).Encode(teams.PollResponse{ServerTime: time.Now(), Work: works, Cancellations: cancellations, MetricsDefault: policy, NextPollSeconds: 900})
		case strings.HasSuffix(r.URL.Path, "/sources"):
			sourceRegistrations.Add(1)
			var manifest teams.Manifest
			if json.Unmarshal(raw, &manifest) != nil || strings.Contains(string(raw), "synthetic-account") || strings.Contains(string(raw), "synthetic-source") {
				t.Error("manifest exposed identity or malformed")
			}
			_, hash, e := manifest.Bytes()
			if e != nil {
				t.Error(e)
			}
			workID := strings.Split(r.URL.Path, "/")[4]
			if owner == "" {
				owner = workID
			}
			assigned := []string{}
			if owner == workID {
				assigned = append(assigned, manifest.SourceManifest[0].Token)
			}
			json.NewEncoder(w).Encode(teams.SourcesResponse{ManifestSHA256: hash, OwnershipRevision: 1, OwnedSourceTokens: assigned})
		case strings.HasSuffix(r.URL.Path, "/privacy"):
			json.NewEncoder(w).Encode(teams.PrivacySnapshot{PrivacyRevision: 2, PrivateSourceTokens: []string{snapshots[0].Entries[0].Token}})
		case strings.HasSuffix(r.URL.Path, "/result"):
			if firstSubmission == nil {
				firstSubmission = append([]byte(nil), raw...)
			} else if !bytes.Equal(firstSubmission, raw) {
				t.Error("retry changed submitted bytes")
			}
			var submission teams.Submission
			var p teams.Payload
			if json.Unmarshal(raw, &submission) != nil || json.Unmarshal(submission.Payload, &p) != nil || submission.PayloadSHA256 != teamsPayloadHash(submission.Payload) || submission.Approval.Mode != "automatic_metrics" || submission.Approval.OrgPolicyVersion != 1 || submission.Approval.SharedPrivacyRevision != 2 {
				t.Error("invalid result")
			}
			if len(p.Rows) != 1 || json.Unmarshal(p.Rows[0].Values["total_tokens"], &posted) != nil {
				t.Error("missing metric")
			}
			savedReceipt = teams.Receipt{ID: "33333333-3333-4333-8333-333333333333", WorkID: ids[0], PayloadSHA256: submission.PayloadSHA256, SubmissionSHA256: teamsPayloadHash(raw), IdempotencyKey: submission.IdempotencyKey, InstallationID: deviceID, ResultRevision: 1, AcceptedAt: time.Now().UTC(), Outcome: "completed"}
			if loseResponse.Load() {
				http.Error(w, "synthetic lost response", http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode(savedReceipt)
		case strings.HasSuffix(r.URL.Path, "/receipt"):
			if receiptAbsent.Swap(false) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":"receipt_not_found"}}`))
				return
			}
			json.NewEncoder(w).Encode(savedReceipt)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := teams.NewDeviceClient(server.URL, deviceID, key, true)
	if err != nil {
		t.Fatal(err)
	}
	poll, err := client.Poll(ctx, teams.PollRequest{AppVersion: "1.0.0", LibraryIDs: []string{"library-a", "library-b"}})
	if err != nil || len(poll.Work) != 2 {
		t.Fatalf("poll: %v", err)
	}
	assigned := make([]teams.SourcesResponse, 2)
	cancelled.Store(true)
	if _, _, err = catalogs[0].registerTeamsManifest(ctx, client, descriptors[0], snapshots[0], works[0], "1.0.0"); err == nil || sourceRegistrations.Load() != 0 {
		t.Fatal("cancelled work disclosed a source manifest")
	}
	cancelled.Store(false)
	for i := range assigned {
		prepared[i], assigned[i], err = catalogs[i].registerTeamsManifest(ctx, client, descriptors[i], snapshots[i], works[i], "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(assigned[0].OwnedSourceTokens) != 1 || len(assigned[1].OwnedSourceTokens) != 0 {
		t.Fatalf("source ownership: %+v", assigned)
	}
	shared, err := client.Privacy(ctx, ids[0])
	if err != nil || shared.PrivacyRevision != 2 || len(shared.PrivateSourceTokens) != 1 {
		t.Fatalf("privacy: %+v %v", shared, err)
	}
	plan, err := teamsPlanFromWork(poll.Work[0])
	if err != nil {
		t.Fatal(err)
	}
	registration, err := newTeamsRegistration(prepared[0].Manifest, assigned[0], shared)
	if err != nil {
		t.Fatal(err)
	}
	result, err := catalogs[0].teamsExecuteNumeric(ctx, plan, sourceKey, teamsSyntheticResolver, snapshots[0], registration)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(teams.Payload{Outcome: "completed", Form: "snapshot", ObservedAt: time.Now().UTC(), ComputedAt: time.Now().UTC(), Coverage: teams.Coverage{SourceState: "complete", Measurement: "measured", Caveats: []string{}}, Rows: []teams.Row{{Key: []json.RawMessage{}, Values: map[string]json.RawMessage{"total_tokens": json.RawMessage(`123`)}}}, DeleteKeys: [][]json.RawMessage{}})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := catalogs[0].stageTeamsNumeric(ctx, works[0], descriptors[0], snapshots[0], result, prepared[0], "8134b012-9ab5-4dd4-b3ec-1d5f9e474de7", payload)
	if err != nil || staged.State != "approved" {
		t.Fatalf("stage: %v", err)
	}
	cancelled.Store(true)
	if err = catalogs[0].sendTeamsApproved(ctx, client, descriptors[0], "synthetic-org", "00000000-0000-4000-8000-000000000003", "1.0.0", ids[0]); err == nil || posted != 0 {
		t.Fatal("cancelled work was sent")
	}
	cancelled.Store(false)
	loseResponse.Store(true)
	if err = catalogs[0].sendTeamsApproved(ctx, client, descriptors[0], "synthetic-org", "00000000-0000-4000-8000-000000000003", "1.0.0", ids[0]); err == nil {
		t.Fatal("lost response appeared successful")
	}
	if err = catalogs[0].sendTeamsApproved(ctx, client, descriptors[0], "synthetic-org", "00000000-0000-4000-8000-000000000003", "1.0.0", ids[0]); err == nil {
		t.Fatal("uncertain result was re-emitted")
	}
	loseResponse.Store(false)
	receiptAbsent.Store(true)
	if err = catalogs[0].retryTeamsUncertain(ctx, client, descriptors[0], "synthetic-org", "00000000-0000-4000-8000-000000000003", "1.0.0", ids[0]); err != nil {
		t.Fatal(err)
	}
	if posted != 123 {
		t.Fatalf("duplicate or missing token count: %d", posted)
	}
	var logged []byte
	var sent teams.Submission
	if err = catalogs[0].DB.QueryRow(`SELECT submission FROM teams_send_log WHERE work_id=?`, ids[0]).Scan(&logged); err != nil || json.Unmarshal(logged, &sent) != nil || string(sent.Payload) != string(payload) || sent.PayloadSHA256 != staged.PayloadHash {
		t.Fatalf("sent log: %v", err)
	}
}
