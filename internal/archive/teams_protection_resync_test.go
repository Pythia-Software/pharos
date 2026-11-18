package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

func TestTeamsRecoveryOriginScanRequiresCompleteCursorChain(t *testing.T) {
	opaque := func(b byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }
	const run = "00000000-0000-4000-8000-000000000003"
	start := sourcewire.RecoveryStartReceipt{Status: "proof_required", RunID: run, Generation: 3, ServerTime: 100, ExpiresAt: 200, UnacceptedFloor: 90, ExternalProofRequired: true}
	requests := []sourcewire.RecoveryPageRequest{{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: 100, RunID: run, Family: "origins"}, {Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: 101, RunID: run, Family: "origins", Cursor: "next"}}
	origin := teamsRecoveryOrigin{Token: opaque(1), Witness: opaque(2), Revision: 4, EvidenceRevision: opaque(3), Protection: "unknown", Classification: sourcewire.Classification{State: "unclassified"}}
	raw, _ := json.Marshal(origin)
	pages := []sourcewire.RecoveryPageReceipt{{Status: "proof_required", RunID: run, Generation: 3, ServerTime: 101, UnacceptedFloor: 90, ExternalProofRequired: true, Family: "origins", Items: []json.RawMessage{raw}, NextCursor: "next"}, {Status: "proof_required", RunID: run, Generation: 3, ServerTime: 102, UnacceptedFloor: 91, ExternalProofRequired: true, Family: "origins", Items: []json.RawMessage{}}}
	if rev, protection, err := teamsOriginRevisionFromRecoveryScan(start, requests, pages, origin.Token, origin.Witness); err != nil || rev != 4 || protection != "unknown" {
		t.Fatalf("complete scan: %d %q %v", rev, protection, err)
	}
	if _, _, err := teamsOriginRevisionFromRecoveryScan(start, requests[:1], pages[:1], origin.Token, origin.Witness); err == nil {
		t.Fatal("partial prefix accepted")
	}
	wrongRequests := append([]sourcewire.RecoveryPageRequest(nil), requests...)
	wrongRequests[1].Cursor = "other"
	if _, _, err := teamsOriginRevisionFromRecoveryScan(start, wrongRequests, pages, origin.Token, origin.Witness); err == nil {
		t.Fatal("cursor splice accepted")
	}
	wrongPages := append([]sourcewire.RecoveryPageReceipt(nil), pages...)
	wrongPages[1].UnacceptedFloor = 89
	if _, _, err := teamsOriginRevisionFromRecoveryScan(start, requests, wrongPages, origin.Token, origin.Witness); err == nil {
		t.Fatal("floor rollback accepted")
	}
	wrongPages = append([]sourcewire.RecoveryPageReceipt(nil), pages...)
	wrongPages[1].Items = []json.RawMessage{raw}
	if _, _, err := teamsOriginRevisionFromRecoveryScan(start, requests, wrongPages, origin.Token, origin.Witness); err == nil {
		t.Fatal("cross-page duplicate accepted")
	}
	// All-zero opaque values are valid and must not collide with an
	// implementation sentinel used while validating a whole scan.
	zeros := origin
	zeros.Token = opaque(0)
	zeroRaw, _ := json.Marshal(zeros)
	zeroPage := pages[1]
	zeroPage.Items = []json.RawMessage{zeroRaw}
	if _, err := teamsBuildRecoveryOriginScan(start, requests[:1], []sourcewire.RecoveryPageReceipt{zeroPage}); err != nil {
		t.Fatalf("valid zero token: %v", err)
	}
	heldPages := append([]sourcewire.RecoveryPageReceipt(nil), pages...)
	heldPages[0].Held = true
	heldPages[0].Status = "held"
	scan, err := teamsBuildRecoveryOriginScan(start, requests, heldPages)
	if err != nil || !scan.held {
		t.Fatalf("earlier hold lost: %v", err)
	}
	emptyPages := append([]sourcewire.RecoveryPageReceipt(nil), pages...)
	emptyPages[0].Items = []json.RawMessage{}
	if _, err = teamsBuildRecoveryOriginScan(start, requests, emptyPages); err == nil {
		t.Fatal("empty non-final page accepted")
	}
	other := origin
	other.Token = opaque(4)
	other.Witness = origin.Witness
	otherRaw, _ := json.Marshal(other)
	duplicateWitness := append([]sourcewire.RecoveryPageReceipt(nil), pages...)
	duplicateWitness[1].Items = []json.RawMessage{otherRaw}
	if _, err = teamsBuildRecoveryOriginScan(start, requests, duplicateWitness); err == nil {
		t.Fatal("duplicate witness accepted")
	}
	wrongPages = append([]sourcewire.RecoveryPageReceipt(nil), pages...)
	wrongPages[1].NextCursor = "later"
	if _, _, err := teamsOriginRevisionFromRecoveryScan(start, requests, wrongPages, origin.Token, origin.Witness); err == nil {
		t.Fatal("unfinished scan accepted")
	}
}

