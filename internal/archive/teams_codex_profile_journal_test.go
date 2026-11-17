package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"encoding/base64"
	"github.com/gbdubs/pharos/internal/teams"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func TestTeamsOriginalProfileChain(t *testing.T) {
	opaque := func(b byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }
	const installation = "00000000-0000-4000-8000-000000000003"
	pages, err := teamsBuildCodexProfilePages(installation, "synthetic-library", "00000000-0000-4000-8000-000000000004", strings.Repeat("a", 64), 1, 1, 1794931200, "", []sourcewire.ProfileItem{{Token: opaque(1), Witness: opaque(2), Dependencies: []string{opaque(3)}, EvidenceRevision: opaque(4)}}, true, sourcewire.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = teamsValidateOriginalProfileChain(installation, "synthetic-library", pages.Pages, pages.Seal); err != nil {
		t.Fatal(err)
	}
	if _, err = teamsValidateOriginalProfileChain(installation, "other-library", pages.Pages, pages.Seal); err == nil {
		t.Fatal("foreign scope accepted")
	}
	corrupted := append([][]byte(nil), pages.Pages...)
	corrupted[0] = append(bytes.Clone(corrupted[0]), ' ')
	if _, err = teamsValidateOriginalProfileChain(installation, "synthetic-library", corrupted, pages.Seal); err == nil {
		t.Fatal("changed original page accepted")
	}
}

func TestTeamsProfileJournalExactScopeAndRetry(t *testing.T) {
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
	const snapshot = "00000000-0000-4000-8000-000000000005"
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
	config := json.RawMessage(`{"synthetic":true}`)
	work := teams.Work{ID: workID, VersionID: "synthetic-version", LibraryID: d.ID, Config: config, ConfigSHA256: teamsPayloadHash(config), EvaluationRevision: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	inventory := strings.Repeat("a", 64)
	generation, err := readCacheGenerations(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DB.Exec(`INSERT INTO teams_v2_uploads(work_id,version_id,library_id,instance_id,org_id,installation_id,config_sha256,snapshot_id,binding_id,inventory_revision,inventory_sha256,local_revision,evaluation_revision,settings_revision,source_revision,privacy_revision,generation,mode,policy_version,authorization_json,pages_json,seal_json,sealed_json,state) VALUES(?,?,?,?,?,?,?,?,?,1,?,?,1,?,0,0,?,'reviewed',1,'{}','[]','{}','{}','sealed')`, work.ID, work.VersionID, d.ID, d.Instance, org, installation, work.ConfigSHA256, snapshot, "synthetic-binding", inventory, settings.LocalRevision, settings.SettingsRevision, generation.all)
	if err != nil {
		t.Fatal(err)
	}
	opaque := func(b byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }
	evidence, err := teamsBuildCodexProfilePages(installation, d.ID, snapshot, inventory, 1, settings.LocalRevision, time.Now().UTC().Unix(), "", []sourcewire.ProfileItem{{Token: opaque(1), Witness: opaque(2), Dependencies: []string{opaque(3)}, EvidenceRevision: opaque(4)}}, true, sourcewire.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = c.journalTeamsProfile(ctx, d, credential, work, evidence.Pages, evidence.Seal); err != nil {
			t.Fatalf("exact retry: %v", err)
		}
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_profile_outbox WHERE state='prepared' AND seal_receipt_json IS NULL`); n != 1 {
		t.Fatalf("prepared count %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_profile_outbox_pages WHERE receipt_json IS NULL`); n != 1 {
		t.Fatalf("original pages count %d", n)
	}
	seal, err := sourcewire.DecodeProfileSeal(evidence.Seal)
	if err != nil {
		t.Fatal(err)
	}
	transport := &teamsTestProfileTransport{}
	if err = c.publishTeamsProfile(ctx, nil, d, credential, work, seal.UploadID); !errors.Is(err, errTeamsSourceUnavailable) {
		t.Fatalf("production publication gate opened: %v", err)
	}
	if err = c.publishTeamsProfileUnderGate(ctx, transport, d, credential, work, seal.UploadID); err != nil {
		t.Fatal(err)
	}
	if err = c.publishTeamsProfileUnderGate(ctx, transport, d, credential, work, seal.UploadID); err != nil || transport.pages != 1 || transport.seals != 1 {
		t.Fatalf("accepted retry sent again: %+v %v", transport, err)
	}
	held, err := teamsBuildCodexProfilePages(installation, d.ID, snapshot, inventory, 2, settings.LocalRevision, time.Now().UTC().Unix(), "", []sourcewire.ProfileItem{{Token: opaque(1), Witness: opaque(2), Dependencies: []string{opaque(3)}, EvidenceRevision: opaque(4)}}, true, sourcewire.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.journalTeamsProfile(ctx, d, credential, work, held.Pages, held.Seal); err != nil {
		t.Fatal(err)
	}
	heldSeal, _ := sourcewire.DecodeProfileSeal(held.Seal)
	heldTransport := &teamsTestProfileTransport{held: true}
	if err = c.publishTeamsProfileUnderGate(ctx, heldTransport, d, credential, work, heldSeal.UploadID); !errors.Is(err, errTeamsRecoveryFreshProfile) {
		t.Fatalf("held response: %v", err)
	}
	if err = c.publishTeamsProfileUnderGate(ctx, heldTransport, d, credential, work, heldSeal.UploadID); !errors.Is(err, errTeamsRecoveryFreshProfile) || heldTransport.pages != 1 || heldTransport.seals != 0 {
		t.Fatalf("held upload replayed: %+v %v", heldTransport, err)
	}
	resync, err := teamsBuildCodexProfilePages(installation, d.ID, snapshot, inventory, 3, settings.LocalRevision, time.Now().UTC().Unix(), "", []sourcewire.ProfileItem{{Token: opaque(1), Witness: opaque(2), Dependencies: []string{opaque(3)}, EvidenceRevision: opaque(4)}}, true, sourcewire.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.journalTeamsProfile(ctx, d, credential, work, resync.Pages, resync.Seal); err != nil {
		t.Fatal(err)
	}
	resyncSeal, _ := sourcewire.DecodeProfileSeal(resync.Seal)
	resyncTransport := &teamsTestProfileTransport{resync: true}
	if err = c.publishTeamsProfileUnderGate(ctx, resyncTransport, d, credential, work, resyncSeal.UploadID); !errors.Is(err, errTeamsRecoveryFreshProfile) {
		t.Fatalf("resync response: %v", err)
	}
	if err = c.publishTeamsProfileUnderGate(ctx, resyncTransport, d, credential, work, resyncSeal.UploadID); !errors.Is(err, errTeamsRecoveryFreshProfile) || resyncTransport.pages != 1 {
		t.Fatalf("resync upload replayed: %+v %v", resyncTransport, err)
	}
	now := time.Now().UTC().Unix()
	run := sourcewire.RecoveryStartReceipt{Status: "proof_required", RunID: "00000000-0000-4000-8000-000000000006", Generation: 1, ServerTime: now, ExpiresAt: now + 3600, UnacceptedFloor: now - 1, ExternalProofRequired: true}
	first, err := c.journalTeamsRecoveryProfileStages(ctx, d, credential, work, run, seal.UploadID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	run.UnacceptedFloor = now + 1000
	again, err := c.journalTeamsRecoveryProfileStages(ctx, d, credential, work, run, seal.UploadID, 0, "")
	if err != nil || len(first) != len(again) || !bytes.Equal(first[0], again[0]) {
		t.Fatalf("stage exact retry changed: %v", err)
	}
	tampered, e := sourcewire.DecodeRecoveryStagePage(first[0])
	if e != nil {
		t.Fatal(e)
	}
	tampered.Items[0].Witness = opaque(9)
	bad, hash, e := tampered.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.DB.Exec(`UPDATE teams_recovery_stage_pages SET page_json=?,page_sha256=? WHERE run_id=? AND installation_id=? AND upload_id=? AND page_index=0`, bad, hash, run.RunID, installation, seal.UploadID); e != nil {
		t.Fatal(e)
	}
	if _, e = c.journalTeamsRecoveryProfileStages(ctx, d, credential, work, run, seal.UploadID, 0, ""); !errors.Is(e, errTeamsConsentStale) {
		t.Fatalf("changed original stage accepted: %v", e)
	}
	if _, e = c.DB.Exec(`UPDATE teams_recovery_stage_pages SET page_json=?,page_sha256=? WHERE run_id=? AND installation_id=? AND upload_id=? AND page_index=0`, first[0], sourcewire.RecoveryStagePageDigest(first[0]), run.RunID, installation, seal.UploadID); e != nil {
		t.Fatal(e)
	}
	if _, err = c.DB.Exec(`UPDATE teams_profile_outbox SET expires_at=? WHERE upload_id=?`, now-1, seal.UploadID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.journalTeamsRecoveryProfileStages(ctx, d, credential, work, run, seal.UploadID, 0, ""); !errors.Is(err, errTeamsRecoveryFreshProfile) {
		t.Fatalf("expired original accepted: %v", err)
	}
	_, err = c.DB.Exec(`UPDATE teams_source_revision SET revision=revision+1 WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.journalTeamsProfile(ctx, d, credential, work, evidence.Pages, evidence.Seal); !errors.Is(err, errTeamsSnapshotChanged) {
		t.Fatalf("stale proof accepted: %v", err)
	}
	if _, err = c.DB.Exec(`DELETE FROM teams_protection_policy_ack`); err != nil {
		t.Fatal(err)
	}
	if err = c.publishTeamsProfileUnderGate(ctx, transport, d, credential, work, seal.UploadID); !errors.Is(err, errTeamsConsentStale) || transport.pages != 1 || transport.seals != 1 {
		t.Fatalf("publication without local acknowledgement: %v", err)
	}
}

func TestTeamsRecoveryProfileStageReconstructsOriginal(t *testing.T) {
	opaque := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return base64.RawURLEncoding.EncodeToString(sum[:])
	}
	const installation = "00000000-0000-4000-8000-000000000003"
	const snapshot = "00000000-0000-4000-8000-000000000004"
	const run = "00000000-0000-4000-8000-000000000005"
	var items []sourcewire.ProfileItem
	for i := 0; i < 600; i++ {
		items = append(items, sourcewire.ProfileItem{Token: opaque(fmt.Sprintf("token-%d", i)), Witness: opaque(fmt.Sprintf("witness-%d", i)), Dependencies: []string{opaque(fmt.Sprintf("parent-%d", i))}, EvidenceRevision: opaque(fmt.Sprintf("evidence-%d", i))})
	}
	original, err := teamsBuildCodexProfilePages(installation, "synthetic-library", snapshot, strings.Repeat("a", 64), 1, 1, 1794931200, "", items, true, sourcewire.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := teamsBuildRecoveryProfileStages(installation, "synthetic-library", run, 1794931201, 0, "", original.Pages, original.Seal)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 2 {
		t.Fatalf("chunk count %d", len(stages))
	}
	page, err := sourcewire.DecodeProfilePage(original.Pages[0])
	if err != nil {
		t.Fatal(err)
	}
	var rebuilt []sourcewire.ProfileItem
	previous := ""
	for i, raw := range stages {
		stage, e := sourcewire.DecodeRecoveryStagePage(raw)
		if e != nil {
			t.Fatal(e)
		}
		if i > 0 && (stage.PreviousPageSHA256 == nil || *stage.PreviousPageSHA256 != previous) {
			t.Fatal("stage chain broken")
		}
		previous = sourcewire.RecoveryStagePageDigest(raw)
		for _, item := range stage.Items {
			rebuilt = append(rebuilt, sourcewire.ProfileItem{Token: item.Token, Witness: item.Witness, Dependencies: item.Dependencies, EvidenceRevision: item.EvidenceRevision})
		}
		if stage.Final != (i == len(stages)-1) {
			t.Fatal("final marker wrong")
		}
	}
	if !reflect.DeepEqual(rebuilt, page.Items) {
		t.Fatal("recovery stages changed original evidence")
	}
}

type teamsTestProfileTransport struct {
	pages, seals int
	held         bool
	resync       bool
}

func (f *teamsTestProfileTransport) StageSourceProfilePage(_ context.Context, _ string, raw []byte) (teams.ProfilePageReceipt, error) {
	p, err := sourcewire.DecodeProfilePage(raw)
	if err != nil {
		return teams.ProfilePageReceipt{}, err
	}
	f.pages++
	if f.resync {
		return teams.ProfilePageReceipt{}, &teams.HTTPError{Status: 409, Code: "source_resync_required"}
	}
	status := "accepted"
	if f.held {
		status = "held"
	}
	return teams.ProfilePageReceipt{Status: status, SnapshotID: p.SnapshotID, ProfileID: p.ProfileID, ProfileVersion: p.ProfileVersion, ProfileRevision: p.ProfileRevision, Index: p.Index, PageSHA256: sourcewire.ProfilePageDigest(raw)}, nil
}
func (f *teamsTestProfileTransport) SealSourceProfile(_ context.Context, _ string, raw []byte) (teams.ProfileSealReceipt, error) {
	s, err := sourcewire.DecodeProfileSeal(raw)
	if err != nil {
		return teams.ProfileSealReceipt{}, err
	}
	f.seals++
	return teams.ProfileSealReceipt{Status: "accepted", SnapshotID: s.SnapshotID, ProfileID: s.ProfileID, ProfileVersion: s.ProfileVersion, ProfileRevision: s.ProfileRevision, SealSHA256: sourcewire.ProfileSealDigest(raw), Complete: s.Complete, ItemCount: s.ItemCount}, nil
}
