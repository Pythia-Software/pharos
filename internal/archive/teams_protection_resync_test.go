package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func TestTeamsRecoveryOriginRequiresCompleteAbsence(t *testing.T) {
	opaque := func(b byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }
	token, witness := opaque(1), opaque(2)
	const run = "00000000-0000-4000-8000-000000000003"
	start := sourcewire.RecoveryStartReceipt{Status: "proof_required", RunID: run, Generation: 3, ServerTime: 100, ExpiresAt: 200, UnacceptedFloor: 90, ExternalProofRequired: true}
	request := sourcewire.RecoveryPageRequest{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: 100, RunID: run, Family: "origins"}
	page := sourcewire.RecoveryPageReceipt{Status: "proof_required", RunID: run, Generation: 3, ServerTime: 101, UnacceptedFloor: 90, ExternalProofRequired: true, Family: "origins", Items: []json.RawMessage{}}
	if revision, _, err := teamsOriginRevisionFromRecoveryPage(start, request, page, token, witness); err != nil || revision != 0 {
		t.Fatalf("empty complete origin scan: %d %v", revision, err)
	}
	page.NextCursor = "next"
	if _, _, err := teamsOriginRevisionFromRecoveryPage(start, request, page, token, witness); err == nil {
		t.Fatal("partial page proved absence")
	}
	page.NextCursor = ""
	request.Cursor = "prior"
	if _, _, err := teamsOriginRevisionFromRecoveryPage(start, request, page, token, witness); err == nil {
		t.Fatal("suffix page proved absence")
	}
	request.Cursor = ""
	origin := teamsRecoveryOrigin{Token: token, Witness: witness, Revision: 4, EvidenceRevision: opaque(3), Protection: "flagged", Classification: sourcewire.Classification{State: "unclassified"}}
	body, _ := json.Marshal(origin)
	page.Items = []json.RawMessage{body}
	page.NextCursor = "next"
	if _, _, err := teamsOriginRevisionFromRecoveryPage(start, request, page, token, witness); err == nil {
		t.Fatal("partial matching page was accepted")
	}
	page.NextCursor = ""
	if revision, protection, err := teamsOriginRevisionFromRecoveryPage(start, request, page, token, witness); err != nil || revision != 4 || protection != "flagged" {
		t.Fatalf("matching prior: %d %q %v", revision, protection, err)
	}
	page.Items = append(page.Items, body)
	if _, _, err := teamsOriginRevisionFromRecoveryPage(start, request, page, token, witness); err == nil {
		t.Fatal("duplicate origin accepted")
	}
	page.NextCursor = ""
	origin.Witness = opaque(4)
	body, _ = json.Marshal(origin)
	page.Items = []json.RawMessage{body}
	if _, _, err := teamsOriginRevisionFromRecoveryPage(start, request, page, token, witness); err == nil {
		t.Fatal("conflicting witness accepted as new origin")
	}
	origin.Token, origin.Witness = opaque(4), witness
	body, _ = json.Marshal(origin)
	page.Items = []json.RawMessage{body}
	if _, _, err := teamsOriginRevisionFromRecoveryPage(start, request, page, token, witness); err == nil {
		t.Fatal("conflicting token accepted as new origin")
	}
}