func TestTeamsRecoveryOriginScanMaximumBound(t *testing.T) {
	const run = "00000000-0000-4000-8000-000000000003"
	start := sourcewire.RecoveryStartReceipt{Status: "proof_required", RunID: run, Generation: 1, ServerTime: 100, ExpiresAt: 200, ExternalProofRequired: true}
	var origins []teamsRecoveryOrigin
	opaque := func(kind string, i int) string {
		v := sha256.Sum256([]byte(fmt.Sprintf("synthetic-%s-%d", kind, i)))
		return base64.RawURLEncoding.EncodeToString(v[:])
	}
	for i := 0; i < 2000; i++ {
		origins = append(origins, teamsRecoveryOrigin{Token: opaque("token", i), Witness: opaque("witness", i), Revision: 1, EvidenceRevision: opaque("evidence", i), Protection: "unknown", Classification: sourcewire.Classification{State: "unclassified"}})
	}
	sort.Slice(origins, func(i, j int) bool { return origins[i].Token < origins[j].Token })
	var requests []sourcewire.RecoveryPageRequest
	var pages []sourcewire.RecoveryPageReceipt
	for i := 0; i < 4; i++ {
		cursor := ""
		if i > 0 {
			cursor = fmt.Sprintf("cursor-%d", i)
		}
		next := ""
		if i < 3 {
			next = fmt.Sprintf("cursor-%d", i+1)
		}
		requests = append(requests, sourcewire.RecoveryPageRequest{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: 100, RunID: run, Family: "origins", Cursor: cursor})
		p := sourcewire.RecoveryPageReceipt{Status: "proof_required", RunID: run, Generation: 1, ServerTime: 101, ExternalProofRequired: true, Family: "origins", Items: []json.RawMessage{}, NextCursor: next}
		for _, origin := range origins[i*500 : (i+1)*500] {
			raw, _ := json.Marshal(origin)
			p.Items = append(p.Items, raw)
		}
		pages = append(pages, p)
	}
	scan, err := teamsBuildRecoveryOriginScan(start, requests, pages)
	if err != nil || len(scan.byToken) != 2000 || len(scan.byWitness) != 2000 {
		t.Fatalf("maximum scan: %v", err)
	}
	for _, v := range origins {
		rev, _, e := scan.lookup(v.Token, v.Witness)
		if e != nil || rev != 1 {
			t.Fatalf("lookup: %v", e)
		}
	}
	requests = append(requests, requests[3])
	pages = append(pages, pages[3])
	if _, err = teamsBuildRecoveryOriginScan(start, requests, pages); err == nil {
		t.Fatal("fifth page accepted")
	}
	oversized := pages[:1]
	oversized[0].Items = []json.RawMessage{make([]byte, teamsRecoveryMaxOriginBytes+1)}
	oversized[0].NextCursor = ""
	if _, err = teamsBuildRecoveryOriginScan(start, requests[:1], oversized); err == nil {
		t.Fatal("oversized receipt accepted")
	}
}
