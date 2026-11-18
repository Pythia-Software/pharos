package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/platform/credentialstore"
	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// The census proof hook is supplied by the independent native evidence slice;
// this exercises the normal worker and signed wire while that proof is absent.
func TestTeamsWorkerNonPrivateReplayHoldsOnChangedEvidence(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "nonprivate-stale")
}
func TestTeamsWorkerRetainedBaselineSignedRecovery(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "retained-baseline")
}
func TestTeamsWorkerSourcePilotSignedOrigin(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "")
}
func TestTeamsWorkerSourcePilotMarkerCensusFailure(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "marker-census")
}
func TestTeamsWorkerSourcePilotMarkerCheckpointFailure(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "marker-checkpoint")
}
func TestTeamsWorkerSourcePilotSignedOriginPages(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, true, false, false, "")
}

func TestTeamsWorkerSourcePilotSignedOriginMalformedPage(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, true, true, false, "")
}

func TestTeamsWorkerSourcePilotSignedOriginFifthPage(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, true, false, true, "")
}

func TestTeamsWorkerSourcePilotSignedOriginAuthFault(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "auth")
}
func TestTeamsWorkerSourcePilotExpiredAuthorityParks(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "expired-authority")
}
func TestTeamsWorkerSourcePilotLostReceiptLookup(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "lost-lookup")
}
func TestTeamsWorkerSourcePilotSignedReceiptLookup(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "lookup")
}
func TestTeamsWorkerSourcePilotSignedOriginStale(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "stale")
}
func TestTeamsWorkerSourcePilotSignedOriginMultipageAfterExpiry(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "multi-replace")
}
func TestTeamsWorkerSourcePilotSignedOriginManyAfterExpiry(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "many")
}
func TestTeamsWorkerSourcePilotSignedOriginFirstExpired(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "first-expired")
}
func TestTeamsWorkerSourcePilotSignedOriginExpired(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, false, false, false, "expired")
}
func TestTeamsWorkerSourcePilotSignedOriginCapacity(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, true, false, false, "capacity")
}
func TestTeamsWorkerSourcePilotSignedOriginHeldFirst(t *testing.T) {
	runTeamsWorkerSourcePilotSignedOrigin(t, true, false, false, "held-first")
}
func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}
func runTeamsWorkerSourcePilotSignedOrigin(t *testing.T, multipage, malformed, fifth bool, mode string) {
	c, cfg := testCatalog(t)
	cfg.Application = filepath.Join(t.TempDir(), "Synthetic.app")
	ctx := context.Background()
	override(t, &teamsSourcePilotEnabled, false)
	if mode == "nonprivate-stale" {
		override(t, &teamsNonPrivateSourceProofEnabled, true)
	}
	d, err := ensureLibraryDescriptor(cfg, "Synthetic library")
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const org = "00000000-0000-4000-8000-000000000001"
	const person = "00000000-0000-4000-8000-000000000002"
	const installation = "00000000-0000-4000-8000-000000000003"
	var signedOrigins, originAttempts, starts int
	var firstOrigin, secondOrigin []byte
	var suffixCandidate sourcewire.Item
	var firstPages, secondPages int
	var lookups, resent int
	lookupMissing := true
	var duringLookup func()
	var unconfirmedPage []byte
	var requestRun string
	startBodies := map[string][]byte{}
	runGenerations := map[string]int64{}
	runHeld := map[string]bool{}
	safetyHeldOnce := false
	accepted := map[string]sourcewire.ProtectionObservation{}
	invalidateOnPage, invalidated := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		sum := sha256.Sum256(raw)
		msg := strings.Join([]string{"pharos-device-v1", installation, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), r.Header.Get("X-Pharos-Nonce"), hex.EncodeToString(sum[:])}, "\n")
		signature, e := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || r.Header.Get("X-Pharos-Installation") != installation || !ed25519.Verify(pub, []byte(msg), signature) {
			t.Error("invalid device signature")
			w.WriteHeader(400)
			return
		}
		now := time.Now().UTC().Unix()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/device/capabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{"manifest_schema_versions": []int{1}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/device/poll":
			_ = json.NewEncoder(w).Encode(teams.PollResponse{Work: []teams.Work{}, Cancellations: []teams.Cancellation{}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/source-recovery/start"):
			command, e := sourcewire.DecodeRecoveryStart(raw)
			if e != nil {
				t.Error(e)
				w.WriteHeader(400)
				return
			}
			starts++
			if old, ok := startBodies[command.StartID]; ok && !bytes.Equal(old, raw) {
				t.Error("recovery start retry changed bytes")
			}
			startBodies[command.StartID] = bytes.Clone(raw)
			if starts == 1 {
				// The server received the signed request but its reply was lost.
				if mode == "auth" {
					w.WriteHeader(http.StatusUnauthorized)
				} else {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
				return
			}
			if len(accepted) > 0 && !command.Restart {
				t.Error("new generation did not request restart")
			}
			runID := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(accepted)+6)
			generation := int64(len(accepted) + 1)
			status := "proof_required"
			held := false
			if mode == "retained-baseline" || mode == "nonprivate-stale" {
				runID = fmt.Sprintf("00000000-0000-4000-8000-%012d", starts+100)
				generation = int64(starts)
				if len(accepted) > 0 && !safetyHeldOnce {
					held = true
					status = "held"
					safetyHeldOnce = true
				}
			}
			runGenerations[runID] = generation
			runHeld[runID] = held
			_ = json.NewEncoder(w).Encode(sourcewire.RecoveryStartReceipt{Status: status, Held: held, RunID: runID, Generation: generation, ServerTime: now, ExpiresAt: now + 3600, UnacceptedFloor: now - 1, ExternalProofRequired: true})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/source-recovery/") && strings.HasSuffix(r.URL.Path, "/pages"):
			request, e := sourcewire.DecodeRecoveryPage(raw)
			if e != nil {
				t.Error(e)
				w.WriteHeader(400)
				return
			}
			if multipage {
				n := 502
				if fifth {
					n = 2000
				}
				values := make([]teamsRecoveryOrigin, n)
				field := func(kind string, i int) string {
					sum := sha256.Sum256([]byte(fmt.Sprintf("generated %s-%d", kind, i)))
					return base64.RawURLEncoding.EncodeToString(sum[:])
				}
				for i := range values {
					token := field("token", i)
					if !fifth {
						// Every generated token sorts before the local
						// candidate, putting its existing revision in page 2.
						b := make([]byte, 32)
						b[1] = 1
						b[29] = byte(i >> 16)
						b[30] = byte(i >> 8)
						b[31] = byte(i)
						token = base64.RawURLEncoding.EncodeToString(b)
					}
					values[i] = teamsRecoveryOrigin{Token: token, Witness: field("witness", i), Revision: 4, EvidenceRevision: field("evidence", i), Protection: "unknown", Classification: sourcewire.Classification{State: "unclassified"}}
				}
				if !fifth {
					values[len(values)-1] = teamsRecoveryOrigin{Token: suffixCandidate.Token, Witness: suffixCandidate.Witness, Revision: 4, EvidenceRevision: suffixCandidate.EvidenceRevision, Protection: "unknown", Classification: sourcewire.Classification{State: "unclassified"}}
				}
				sort.Slice(values, func(i, j int) bool { return values[i].Token < values[j].Token })
				cursor := ""
				if fifth {
					index := 0
					if request.Cursor == "" {
						firstPages++
					} else {
						secondPages++
						if _, e = fmt.Sscanf(request.Cursor, "cursor-%d", &index); e != nil || index < 1 || index > 3 {
							t.Error("unexpected fifth page request")
							w.WriteHeader(400)
							return
						}
					}
					cursor = fmt.Sprintf("cursor-%d", index+1)
					values = values[index*500 : (index+1)*500]
					items := []json.RawMessage{}
					for _, v := range values {
						body, _ := json.Marshal(v)
						items = append(items, body)
					}
					_ = json.NewEncoder(w).Encode(sourcewire.RecoveryPageReceipt{Status: "proof_required", RunID: request.RunID, Generation: 1, ServerTime: now, UnacceptedFloor: now - 1, ExternalProofRequired: true, Family: "origins", Items: items, NextCursor: cursor})
					return
				}
				switch request.Cursor {
				case "":
					firstPages++
					requestRun = request.RunID
					if mode == "capacity" && firstPages == 1 {
						_ = json.NewEncoder(w).Encode(sourcewire.RecoveryPageReceipt{Status: "capacity_blocked", RunID: request.RunID, Generation: 1, ServerTime: now, UnacceptedFloor: now - 1, ExternalProofRequired: true, Family: "origins", Items: []json.RawMessage{}})
						return
					}
					cursor = "opaque-synthetic-continuation"
					values = values[:500]
				case "opaque-synthetic-continuation":
					secondPages++
					if malformed {
						_, _ = w.Write([]byte(`{"status":"proof_required","status":"held"}`))
						return
					}
					if request.RunID != requestRun {
						t.Error("changed run during scan")
					}
					if secondPages == 1 {
						unconfirmedPage = bytes.Clone(raw)
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if !bytes.Equal(unconfirmedPage, raw) {
						t.Error("pending continuation changed on retry")
					}
					values = values[500:]
				default:
					t.Error("unexpected cursor")
					w.WriteHeader(400)
					return
				}
				items := []json.RawMessage{}
				for _, v := range values {
					body, _ := json.Marshal(v)
					items = append(items, body)
				}
				status, held := "proof_required", false
				if mode == "held-first" && request.Cursor == "" {
					status, held = "held", true
				}
				_ = json.NewEncoder(w).Encode(sourcewire.RecoveryPageReceipt{Status: status, Held: held, RunID: request.RunID, Generation: 1, ServerTime: now, UnacceptedFloor: now - 1, ExternalProofRequired: true, Family: "origins", Items: items, NextCursor: cursor})
				return
			}
			origins := make([]teamsRecoveryOrigin, 0, len(accepted))
			for _, v := range accepted {
				origins = append(origins, teamsRecoveryOrigin{Token: v.Token, Witness: v.Witness, Revision: v.Revision, EvidenceRevision: v.EvidenceRevision, Protection: v.Protection, Classification: v.Classification})
			}
			sort.Slice(origins, func(i, j int) bool { return origins[i].Token < origins[j].Token })
			items := make([]json.RawMessage, 0, len(origins))
			for _, v := range origins {
				b, _ := json.Marshal(v)
				items = append(items, b)
			}
			if invalidateOnPage {
				invalidated = true
			}
			cursor := ""
			if mode == "multi-replace" && len(accepted) > 0 {
				if request.Cursor == "" {
					firstPages++
					cursor = "replacement-page-two"
				} else if request.Cursor == "replacement-page-two" {
					secondPages++
					items = []json.RawMessage{}
				} else {
					t.Error("unexpected replacement cursor")
					w.WriteHeader(400)
					return
				}
			}
			generation := int64(len(accepted) + 1)
			status := "proof_required"
			held := false
			if mode == "retained-baseline" || mode == "nonprivate-stale" {
				generation = runGenerations[request.RunID]
				held = runHeld[request.RunID]
				if held {
					status = "held"
				}
			}
			_ = json.NewEncoder(w).Encode(sourcewire.RecoveryPageReceipt{Status: status, Held: held, RunID: request.RunID, Generation: generation, ServerTime: now, UnacceptedFloor: now - 1, ExternalProofRequired: true, Family: "origins", Items: items, NextCursor: cursor})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/source-protection/") && strings.Contains(r.URL.Path, "/receipts/"):
			lookups++
			if len(raw) != 0 {
				t.Error("lookup sent an observation body")
			}
			if duringLookup != nil {
				duringLookup()
			}
			if lookupMissing {
				http.Error(w, "unavailable", http.StatusNotFound)
				return
			}
			lookedUp := firstOrigin
			if len(secondOrigin) > 0 {
				candidate, e := sourcewire.DecodeProtectionObservation(secondOrigin)
				if e == nil && strings.Contains(r.URL.Path, candidate.ObservationID) {
					lookedUp = secondOrigin
				}
			}
			v, e := sourcewire.DecodeProtectionObservation(lookedUp)
			if e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			want := "/v1/device/libraries/" + d.ID + "/source-protection/" + v.ObservationID + "/receipts/" + sourcewire.ProtectionDigest(lookedUp)
			if r.URL.Path != want {
				t.Error("lookup changed exact observation identity")
			}
			_ = json.NewEncoder(w).Encode(sourcewire.ProtectionReceipt{Status: "accepted", ObservationID: v.ObservationID, Revision: v.Revision, ObservationSHA256: sourcewire.ProtectionDigest(lookedUp), PrivacyRevision: 1, OriginProtection: v.Protection})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/source-protection/"):
			v, e := sourcewire.DecodeProtectionObservation(raw)
			if e != nil {
				t.Error(e)
				w.WriteHeader(400)
				return
			}
			originAttempts++
			if originAttempts > 1 && bytes.Equal(firstOrigin, raw) {
				resent++
			}
			if mode == "first-expired" && originAttempts == 1 {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "source_issuance_expired"}})
				return
			}
			if multipage && v.Token == suffixCandidate.Token && v.ExpectedRevision != 4 {
				t.Error("page-two origin became an absent baseline")
			}
			if originAttempts == 1 {
				var prepared int
				want := 1
				if e = c.DB.QueryRow(`SELECT count(*) FROM teams_origin_outbox WHERE state='prepared'`).Scan(&prepared); e != nil || prepared != want {
					t.Errorf("first PUT lacked its durable journal: %d %v", prepared, e)
				}
			}
			if _, ok := accepted[v.ObservationID]; !ok && mode != "stale" {
				accepted[v.ObservationID] = v
			}
			if len(firstOrigin) == 0 {
				firstOrigin = bytes.Clone(raw)
				// Simulate acceptance followed by a lost response.
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if originAttempts == 2 && mode != "lost-lookup" && !bytes.Equal(firstOrigin, raw) {
				t.Error("origin retry changed bytes")
			}
			if originAttempts == 2 && (mode == "stale" || mode == "expired" || mode == "lookup") {
				code := "stale_protection_revision"
				if mode == "expired" {
					code = "source_issuance_expired"
				}
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
				return
			}
			signedOrigins++
			_ = json.NewEncoder(w).Encode(sourcewire.ProtectionReceipt{Status: "accepted", ObservationID: v.ObservationID, Revision: v.Revision, ObservationSHA256: sourcewire.ProtectionDigest(raw), PrivacyRevision: 1, OriginProtection: v.Protection, SafetyPending: mode == "retained-baseline" || mode == "nonprivate-stale"})
		default:
			t.Errorf("unexpected synthetic route %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	credentialRaw, _ := json.Marshal(map[string]any{"version": 1, "origin": server.URL, "org_id": org, "person_id": person, "installation_id": installation, "library_id": d.ID, "library_instance": d.Instance, "application_id": "test-app", "private_key": base64.RawURLEncoding.EncodeToString(key), "source_token_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "metrics_default": map[string]any{"value": false, "policy_version": 1}})
	credential, err := teams.ParseCredential(credentialRaw, d.ID, d.Instance, "test-app", true)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	settings, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, org, installation, teamsPayloadHash(pub), true, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.acknowledgeTeamsProtectionPolicy(ctx, d, credential, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	settings, err = readTeamsSettings(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	var sourceRev, privacyRev int64
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&sourceRev); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacyRev); err != nil {
		t.Fatal(err)
	}
	gen, err := readCacheGenerations(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	opaque := func(b byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }
	snapshot := teamsV2Inventory{Items: []sourcewire.Item{}, Complete: true, SourceRevision: sourceRev, PrivacyRevision: privacyRev, Generation: gen.all}
	last := byte(13)
	if mode == "many" {
		last = 88
	}
	for i := byte(1); i <= last; i += 3 {
		protection := "private"
		if i == 1 || (mode == "retained-baseline" && i == 4) {
			protection = "public"
		}
		snapshot.Items = append(snapshot.Items, sourcewire.Item{Kind: "codex_thread", Token: opaque(i), Witness: opaque(i + 1), Profile: &sourcewire.Profile{ID: "codex_thread_usage", Version: 1}, EvidenceRevision: opaque(i + 2), Protection: protection, Dependencies: []string{}})
	}
	if mode == "many" {
		sort.Slice(snapshot.Items, func(i, j int) bool { return snapshot.Items[i].Token < snapshot.Items[j].Token })
	}
	suffixCandidate = snapshot.Items[0]
	hasher, err := sourcewire.NewInventoryHasher(true, sourcewire.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range snapshot.Items {
		if err = hasher.Add(item); err != nil {
			t.Fatal(err)
		}
	}
	inventoryHash, _, err := hasher.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`UPDATE teams_protection_baseline SET state='current',library_id=?,instance_id=?,inventory_sha256=?,source_revision=?,privacy_revision=? WHERE id=1`, d.ID, d.Instance, inventoryHash, sourceRev, privacyRev); err != nil {
		t.Fatal(err)
	}
	writer, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.db.Close()
	tx, err := teamsBeginProtectedWrite(ctx, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	writer.authorized.Store(true)
	if err = teamsCaptureOpenContract(ctx, tx); err != nil {
		t.Fatal(err)
	}
	writer.authorized.Store(false)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	hostDir := filepath.Join(t.TempDir(), "host-store")
	store := &teamsSourceFaultStore{Store: credentialstore.New(hostDir)}
	scope := teamsCheckpointScope(installationID(cfg.Application), d.ID)
	guard := bytes.Repeat([]byte{42}, 32)
	writer.requireFence = true
	writer.expectedInstance = teamsCheckpointInstance(d.ID, d.Instance)
	if err = teamsCheckpointActivate(ctx, writer, store, scope, scope, teamsCheckpointInstance(d.ID, d.Instance), guard, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: writer, store: store, name: scope, scope: scope, guardKey: guard}
	defer func() { c.teamsCheckpoint = nil }()
	if err = c.teamsUpgradeProtectedFormat35(ctx, cfg, d, func(*sql.Tx) error { return nil }, nil); err != nil {
		t.Fatalf("synthetic guarded format upgrade: %v", err)
	}
	if err = c.setTeamsSourceTransferChoice(ctx, d, credential, settings.SettingsRevision, true); err != nil {
		t.Fatal(err)
	}
	override(t, &teamsWorkerCapabilities, func() teamsCapabilityManifest {
		return teamsCapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}}
	})
	override(t, &teamsLoadCredential, func(*Catalog, Config, LibraryDescriptor) (*teams.EnrollmentCredential, error) {
		return teams.ParseCredential(credentialRaw, d.ID, d.Instance, "test-app", true)
	})
	override(t, &teamsDeviceClient, func(origin, installation string, signing ed25519.PrivateKey, _ bool) (*teams.DeviceClient, error) {
		return teams.NewDeviceClient(origin, installation, signing, true)
	})
	off := testServer(t, cfg, c)
	if _, err = off.checkTeamsWork(ctx); err != nil {
		t.Fatal(err)
	}
	if starts != 0 || signedOrigins != 0 {
		t.Fatal("disabled cohort sent a source request")
	}
	teamsSourcePilotEnabled = true
	override(t, &teamsIndependentSourceCarrierReady, func(teams.CapabilityManifest, bool) bool { return true })
	service := testServer(t, cfg, c)
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatal(err)
	}
	service.teamsWorker.mu.Lock()
	problem := service.teamsWorker.sourceProblem
	service.teamsWorker.mu.Unlock()
	if starts != 0 || signedOrigins != 0 || !strings.Contains(problem, "verified evidence") {
		t.Fatalf("missing census proof not held visibly: %q", problem)
	}
	override(t, &teamsIndependentRosterProof, func(ctx context.Context, tx *sql.Tx, _ Config, _ LibraryDescriptor, _ *teams.EnrollmentCredential) (teamsV2Inventory, error) {
		if invalidated {
			return teamsV2Inventory{}, errTeamsSnapshotChanged
		}
		if mode == "marker-census" || mode == "marker-checkpoint" {
			var marked bool
			if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_origin_attempts WHERE first_attempt_at>0 AND terminal_code='')`).Scan(&marked); e != nil {
				return teamsV2Inventory{}, e
			}
			if marked {
				if mode == "marker-census" {
					return teamsV2Inventory{}, errTeamsSnapshotChanged
				}
				store.failNextLoad = true
			}
		}
		return snapshot, nil
	})
	if mode == "retained-baseline" || mode == "nonprivate-stale" {
		override(t, &teamsNonPrivateSourceProofEnabled, true)
		// A real birth supplies this private, location-checked writer binding.
		// This fixture focuses on the signed 038 journal and baseline CAS.
		d.ManagedBirth = true
		writer.validateBinding = func() error { return nil }
		if err = c.writeTransaction(ctx, "hold before synthetic baseline", func(tx *sql.Tx) error {
			_, e := tx.Exec(`UPDATE teams_protection_baseline SET state='held' WHERE id=1`)
			return e
		}); err != nil {
			t.Fatal(err)
		}
		client, e := teams.NewDeviceClient(server.URL, installation, key, true)
		if e != nil {
			t.Fatal(e)
		}
		for turn := 0; turn < 8; turn++ {
			e = c.teamsProduceRetainedBaseline(ctx, cfg, client, d, credential)
			if e == nil {
				break
			}
		}
		if e != nil {
			t.Fatalf("signed baseline: %v", e)
		}
		var state, run string
		if e = c.DB.QueryRow(`SELECT state,proposal_sha256 FROM teams_protection_baseline WHERE id=1`).Scan(&state, &run); e != nil || state != "current" || run == "" {
			t.Fatalf("baseline not checkpointed: %s %v", state, e)
		}
		if n := countRows(t, c, `SELECT count(*) FROM teams_origin_runs WHERE token='baseline' AND state='completed' AND start_receipt_json IS NOT NULL`); n != 1 {
			t.Fatalf("signed run not retained: %d", n)
		}
		if starts != 2 || signedOrigins != 0 {
			t.Fatalf("unexpected baseline effects: %d %d", starts, signedOrigins)
		}
		if mode == "nonprivate-stale" {
			for turn := 0; turn < 12 && originAttempts == 0; turn++ {
				_ = c.teamsRunIndependentSourcePilot(ctx, cfg, client, d, credential, teams.CapabilityManifest{}, true)
			}
			if originAttempts != 1 || signedOrigins != 0 {
				t.Fatalf("expected uncertain first attempt: %d %d", originAttempts, signedOrigins)
			}
			invalidated = true
			for turn := 0; turn < 3; turn++ {
				_ = c.teamsRunIndependentSourcePilot(ctx, cfg, client, d, credential, teams.CapabilityManifest{}, true)
			}
			if originAttempts != 1 || signedOrigins != 0 {
				t.Fatal("changed evidence replayed uncertain non-private origin")
			}
			return
		}
		// Publish every bounded synthetic origin through the existing signed
		// worker, then rebuild from the retained receipts and server floor.
		for turn := 0; turn < 80 && signedOrigins < len(snapshot.Items); turn++ {
			e = c.teamsRunIndependentSourcePilot(ctx, cfg, client, d, credential, teams.CapabilityManifest{}, true)
			// The synthetic server deliberately loses the first accepted PUT;
			// the worker must replay its retained exact bytes on the next turn.
			if e != nil && turn > 0 && !errors.Is(e, errTeamsPilotMore) && !errors.Is(e, errTeamsPilotRetry) && !errors.Is(e, errTeamsPilotPending) {
				t.Fatalf("signed origin %d: %v", turn, e)
			}
		}
		if signedOrigins != len(snapshot.Items) {
			t.Fatalf("origin count %d", signedOrigins)
		}
		public, private := 0, 0
		for _, v := range accepted {
			switch v.Protection {
			case "non_private":
				public++
			case "explicit_private":
				private++
			}
		}
		if public != 2 || private != len(snapshot.Items)-2 {
			t.Fatalf("wrong signed protections: %d %d", public, private)
		}
		for turn := 0; turn < 12; turn++ {
			if e = c.teamsRunIndependentSourcePilot(ctx, cfg, client, d, credential, teams.CapabilityManifest{}, true); e == nil {
				break
			}
		}
		if e != nil {
			t.Fatalf("finish safety recheck: %v", e)
		}
		finishedStarts := starts
		for turn := 0; turn < 4; turn++ {
			if e = c.teamsRunIndependentSourcePilot(ctx, cfg, client, d, credential, teams.CapabilityManifest{}, true); e != nil {
				t.Fatal(e)
			}
		}
		if starts != finishedStarts {
			t.Fatal("completed safety proof created extra scans")
		}
		if e = c.writeTransaction(ctx, "synthetic baseline invalidation", func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE teams_protection_baseline SET state='held' WHERE id=1`)
			return err
		}); e != nil {
			t.Fatal(e)
		}
		for turn := 0; turn < 12; turn++ {
			e = c.teamsProduceRetainedBaseline(ctx, cfg, client, d, credential)
			if e == nil {
				break
			}
		}
		if e != nil {
			var pending, acceptedCount, active, completed int
			_ = c.DB.QueryRow(`SELECT count(*) FILTER(WHERE state<>'accepted'),count(*) FILTER(WHERE state='accepted') FROM teams_origin_outbox`).Scan(&pending, &acceptedCount)
			_ = c.DB.QueryRow(`SELECT count(*) FILTER(WHERE state='ready'),count(*) FILTER(WHERE state='completed') FROM teams_origin_runs`).Scan(&active, &completed)
			t.Fatalf("baseline with retained origins: %v (outbox pending=%d accepted=%d runs ready=%d completed=%d)", e, pending, acceptedCount, active, completed)
		}
		if n := countRows(t, c, `SELECT count(*) FROM teams_origin_runs WHERE token='baseline' AND state='completed'`); n != 1 {
			t.Fatalf("superseded baseline scans retained: %d", n)
		}
		if e = c.teamsPilotCheckpointCurrent(ctx, d); e != nil {
			t.Fatal(e)
		}
		// A higher floor retained by a safety scan must fence a later baseline
		// or batch even when the original baseline had a lower floor.
		if e = c.writeTransaction(ctx, "synthetic-safety-floor", func(tx *sql.Tx) error {
			result, x := tx.Exec(`UPDATE teams_origin_run_pages SET receipt_json=json_set(receipt_json,'$.unaccepted_floor',?) WHERE run_key=(SELECT run_key FROM teams_origin_runs WHERE token='batch-safety' AND state='completed' ORDER BY rowid DESC LIMIT 1)`, time.Now().Unix()+120)
			if x != nil {
				return x
			}
			n, x := result.RowsAffected()
			if x != nil {
				return x
			}
			if n < 1 {
				return errTeamsProtectionHeld
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		tx, e := c.reader().BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		e = teamsRequireRetainedRecoveryFloorIn(ctx, tx, d, credential, &teamsRecoveryOriginScan{last: sourcewire.RecoveryPageReceipt{UnacceptedFloor: time.Now().Unix() - 1}})
		_ = tx.Rollback()
		if !errors.Is(e, errTeamsProtectionHeld) {
			t.Fatalf("higher safety floor lost: %v", e)
		}
		return
	}
	store.rejectCurrent = true
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatal(err)
	}
	if starts != 0 || signedOrigins != 0 {
		t.Fatal("pending host finalization sent a network effect")
	}
	store.rejectCurrent = false
	if multipage {
		if fifth {
			for turn := 0; turn < 8; turn++ {
				service = testServer(t, cfg, c)
				if _, err = service.checkTeamsWork(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if firstPages != 1 || secondPages != 3 || originAttempts != 0 {
				t.Fatalf("fifth page crossed bound: %d %d %d", firstPages, secondPages, originAttempts)
			}
			if n := countRows(t, c, `SELECT count(*) FROM teams_origin_runs WHERE state='held'`); n != 1 {
				t.Fatalf("oversized chain not held: %d", n)
			}
			return
		}
		if malformed {
			var active bool
			for turn := 0; turn < 5; turn++ {
				service = testServer(t, cfg, c)
				active, err = service.checkTeamsWork(ctx)
				if err != nil {
					t.Fatal(err)
				}
			}
			service.teamsWorker.mu.Lock()
			problem := service.teamsWorker.sourceProblem
			service.teamsWorker.mu.Unlock()
			if active || !strings.Contains(problem, "waiting for verified evidence") {
				t.Fatalf("held scan cadence: active=%v problem=%q", active, problem)
			}
			if starts != 2 || firstPages != 1 || secondPages != 1 || originAttempts != 0 {
				t.Fatalf("invalid page was retried or disclosed: %d %d %d %d", starts, firstPages, secondPages, originAttempts)
			}
			if n := countRows(t, c, `SELECT count(*) FROM teams_origin_runs WHERE state='held'`); n != 1 {
				t.Fatalf("invalid page not durably held: %d", n)
			}
			if err = c.writeTransaction(ctx, "synthetic-held-expiry", func(tx *sql.Tx) error {
				_, e := tx.Exec(`UPDATE teams_origin_runs SET expires_at=? WHERE state='held'`, time.Now().Unix()-1)
				return e
			}); err != nil {
				t.Fatal(err)
			}
			if _, err = service.checkTeamsWork(ctx); err != nil {
				t.Fatal(err)
			}
			if starts != 2 || secondPages != 1 || originAttempts != 0 {
				t.Fatal("expired invalid scan restarted automatically")
			}
			return
		}
		// Page one commits in this process. A separate OS process then opens
		// the same SQLite catalog and durable host checkpoint and loses the
		// first page-two reply; no live source or provider is used.
		active, e := service.checkTeamsWork(ctx)
		if e != nil {
			t.Fatal(e)
		}
		service.teamsWorker.mu.Lock()
		problem := service.teamsWorker.sourceProblem
		service.teamsWorker.mu.Unlock()
		if !active || !strings.Contains(problem, "retry a connection") {
			t.Fatalf("uncertain start cadence: active=%v problem=%q", active, problem)
		}
		if _, err = service.checkTeamsWork(ctx); err != nil {
			t.Fatal(err)
		}
		if firstPages != 1 || secondPages != 0 {
			t.Fatalf("unexpected prefix %d/%d", firstPages, secondPages)
		}
		if mode == "capacity" {
			if n := countRows(t, c, `SELECT count(*) FROM teams_origin_runs WHERE state='held'`); n != 0 {
				t.Fatal("capacity created permanent hold")
			}
			if _, err = service.checkTeamsWork(ctx); err != nil {
				t.Fatal(err)
			}
			if firstPages != 2 {
				t.Fatal("capacity page was not retried")
			}
		}
		processConfig := cfg
		processConfig.APIToken = ""
		processConfig.TL1Token = ""
		processConfig.GitHubToken = ""
		fixture := teamsOriginProcessFixture{Config: processConfig, Descriptor: d, Snapshot: snapshot, HostDir: hostDir, Scope: scope}
		encoded, e := json.Marshal(fixture)
		if e != nil {
			t.Fatal(e)
		}
		c.teamsCheckpoint = nil
		c.Close()
		_ = writer.db.Close()
		cmd := exec.Command(os.Args[0], "-test.run=^TestTeamsOriginPageProcessChild$", "-test.v")
		cmd.Env = append(os.Environ(), "PHAROS_ORIGIN_PROCESS_FIXTURE="+base64.RawURLEncoding.EncodeToString(encoded), "PHAROS_ORIGIN_CREDENTIAL="+base64.RawURLEncoding.EncodeToString(credentialRaw), "PHAROS_ORIGIN_GUARD="+base64.RawURLEncoding.EncodeToString(guard), "PHAROS_TEST_PROCESSES=1")
		output, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("separate page process failed: %v\n%s", e, output)
		}
		if secondPages != 1 || originAttempts != 0 {
			t.Fatal("separate process did not persist the uncertain page")
		}
		c = reopenCatalog(t, cfg.CatalogPath)
		defer c.Close()
		writer, e = teamsOpenProtectedWriter(c.Path)
		if e != nil {
			t.Fatal(e)
		}
		defer writer.db.Close()
		writer.requireFence = true
		writer.expectedInstance = teamsCheckpointInstance(d.ID, d.Instance)
		c.teamsCheckpoint = &teamsCheckpointRuntime{writer: writer, store: store, name: scope, scope: scope, guardKey: guard}
		reopened031 := false
		for turn := 0; turn < 9; turn++ {
			service = testServer(t, cfg, c)
			if _, err = service.checkTeamsWork(ctx); err != nil {
				t.Fatal(err)
			}
			if secondPages < 2 && originAttempts != 0 {
				t.Fatal("sent origin before complete scan")
			}
			if mode == "" && originAttempts == 1 && !reopened031 {
				// The signed first 031 may have committed despite its lost reply.
				// A fresh process must replay exactly its existing journal bytes.
				reopened031 = true
				c.teamsCheckpoint = nil
				c.Close()
				_ = writer.db.Close()
				cmd = exec.Command(os.Args[0], "-test.run=^TestTeamsOriginPageProcessChild$", "-test.v")
				cmd.Env = append(os.Environ(), "PHAROS_ORIGIN_PROCESS_FIXTURE="+base64.RawURLEncoding.EncodeToString(encoded), "PHAROS_ORIGIN_CREDENTIAL="+base64.RawURLEncoding.EncodeToString(credentialRaw), "PHAROS_ORIGIN_GUARD="+base64.RawURLEncoding.EncodeToString(guard), "PHAROS_TEST_PROCESSES=1")
				output, e = cmd.CombinedOutput()
				if e != nil {
					t.Fatalf("031 replay process failed: %v\n%s", e, output)
				}
				if originAttempts != 2 || signedOrigins != 1 {
					t.Fatalf("fresh process failed exact 031 replay: attempts=%d accepted=%d", originAttempts, signedOrigins)
				}
				c = reopenCatalog(t, cfg.CatalogPath)
				defer c.Close()
				writer, e = teamsOpenProtectedWriter(c.Path)
				if e != nil {
					t.Fatal(e)
				}
				defer writer.db.Close()
				writer.requireFence = true
				writer.expectedInstance = teamsCheckpointInstance(d.ID, d.Instance)
				c.teamsCheckpoint = &teamsCheckpointRuntime{writer: writer, store: store, name: scope, scope: scope, guardKey: guard}
			}
			if signedOrigins == 5 {
				break
			}
		}
		if mode == "held-first" {
			if firstPages != 1 || secondPages != 2 || originAttempts != 0 {
				t.Fatalf("held prefix emitted observations: %d/%d/%d", firstPages, secondPages, originAttempts)
			}
			if err = c.writeTransaction(ctx, "held-chain-expiry", func(tx *sql.Tx) error {
				_, e := tx.Exec(`UPDATE teams_origin_runs SET expires_at=? WHERE state='ready'`, time.Now().Unix()-1)
				return e
			}); err != nil {
				t.Fatal(err)
			}
			if _, err = service.checkTeamsWork(ctx); err != nil {
				t.Fatal(err)
			}
			if starts != 2 || originAttempts != 0 {
				t.Fatal("expired held chain restarted or sent")
			}
			return
		}
		if starts != 2 || firstPages != 1+btoi(mode == "capacity") || secondPages != 2 || signedOrigins != 5 {
			t.Fatalf("recovery did not continue: starts=%d page1=%d page2=%d accepted=%d", starts, firstPages, secondPages, signedOrigins)
		}
		return
	}
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || signedOrigins != 0 {
		t.Fatalf("uncertain start advanced: starts=%d origins=%d", starts, signedOrigins)
	}
	invalidateOnPage = true
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatal(err)
	}
	if starts != 2 {
		t.Fatalf("recovery start was not retried exactly: %d", starts)
	}
	if signedOrigins != 0 || !invalidated {
		t.Fatal("changed source proof was sent")
	}
	invalidated, invalidateOnPage = false, false
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatal(err)
	}
	if mode == "marker-census" || mode == "marker-checkpoint" {
		for turn := 0; turn < 2; turn++ {
			if _, err = service.checkTeamsWork(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if originAttempts != 0 {
			t.Fatalf("known-unsent marker sent %d PUTs", originAttempts)
		}
		var at int64
		var disposition string
		if err = c.DB.QueryRow(`SELECT first_attempt_at,terminal_code FROM teams_origin_attempts`).Scan(&at, &disposition); err != nil || at != 0 || disposition != "" {
			t.Fatalf("known-unsent marker not reverted: %d %q %v", at, disposition, err)
		}
		return
	}
	if originAttempts != 1 || signedOrigins != 0 {
		t.Fatalf("uncertain origin advanced: attempts=%d accepted=%d", originAttempts, signedOrigins)
	}
	if mode == "lost-lookup" {
		if err = c.writeTransaction(ctx, "synthetic-local-run-expiry", func(tx *sql.Tx) error {
			_, e := tx.Exec(`UPDATE teams_origin_outbox SET expires_at=? WHERE state='prepared'`, time.Now().Unix()-1)
			return e
		}); err != nil {
			t.Fatal(err)
		}
		lookupMissing = false
		local := httptest.NewServer(service)
		defer local.Close()
		r, err := http.NewRequest(http.MethodPost, local.URL+"/api/teams/worker/source-reconcile", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+cfg.APIToken)
		response, err := local.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"found":true`) || lookups != 1 || originAttempts != 1 {
			t.Fatalf("lost receipt lookup %d %s gets=%d puts=%d", response.StatusCode, body, lookups, originAttempts)
		}
		if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox o JOIN teams_origin_attempts a ON a.observation_id=o.observation_id WHERE o.state='accepted' AND o.receipt_json IS NOT NULL AND a.terminal_code='reconciled_lookup'`); n != 1 {
			t.Fatal("lost receipt was not committed")
		}
		if _, err = service.checkTeamsWork(ctx); err != nil {
			t.Fatal(err)
		}
		if resent != 0 || originAttempts != 2 || signedOrigins != 1 {
			t.Fatalf("worker did not advance to a distinct observation: replayed=%d attempts=%d accepted=%d", resent, originAttempts, signedOrigins)
		}
		return
	}
	if mode == "first-expired" {
		var disposition string
		if err = c.DB.QueryRow(`SELECT terminal_code FROM teams_origin_attempts`).Scan(&disposition); err != nil || disposition != "first_rejected_source_issuance_expired" {
			t.Fatalf("first disposition %q: %v", disposition, err)
		}
		ambiguous, e := teamsOriginAmbiguousIn(ctx, c.reader(), d, credential)
		if e != nil || ambiguous {
			t.Fatal("first rejection became ambiguous")
		}
		if n := countRows(t, c, `SELECT count(*) FROM teams_origin_runs WHERE state='completed'`); n != 1 {
			t.Fatal("first rejection did not retire scan")
		}
		return
	}
	// Withdrawal must not rewrite the uncertain command or authorize its replay.
	settings, err = readTeamsSettings(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.setTeamsSourceTransferChoice(ctx, d, credential, settings.SettingsRevision, false); err != nil {
		t.Fatal(err)
	}
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatal(err)
	}
	if originAttempts != 1 {
		t.Fatal("withdrawal sent an uncertain observation")
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox o JOIN teams_origin_attempts a ON a.observation_id=o.observation_id WHERE o.state='prepared' AND o.receipt_json IS NULL AND a.terminal_code=''`); n != 1 {
		t.Fatalf("withdrawal lost uncertain command: %d", n)
	}
	if err = c.setTeamsSourceTransferChoice(ctx, d, credential, settings.SettingsRevision, true); err != nil {
		t.Fatal(err)
	}
	// An old census cannot replace the exact request, but must not discard its receipt.
	invalidated = true
	// Expiry of the original recovery run removes authority for a replay,
	// but a user-requested read-only lookup may still find its receipt.
	if mode == "expired-authority" {
		if err = c.writeTransaction(ctx, "synthetic-run-expiry", func(tx *sql.Tx) error {
			_, e := tx.Exec(`UPDATE teams_origin_outbox SET expires_at=? WHERE state='prepared'`, time.Now().Unix()-1)
			return e
		}); err != nil {
			t.Fatal(err)
		}
		if _, err = service.checkTeamsWork(ctx); err != nil {
			t.Fatal(err)
		}
		if originAttempts != 1 {
			t.Fatal("expired authority replayed observation")
		}
		r := httptest.NewRequest(http.MethodPost, "/api/teams/worker/source-reconcile", strings.NewReader("{}"))
		w := httptest.NewRecorder()
		service.teamsWorkerHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"found":false`) || lookups != 1 {
			t.Fatalf("unavailable expired lookup: %d %s", w.Code, w.Body.String())
		}
		if _, err = service.checkTeamsWork(ctx); err != nil {
			t.Fatal(err)
		}
		if originAttempts != 1 {
			t.Fatal("failed lookup recreated expired authority")
		}
		if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox o JOIN teams_origin_attempts a ON a.observation_id=o.observation_id WHERE o.state='prepared' AND o.receipt_json IS NULL AND a.terminal_code=''`); n != 1 {
			t.Fatal("expired attempt lost or replaced")
		}
		return
	}
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatal(err)
	}
	if mode == "nonprivate-stale" {
		if originAttempts != 1 || signedOrigins != 0 {
			t.Fatalf("stale non-private replayed: attempts=%d accepted=%d", originAttempts, signedOrigins)
		}
		if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox WHERE state='prepared' AND receipt_json IS NULL`); n != 1 {
			t.Fatal("uncertain receipt not retained")
		}
		return
	}
	if mode == "stale" || mode == "expired" || mode == "lookup" {
		want := "unknown_stale_protection_revision"
		if mode == "expired" {
			want = "unknown_source_issuance_expired"
		}
		var got string
		if err = c.DB.QueryRow(`SELECT terminal_code FROM teams_origin_attempts LIMIT 1`).Scan(&got); err != nil || got != want {
			t.Fatalf("disposition %q: %v", got, err)
		}
		if _, err = service.checkTeamsWork(ctx); err != nil {
			t.Fatal(err)
		}
		if originAttempts != 2 {
			t.Fatal("unknown outcome retransmitted")
		}
		ambiguous, e := teamsOriginAmbiguousIn(ctx, c.reader(), d, credential)
		if e != nil || !ambiguous {
			t.Fatal("unresolved possible hold lost")
		}
		if mode == "lookup" {
			// A missing oldest receipt must not mask a later recoverable one.
			second, e := sourcewire.DecodeProtectionObservation(firstOrigin)
			if e != nil {
				t.Fatal(e)
			}
			firstID := second.ObservationID
			for i := 1; i < 256; i++ {
				second.Token = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(i)}, 32))
				second.ObservationID, e = sourcewire.ProtectionObservationID(second, installation, d.ID)
				if e != nil {
					t.Fatal(e)
				}
				if second.ObservationID > firstID {
					break
				}
			}
			if second.ObservationID <= firstID {
				t.Fatal("could not order synthetic observations")
			}
			secondOrigin, _, e = second.Bytes()
			if e != nil {
				t.Fatal(e)
			}
			e = c.writeTransaction(ctx, "synthetic-second-unknown", func(tx *sql.Tx) error {
				_, e := tx.Exec(`INSERT INTO teams_origin_outbox(observation_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,run_id,run_generation,expires_at,token,expected_revision,observation_json,observation_sha256,state) SELECT ?,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,run_id,run_generation,expires_at,?,expected_revision,?,?,'prepared' FROM teams_origin_outbox WHERE observation_id=?`, second.ObservationID, second.Token, secondOrigin, sourcewire.ProtectionDigest(secondOrigin), firstID)
				if e != nil {
					return e
				}
				_, e = tx.Exec(`INSERT INTO teams_origin_attempts(observation_id,first_attempt_at,terminal_code) VALUES(?,?,'unknown_stale_protection_revision')`, second.ObservationID, time.Now().Unix())
				return e
			})
			if e != nil {
				t.Fatal(e)
			}
			status := httptest.NewRecorder()
			service.teamsWorkerHTTP(status, httptest.NewRequest(http.MethodGet, "/api/teams/worker", nil))
			var statusBody struct {
				Available bool `json:"sourceLookupAvailable"`
			}
			if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &statusBody) != nil || !statusBody.Available {
				t.Fatalf("lookup action not offered: %d %s", status.Code, status.Body.String())
			}
			action := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "/api/teams/worker/source-reconcile", strings.NewReader("{}"))
				w := httptest.NewRecorder()
				service.teamsWorkerHTTP(w, r)
				return w
			}
			w := action()
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"found":false`) || lookups != 1 || originAttempts != 2 {
				t.Fatalf("missing lookup altered state: %d lookups=%d puts=%d", w.Code, lookups, originAttempts)
			}
			if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox o JOIN teams_origin_attempts a ON a.observation_id=o.observation_id WHERE o.state='prepared' AND o.receipt_json IS NULL AND a.terminal_code='unknown_stale_protection_revision'`); n != 2 {
				t.Fatal("missing lookup changed protected state")
			}
			lookupMissing = false
			duringLookup = func() {
				use, e := fileLease(libraryUseLock(cfg), true)
				if e != nil {
					t.Errorf("library lease crossed lookup: %v", e)
				} else {
					use.Close()
				}
				lease, e := c.teamsSharingLease()
				if e != nil {
					t.Errorf("sharing lease crossed lookup: %v", e)
				} else {
					lease.Close()
				}
				if e = c.setTeamsSourceTransferChoice(ctx, d, credential, settings.SettingsRevision, false); e != nil {
					t.Errorf("withdraw during lookup: %v", e)
				}
			}
			w = action()
			if w.Code != http.StatusOK || lookups != 2 || originAttempts != 2 {
				t.Fatalf("exact lookup failed: %d %s lookups=%d puts=%d", w.Code, w.Body.String(), lookups, originAttempts)
			}
			if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox o JOIN teams_origin_attempts a ON a.observation_id=o.observation_id WHERE o.state='accepted' AND o.receipt_json IS NOT NULL AND a.terminal_code='reconciled_lookup'`); n != 1 {
				t.Fatal("accepted receipt and disposition did not commit together")
			}
			if _, err = service.checkTeamsWork(ctx); err != nil {
				t.Fatal(err)
			}
			if originAttempts != 2 {
				t.Fatal("withdrawal authorized a new observation")
			}
			status = httptest.NewRecorder()
			service.teamsWorkerHTTP(status, httptest.NewRequest(http.MethodGet, "/api/teams/worker", nil))
			statusBody.Available = true
			if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &statusBody) != nil || statusBody.Available {
				t.Fatalf("withdrawn source offered lookup: %d %s", status.Code, status.Body.String())
			}
			if e = c.setTeamsSourceTransferChoice(ctx, d, credential, settings.SettingsRevision, true); e != nil {
				t.Fatal(e)
			}
			// Recreate one live uncertain receipt to prove an older expired
			// row cannot mask it, then age both terminal states.
			if e = c.writeTransaction(ctx, "synthetic-live-uncertain", func(tx *sql.Tx) error {
				if _, e := tx.Exec(`UPDATE teams_origin_outbox SET state='prepared',receipt_json=NULL WHERE observation_id=?`, second.ObservationID); e != nil {
					return e
				}
				_, e := tx.Exec(`UPDATE teams_origin_attempts SET terminal_code='unknown_stale_protection_revision' WHERE observation_id=?`, second.ObservationID)
				return e
			}); e != nil {
				t.Fatal(e)
			}
			duringLookup = nil
			// Valid immutable commands in both terminal states age beyond the
			// service's retained horizon. Neither may consume a lookup turn.
			for i, original := range [][]byte{firstOrigin, secondOrigin} {
				aged, e := sourcewire.DecodeProtectionObservation(original)
				if e != nil {
					t.Fatal(e)
				}
				oldID := aged.ObservationID
				for offset := int64(60); offset < 600; offset++ {
					aged.IssuedAt = time.Now().Unix() - int64(sourcewire.RecoveryHorizon/time.Second) - offset
					aged.ObservationID, e = sourcewire.ProtectionObservationID(aged, installation, d.ID)
					if e != nil {
						t.Fatal(e)
					}
					if i != 0 || aged.ObservationID < second.ObservationID {
						break
					}
				}
				if i == 0 && aged.ObservationID >= second.ObservationID {
					t.Fatal("could not sort expired observation first")
				}
				agedBody, _, e := aged.Bytes()
				if e != nil {
					t.Fatal(e)
				}
				disposition := ""
				if i == 1 {
					disposition = "unknown_stale_protection_revision"
				}
				e = c.writeTransaction(ctx, "synthetic-aged-attempt", func(tx *sql.Tx) error {
					if _, e := tx.Exec(`DELETE FROM teams_origin_attempts WHERE observation_id=?`, oldID); e != nil {
						return e
					}
					if _, e := tx.Exec(`UPDATE teams_origin_outbox SET observation_id=?,observation_json=?,observation_sha256=?,state='prepared',receipt_json=NULL WHERE observation_id=?`, aged.ObservationID, agedBody, sourcewire.ProtectionDigest(agedBody), oldID); e != nil {
						return e
					}
					_, e := tx.Exec(`INSERT INTO teams_origin_attempts(observation_id,first_attempt_at,terminal_code) VALUES(?,?,?)`, aged.ObservationID, time.Now().Unix(), disposition)
					return e
				})
				if e != nil {
					t.Fatal(e)
				}
				if i == 0 {
					status = httptest.NewRecorder()
					service.teamsWorkerHTTP(status, httptest.NewRequest(http.MethodGet, "/api/teams/worker", nil))
					statusBody.Available = false
					if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &statusBody) != nil || !statusBody.Available {
						t.Fatalf("live receipt masked by expired: %d %s", status.Code, status.Body.String())
					}
					w = action()
					if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"found":true`) || lookups != 3 {
						t.Fatalf("live receipt not selected: %d %s gets=%d", w.Code, w.Body.String(), lookups)
					}
				}
			}
			status = httptest.NewRecorder()
			service.teamsWorkerHTTP(status, httptest.NewRequest(http.MethodGet, "/api/teams/worker", nil))
			statusBody.Available = true
			if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &statusBody) != nil || statusBody.Available {
				t.Fatalf("expired issuance offered lookup: %d %s", status.Code, status.Body.String())
			}
			w = action()
			if w.Code != http.StatusConflict || lookups != 3 {
				t.Fatalf("expired issuance sent lookup: %d gets=%d", w.Code, lookups)
			}
			if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox o JOIN teams_origin_attempts a ON a.observation_id=o.observation_id WHERE o.state='prepared' AND o.receipt_json IS NULL AND a.terminal_code IN ('','unknown_stale_protection_revision')`); n != 2 {
				t.Fatal("expired lookup changed evidence")
			}
		}
		return
	}
	if signedOrigins != 1 {
		t.Fatalf("uncertain origin did not reconcile first: %d", signedOrigins)
	}
	if mode == "multi-replace" || mode == "many" {
		if err = c.writeTransaction(ctx, "synthetic-expire-accepted-run", func(tx *sql.Tx) error {
			if _, e := tx.Exec(`UPDATE teams_origin_runs SET expires_at=?`, time.Now().Unix()-1); e != nil {
				return e
			}
			_, e := tx.Exec(`UPDATE teams_origin_outbox SET expires_at=? WHERE state='accepted'`, time.Now().Unix()-1)
			return e
		}); err != nil {
			t.Fatal(err)
		}
	}
	invalidated = false
	if mode == "multi-replace" {
		for i := 0; i < 5 && signedOrigins < 5; i++ {
			if _, err = service.checkTeamsWork(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if signedOrigins != 5 || starts != 3 || firstPages != 1 || secondPages != 1 {
			t.Fatalf("multi-page replacement stalled: accepted=%d starts=%d pages=%d/%d", signedOrigins, starts, firstPages, secondPages)
		}
		return
	}
	if mode == "many" {
		for i := 0; i < 35 && signedOrigins < 30; i++ {
			if _, err = service.checkTeamsWork(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if signedOrigins != 30 || starts != 3 {
			t.Fatalf("replacement batch stalled: accepted=%d starts=%d", signedOrigins, starts)
		}
		return
	}
	for i := 0; i < 8 && signedOrigins < 5; i++ {
		if _, err = service.checkTeamsWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if signedOrigins != 5 || starts != 2 {
		t.Fatalf("independent bounded turns: accepted=%d starts=%d", signedOrigins, starts)
	}
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox WHERE state='accepted' AND receipt_json IS NOT NULL AND work_id=''`); n != 5 {
		t.Fatalf("worker stored %d independent receipts", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox WHERE state='held' AND work_id=''`); n != 0 {
		t.Fatalf("expired unattempted evidence not retained: %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_v2_uploads`); n != 0 {
		t.Fatal("source action required numeric work")
	}
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatal(err)
	}
	if signedOrigins != 5 || originAttempts != 6 {
		t.Fatalf("worker replayed accepted origins: accepted=%d attempts=%d", signedOrigins, originAttempts)
	}
	// Fill the bounded run journal with eight unrecoverable shapes ahead of
	// duplicate, independently retained complete evidence. Safe later rows
	// must still be retired; neither held origins nor receipts are deleted.
	if err = c.writeTransaction(ctx, "synthetic-capacity", func(tx *sql.Tx) error {
		var sourceKey string
		if e := tx.QueryRow(`SELECT run_key FROM teams_origin_runs WHERE state='completed' ORDER BY rowid DESC LIMIT 1`).Scan(&sourceKey); e != nil {
			return e
		}
		for i := 0; i < 31; i++ {
			key := fmt.Sprintf("synthetic-capacity-%02d", i)
			_, e := tx.Exec(`INSERT INTO teams_origin_runs(run_key,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,token,start_json,start_receipt_json,expires_at,state) SELECT ?,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,token,start_json,start_receipt_json,expires_at,state FROM teams_origin_runs WHERE run_key=?`, key, sourceKey)
			if e != nil {
				return e
			}
			if i < 8 {
				_, e = tx.Exec(`INSERT INTO teams_origin_run_pages(run_key,page_index,request_json,receipt_json) SELECT ?,page_index,request_json,X'7b7d' FROM teams_origin_run_pages WHERE run_key=?`, key, sourceKey)
			} else {
				_, e = tx.Exec(`INSERT INTO teams_origin_run_pages(run_key,page_index,request_json,receipt_json) SELECT ?,page_index,request_json,receipt_json FROM teams_origin_run_pages WHERE run_key=?`, key, sourceKey)
			}
			if e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_runs`); n != 32 {
		t.Fatalf("capacity fixture has %d runs", n)
	}
	var retired int
	for pass := 0; pass < 4 && retired < 8; pass++ {
		if err = c.writeTransaction(ctx, "synthetic-retire", func(tx *sql.Tx) error {
			n, e := teamsRetireRedundantOriginRunsIn(ctx, tx, d, credential, 8)
			retired += n
			return e
		}); err != nil {
			t.Fatal(err)
		}
	}
	if retired < 8 || countRows(t, c, `SELECT count(*) FROM teams_origin_outbox WHERE state='accepted'`) != 5 || countRows(t, c, `SELECT count(*) FROM teams_origin_outbox WHERE state='held'`) != 0 {
		t.Fatalf("unsafe or starved retirement: %d", retired)
	}

}

// The non-secret fixture is synthetic. The generated child credential and
// guard are injected separately through the environment, never a file or log.
type teamsOriginProcessFixture struct {
	Config         Config
	Descriptor     LibraryDescriptor
	Snapshot       teamsV2Inventory
	HostDir, Scope string
}

func TestTeamsOriginPageProcessChild(t *testing.T) {
	encoded := os.Getenv("PHAROS_ORIGIN_PROCESS_FIXTURE")
	if encoded == "" {
		t.Skip("subprocess only")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var fixture teamsOriginProcessFixture
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("invalid synthetic process fixture")
	}
	credential, err := base64.RawURLEncoding.DecodeString(os.Getenv("PHAROS_ORIGIN_CREDENTIAL"))
	if err != nil || len(credential) == 0 {
		t.Fatal("missing synthetic child credential")
	}
	defer clear(credential)
	guard, err := base64.RawURLEncoding.DecodeString(os.Getenv("PHAROS_ORIGIN_GUARD"))
	if err != nil || len(guard) == 0 {
		t.Fatal("missing synthetic child guard")
	}
	defer clear(guard)
	c := reopenCatalog(t, fixture.Config.CatalogPath)
	defer c.Close()
	writer, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.db.Close()
	writer.requireFence = true
	writer.expectedInstance = teamsCheckpointInstance(fixture.Descriptor.ID, fixture.Descriptor.Instance)
	store := credentialstore.New(fixture.HostDir)
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: writer, store: store, name: fixture.Scope, scope: fixture.Scope, guardKey: guard}
	defer func() { c.teamsCheckpoint = nil }()
	override(t, &teamsSourcePilotEnabled, true)
	override(t, &teamsWorkerCapabilities, func() teamsCapabilityManifest {
		return teamsCapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}}
	})
	override(t, &teamsLoadCredential, func(*Catalog, Config, LibraryDescriptor) (*teams.EnrollmentCredential, error) {
		return teams.ParseCredential(credential, fixture.Descriptor.ID, fixture.Descriptor.Instance, "test-app", true)
	})
	override(t, &teamsDeviceClient, func(origin, installation string, key ed25519.PrivateKey, _ bool) (*teams.DeviceClient, error) {
		return teams.NewDeviceClient(origin, installation, key, true)
	})
	override(t, &teamsIndependentSourceCarrierReady, func(teams.CapabilityManifest, bool) bool { return true })
	override(t, &teamsIndependentRosterProof, func(context.Context, *sql.Tx, Config, LibraryDescriptor, *teams.EnrollmentCredential) (teamsV2Inventory, error) {
		return fixture.Snapshot, nil
	})
	service := testServer(t, fixture.Config, c)
	if _, err = service.checkTeamsWork(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type teamsSourceFaultStore struct {
	credentialstore.Store
	rejectCurrent bool
	failNextLoad  bool
}

func (s *teamsSourceFaultStore) Load(name string) ([]byte, error) {
	if s.failNextLoad {
		s.failNextLoad = false
		return nil, credentialstore.ErrUnsafe
	}
	return s.Store.Load(name)
}

func (s *teamsSourceFaultStore) Replace(name string, old, next []byte) error {
	if s.rejectCurrent {
		record, err := teamsParseCheckpoint(next)
		if err == nil && record.Phase == "current" {
			return credentialstore.ErrUnsafe
		}
	}
	return s.Store.Replace(name, old, next)
}

func TestTeamsSourceCarrierRequiresNegotiatedFeatures(t *testing.T) {
	m := teams.CapabilityManifest{Capabilities: []string{"source_snapshot_v2"}, V2OnlyFeatures: []string{"source_protection_v1", "source_recovery_v1"}}
	if teamsIndependentSourceCarrierReady(m, false) {
		t.Fatal("legacy fallback admitted source effects")
	}
	if !teamsIndependentSourceCarrierReady(m, true) {
		t.Fatal("negotiated source carrier rejected")
	}
	m.V2OnlyFeatures = []string{"source_protection_v1"}
	if teamsIndependentSourceCarrierReady(m, true) {
		t.Fatal("incomplete source capabilities admitted")
	}
}

func TestTeamsOriginSweepPreservesHeldAndAmbiguous(t *testing.T) {
	c, _ := testCatalog(t)
	if _, err := c.DB.Exec(teamsProtectedFormat35DDL); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	credential := &teams.EnrollmentCredential{Origin: "https://synthetic.invalid", OrgID: "synthetic-org", PersonID: "synthetic-person", InstallationID: "synthetic-installation"}
	scope := teamsOriginBatchScope{inventoryHash: "current", settingsRevision: 2, sourceRevision: 3, privacyRevision: 4, generation: 5}
	err := c.writeTransaction(ctx, "synthetic-partials", func(tx *sql.Tx) error {
		for i := 0; i < 33; i++ {
			_, e := tx.Exec(`INSERT INTO teams_origin_runs(run_key,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,token,start_json,expires_at,state) VALUES(?,?,?,?,?,?,?,'','','old',1,1,1,1,'batch','{}',?,'prepared')`, fmt.Sprintf("partial-%02d", i), credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, time.Now().Unix()-1)
			if e != nil {
				return e
			}
		}
		_, e := tx.Exec(`INSERT INTO teams_origin_runs(run_key,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,token,start_json,expires_at,state) VALUES('held',?,?,?,?,?,?,'','','old',1,1,1,1,'batch','{}',?,'started')`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, time.Now().Unix()-1)
		if e != nil {
			return e
		}
		_, e = tx.Exec(`INSERT INTO teams_origin_run_pages(run_key,page_index,request_json,receipt_json) VALUES('held',0,'{}',?)`, []byte(`{"status":"held","held":true,"items":[]}`))
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	// The bounded sweep can need a later call, but never drops the held evidence.
	for i := 0; i < 2; i++ {
		if err = c.writeTransaction(ctx, "synthetic-sweep", func(tx *sql.Tx) error { _, e := teamsSweepOriginPartialsIn(ctx, tx, d, credential, scope); return e }); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_runs WHERE run_key LIKE 'partial-%'`); n != 0 {
		t.Fatalf("transport partials retained: %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_runs WHERE run_key='held' AND state='held'`); n != 1 {
		t.Fatal("held evidence was lost")
	}
	if err = c.writeTransaction(ctx, "synthetic-repeat-sweep", func(tx *sql.Tx) error {
		changed, e := teamsSweepOriginPartialsIn(ctx, tx, d, credential, scope)
		if e == nil && changed {
			t.Error("permanent hold repeatedly caused restart")
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	err = c.writeTransaction(ctx, "synthetic-attempt", func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO teams_origin_outbox(observation_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,run_id,run_generation,expires_at,token,expected_revision,observation_json,observation_sha256,state) VALUES('attempt',?,?,?,?,?,?,'','','old',1,1,1,1,'old-run',1,?,'token',0,'{}','hash','prepared')`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, time.Now().Unix()-1)
		if e != nil {
			return e
		}
		_, e = tx.Exec(`INSERT INTO teams_origin_attempts(observation_id,first_attempt_at) VALUES('attempt',?)`, time.Now().Unix())
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	// A v34 outbox row without any v35 attempt marker is ambiguous; a
	// v35 zero marker proves this implementation has not sent it yet.
	if _, err = c.DB.Exec(`DELETE FROM teams_origin_attempts WHERE observation_id='attempt'`); err != nil {
		t.Fatal(err)
	}
	ambiguous, e := teamsOriginAmbiguousIn(ctx, c.reader(), d, credential)
	if e != nil || !ambiguous {
		t.Fatal("legacy row without marker was treated as unattempted")
	}
	if _, err = c.DB.Exec(`INSERT INTO teams_origin_attempts(observation_id,first_attempt_at) VALUES('attempt',0)`); err != nil {
		t.Fatal(err)
	}
	ambiguous, e = teamsOriginAmbiguousIn(ctx, c.reader(), d, credential)
	if e != nil || ambiguous {
		t.Fatal("durable never-sent marker was treated as unknown")
	}
	if _, err = c.DB.Exec(`UPDATE teams_origin_attempts SET first_attempt_at=1 WHERE observation_id='attempt'`); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"prepared", "held"} {
		if _, err = c.DB.Exec(`UPDATE teams_origin_outbox SET state=? WHERE observation_id='attempt'`, state); err != nil {
			t.Fatal(err)
		}
		ambiguous, e := teamsOriginAmbiguousIn(ctx, c.reader(), d, credential)
		if e != nil || !ambiguous {
			t.Fatalf("%s attempt escaped: %v", state, e)
		}
	}
}

func TestTeamsOriginRetireMaximumCompleteScan(t *testing.T) {
	c, _ := testCatalog(t)
	if _, err := c.DB.Exec(teamsProtectedFormat35DDL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	credential := &teams.EnrollmentCredential{Origin: "https://synthetic.invalid", OrgID: "synthetic-org", PersonID: "synthetic-person", InstallationID: "00000000-0000-4000-8000-000000000003"}
	const run = "00000000-0000-4000-8000-000000000004"
	now := time.Now().UTC().Unix()
	start := sourcewire.RecoveryStartReceipt{Status: "proof_required", RunID: run, Generation: 1, ServerTime: now - 7200, ExpiresAt: now - 3600, ExternalProofRequired: true}
	startRaw, _ := json.Marshal(start)
	opaque := func(kind string, i int) string {
		sum := sha256.Sum256([]byte(fmt.Sprintf("retire-%s-%d", kind, i)))
		return base64.RawURLEncoding.EncodeToString(sum[:])
	}
	origins := make([]teamsRecoveryOrigin, 2000)
	for i := range origins {
		origins[i] = teamsRecoveryOrigin{Token: opaque("token", i), Witness: opaque("witness", i), Revision: 1, EvidenceRevision: opaque("evidence", i), Protection: "unknown", Classification: sourcewire.Classification{State: "unclassified"}}
	}
	sort.Slice(origins, func(i, j int) bool { return origins[i].Token < origins[j].Token })
	err := c.writeTransaction(ctx, "synthetic-maximum-retire", func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO teams_origin_runs(run_key,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,token,start_json,start_receipt_json,expires_at,state) VALUES('maximum',?,?,?,?,?,?,'','','hash',1,1,1,1,'batch','{}',?,?,'completed')`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, startRaw, start.ExpiresAt)
		if e != nil {
			return e
		}
		for p := 0; p < 4; p++ {
			cursor := ""
			if p > 0 {
				cursor = fmt.Sprintf("cursor-%d", p)
			}
			next := ""
			if p < 3 {
				next = fmt.Sprintf("cursor-%d", p+1)
			}
			request := sourcewire.RecoveryPageRequest{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: now - 7199, RunID: run, Family: "origins", Cursor: cursor}
			requestRaw, _ := json.Marshal(request)
			page := sourcewire.RecoveryPageReceipt{Status: "proof_required", RunID: run, Generation: 1, ServerTime: now - 7199, ExternalProofRequired: true, Family: "origins", Items: []json.RawMessage{}, NextCursor: next}
			for _, v := range origins[p*500 : (p+1)*500] {
				itemRaw, _ := json.Marshal(v)
				page.Items = append(page.Items, itemRaw)
				observation := sourcewire.ProtectionObservation{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: now - 7199, ExpectedRevision: 0, Revision: 1, Token: v.Token, Witness: v.Witness, Dependencies: []string{}, EvidenceRevision: v.EvidenceRevision, Protection: v.Protection, Classification: v.Classification}
				observation.ObservationID, e = sourcewire.ProtectionObservationID(observation, credential.InstallationID, d.ID)
				if e != nil {
					return e
				}
				body, hash, e := observation.Bytes()
				if e != nil {
					return e
				}
				receipt, _ := json.Marshal(sourcewire.ProtectionReceipt{Status: "accepted", ObservationID: observation.ObservationID, Revision: 1, ObservationSHA256: hash, PrivacyRevision: 1, OriginProtection: v.Protection})
				_, e = tx.Exec(`INSERT INTO teams_origin_outbox(observation_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,run_id,run_generation,expires_at,token,expected_revision,observation_json,observation_sha256,state,receipt_json) VALUES(?,?,?,?,?,?,?,'','','hash',1,1,1,1,?,1,?,?,0,?,?,'accepted',?)`, observation.ObservationID, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, run, start.ExpiresAt, v.Token, body, hash, receipt)
				if e != nil {
					return e
				}
			}
			pageRaw, _ := json.Marshal(page)
			if _, e = tx.Exec(`INSERT INTO teams_origin_run_pages(run_key,page_index,request_json,receipt_json) VALUES('maximum',?,?,?)`, p, requestRaw, pageRaw); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var maxPage, totalPages int64
	if err = c.DB.QueryRow(`SELECT max(length(receipt_json)),sum(length(request_json)+length(receipt_json)) FROM teams_origin_run_pages WHERE run_key='maximum'`).Scan(&maxPage, &totalPages); err != nil {
		t.Fatal(err)
	}
	t.Logf("synthetic 4x500 page maximum=%d requests+receipts=%d start_receipt=%d", maxPage, totalPages, len(startRaw))
	if err = c.writeTransaction(ctx, "synthetic-maximum-compact", func(tx *sql.Tx) error {
		n, e := teamsRetireRedundantOriginRunsIn(ctx, tx, d, credential, 1)
		if e != nil {
			return e
		}
		if n != 1 {
			return fmt.Errorf("retired %d runs", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox WHERE state='accepted'`); n != 2000 {
		t.Fatalf("accepted evidence changed: %d", n)
	}
}
