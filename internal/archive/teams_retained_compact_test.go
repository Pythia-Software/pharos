package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
	"testing"
)

func TestTeamsReceiptSupersessionPreservesProtection(t *testing.T) {
	old := sourcewire.ProtectionObservation{Token: "t", Witness: "w", Revision: 1, IssuedAt: 10, Protection: "non_private"}
	latest := sourcewire.ProtectionObservation{Token: "t", Witness: "w", Revision: 2, IssuedAt: 11, Protection: "explicit_private", EvidenceRevision: "e"}
	current := teamsRecoveryOrigin{Token: "t", Witness: "w", Revision: 2, Protection: "explicit_private", EvidenceRevision: "e"}
	if !teamsReceiptSupersedes(old, latest, current) {
		t.Fatal("safe restriction did not supersede")
	}
	private := old
	private.Protection = "explicit_private"
	public := latest
	public.Protection = "non_private"
	reported := current
	reported.Protection = "non_private"
	if teamsReceiptSupersedes(private, public, reported) {
		t.Fatal("privacy downgrade compacted")
	}
	wrong := current
	wrong.Witness = "other"
	if teamsReceiptSupersedes(old, latest, wrong) {
		t.Fatal("unrelated witness compacted")
	}
	wrong = current
	wrong.EvidenceRevision = "different"
	if teamsReceiptSupersedes(old, latest, wrong) {
		t.Fatal("unproved evidence compacted")
	}
	later := old
	later.IssuedAt = 12
	if teamsReceiptSupersedes(later, latest, current) {
		t.Fatal("issuance history regressed")
	}
}

func TestTeamsRetainedReceiptCompactionKeepsLatest(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	raw, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://synthetic.invalid", "org_id": "00000000-0000-4000-8000-000000000001", "person_id": "00000000-0000-4000-8000-000000000002", "installation_id": "00000000-0000-4000-8000-000000000003", "library_id": d.ID, "library_instance": d.Instance, "application_id": "synthetic-app", "private_key": base64.RawURLEncoding.EncodeToString(key), "source_token_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "metrics_default": map[string]any{"value": false, "policy_version": 1}})
	credential, err := teams.ParseCredential(raw, d.ID, d.Instance, "synthetic-app", false)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	witness := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	evidence := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var lastID string
	for n := int64(1); n <= 1000; n++ {
		v := sourcewire.ProtectionObservation{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: n, ExpectedRevision: n - 1, Revision: n, Token: token, Witness: witness, EvidenceRevision: evidence, Protection: "explicit_private", Dependencies: []string{}, Classification: sourcewire.Classification{State: "unclassified"}}
		v.ObservationID, err = sourcewire.ProtectionObservationID(v, credential.InstallationID, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		body, hash, e := v.Bytes()
		if e != nil {
			t.Fatal(e)
		}
		receipt, _ := json.Marshal(sourcewire.ProtectionReceipt{Status: "accepted", ObservationID: v.ObservationID, Revision: n, ObservationSHA256: hash, PrivacyRevision: n, OriginProtection: v.Protection})
		_, e = tx.Exec(`INSERT INTO teams_origin_outbox(observation_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,run_id,run_generation,expires_at,token,expected_revision,observation_json,observation_sha256,state,receipt_json) VALUES(?,?,?,?,?,?,?,'','','synthetic',1,1,1,1,'run',1,9999999999,?,?,?,?,'accepted',?)`, v.ObservationID, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, token, n-1, body, hash, receipt)
		if e != nil {
			t.Fatal(e)
		}
		lastID = v.ObservationID
	}
	scan := &teamsRecoveryOriginScan{byToken: map[string]teamsRecoveryOrigin{token: {Token: token, Witness: witness, Revision: 1000, Protection: "explicit_private", EvidenceRevision: evidence}}}
	if err = teamsCompactRetainedReceiptsIn(ctx, tx, d, credential, scan); err != nil {
		t.Fatal(err)
	}
	var count, latest int
	if err = tx.QueryRow(`SELECT count(*),sum(observation_id=?) FROM teams_origin_outbox`, lastID).Scan(&count, &latest); err != nil || count != 992 || latest != 1 {
		t.Fatalf("bounded receipt compaction: %d %d %v", count, latest, err)
	}
}