func TestTeamsOriginJournalUsesCurrentAnchorAndPolicy(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const org = "00000000-0000-4000-8000-000000000001"
	const person = "00000000-0000-4000-8000-000000000002"
	const installation = "00000000-0000-4000-8000-000000000003"
	const workID = "00000000-0000-4000-8000-000000000004"
	const snapshotID = "00000000-0000-4000-8000-000000000005"
	const runID = "00000000-0000-4000-8000-000000000006"
	credentialRaw, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://synthetic.invalid", "org_id": org, "person_id": person, "installation_id": installation, "library_id": d.ID, "library_instance": d.Instance, "application_id": "test-app", "private_key": base64.RawURLEncoding.EncodeToString(key), "source_token_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "metrics_default": map[string]any{"value": false, "policy_version": 1}})
	credential, err := teams.ParseCredential(credentialRaw, d.ID, d.Instance, "test-app", false)
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
	generation, err := readCacheGenerations(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	opaque := func(b byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }
	snapshot := teamsV2Inventory{Items: []sourcewire.Item{{Kind: "codex_thread", Token: opaque(1), Witness: opaque(2), Profile: &sourcewire.Profile{ID: "codex_thread_usage", Version: 1}, EvidenceRevision: opaque(3), Protection: "private", Dependencies: []string{}}}, SourceRevision: sourceRev, PrivacyRevision: privacyRev, Generation: generation.all}
	pages, err := teamsBuildV2Pages(snapshot, snapshotID, settings.LocalRevision, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	pagesJSON, _ := json.Marshal(pages.Pages)
	config := json.RawMessage(`{"synthetic":true}`)
	work := teams.Work{ID: workID, VersionID: "synthetic-version", LibraryID: d.ID, Config: config, ConfigSHA256: teamsPayloadHash(config), EvaluationRevision: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	_, err = c.DB.Exec(`INSERT INTO teams_v2_uploads(work_id,version_id,library_id,instance_id,org_id,installation_id,config_sha256,snapshot_id,binding_id,inventory_revision,inventory_sha256,local_revision,evaluation_revision,settings_revision,source_revision,privacy_revision,generation,mode,policy_version,authorization_json,pages_json,seal_json,sealed_json,state) VALUES(?,?,?,?,?,?,?,?,?,1,?,?,1,?,?,?,?,'reviewed',1,'{}',?,?,'{}','sealed')`, work.ID, work.VersionID, d.ID, d.Instance, org, installation, work.ConfigSHA256, snapshotID, "synthetic-binding", pages.InventorySHA256, settings.LocalRevision, settings.SettingsRevision, sourceRev, privacyRev, generation.all, pagesJSON, pages.Seal)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	start := sourcewire.RecoveryStartReceipt{Status: "proof_required", RunID: runID, Generation: 1, ServerTime: now, ExpiresAt: now + 3600, UnacceptedFloor: now - 1, ExternalProofRequired: true}
	request := sourcewire.RecoveryPageRequest{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: now, RunID: runID, Family: "origins"}
	page := sourcewire.RecoveryPageReceipt{Status: "proof_required", RunID: runID, Generation: 1, ServerTime: now, UnacceptedFloor: now - 1, ExternalProofRequired: true, Family: "origins", Items: []json.RawMessage{}}
	raw, err := c.journalTeamsOrigin(ctx, d, credential, work, snapshot, opaque(1), start, request, page)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := sourcewire.DecodeProtectionObservation(raw)
	if err != nil || observation.Protection != "explicit_private" || observation.Revision != 1 {
		t.Fatalf("origin: %+v %v", observation, err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox WHERE state='prepared' AND receipt_json IS NULL`); n != 1 {
		t.Fatalf("origin count %d", n)
	}
	page.UnacceptedFloor = now + 1
	retry, err := c.journalTeamsOrigin(ctx, d, credential, work, snapshot, opaque(1), start, request, page)
	if err != nil || !bytes.Equal(retry, raw) {
		t.Fatalf("origin retry changed bytes: %v", err)
	}
	page.UnacceptedFloor = now - 1
	transport := &teamsTestOriginTransport{}
	if err = c.publishTeamsOrigin(ctx, nil, d, credential, work, observation.ObservationID); !errors.Is(err, errTeamsSourceUnavailable) {
		t.Fatalf("origin production gate opened: %v", err)
	}
	if err = c.publishTeamsOriginUnderGate(ctx, transport, d, credential, work, observation.ObservationID); err != nil {
		t.Fatal(err)
	}
	if err = c.publishTeamsOriginUnderGate(ctx, transport, d, credential, work, observation.ObservationID); err != nil || transport.calls != 1 {
		t.Fatalf("origin resent: %+v %v", transport, err)
	}
	start.RunID = "00000000-0000-4000-8000-000000000007"
	request.RunID, page.RunID = start.RunID, start.RunID
	heldRaw, err := c.journalTeamsOrigin(ctx, d, credential, work, snapshot, opaque(1), start, request, page)
	if err != nil {
		t.Fatal(err)
	}
	heldObservation, _ := sourcewire.DecodeProtectionObservation(heldRaw)
	heldTransport := &teamsTestOriginTransport{held: true}
	if err = c.publishTeamsOriginUnderGate(ctx, heldTransport, d, credential, work, heldObservation.ObservationID); !errors.Is(err, errTeamsRecoveryFreshProfile) {
		t.Fatalf("held origin: %v", err)
	}
	if err = c.publishTeamsOriginUnderGate(ctx, heldTransport, d, credential, work, heldObservation.ObservationID); !errors.Is(err, errTeamsRecoveryFreshProfile) || heldTransport.calls != 1 {
		t.Fatalf("held origin replayed: %+v %v", heldTransport, err)
	}
	prior := teamsRecoveryOrigin{Token: opaque(1), Witness: opaque(2), Revision: 1, EvidenceRevision: opaque(3), Protection: "flagged", Classification: sourcewire.Classification{State: "unclassified"}}
	body, _ := json.Marshal(prior)
	page.Items = []json.RawMessage{body}
	if _, err = c.journalTeamsOrigin(ctx, d, credential, work, snapshot, opaque(1), start, request, page); !errors.Is(err, errTeamsSourceUnavailable) {
		t.Fatalf("flagged origin downgraded: %v", err)
	}
	page.Items = []json.RawMessage{}
	if _, err = c.DB.Exec(`DELETE FROM teams_protection_policy_ack`); err != nil {
		t.Fatal(err)
	}
	if _, err = c.journalTeamsOrigin(ctx, d, credential, work, snapshot, opaque(1), start, request, page); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("missing policy accepted: %v", err)
	}
	if err = c.publishTeamsOriginUnderGate(ctx, transport, d, credential, work, observation.ObservationID); !errors.Is(err, errTeamsConsentStale) || transport.calls != 1 {
		t.Fatalf("origin sent without policy: %v", err)
	}
}

type teamsTestOriginTransport struct {
	calls int
	held  bool
}

func (f *teamsTestOriginTransport) ObserveSourceProtection(_ context.Context, _ string, raw []byte) (sourcewire.ProtectionReceipt, error) {
	v, err := sourcewire.DecodeProtectionObservation(raw)
	if err != nil {
		return sourcewire.ProtectionReceipt{}, err
	}
	f.calls++
	if f.held {
		return sourcewire.ProtectionReceipt{Status: "held", ObservationID: v.ObservationID, ObservationSHA256: sourcewire.ProtectionDigest(raw), SafetyPending: true}, nil
	}
	return sourcewire.ProtectionReceipt{Status: "accepted", ObservationID: v.ObservationID, Revision: v.Revision, ObservationSHA256: sourcewire.ProtectionDigest(raw), PrivacyRevision: 1, OriginProtection: v.Protection}, nil
}
