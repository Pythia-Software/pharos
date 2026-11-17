package archive

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestTeamsSourceProofRequiresOptInAndReindexes(t *testing.T) {
	useHost(t, "synthetic-host")
	c, _ := testCatalog(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "codex")
	writeSourceFile(t, filepath.Join(root, "sessions", "rollout.jsonl"), codexLines("synthetic-thread", 1)+`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":123},"last_token_usage":{"total_tokens":123}}}}`+"\n")
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT COUNT(*) FROM teams_native_source_proofs`); n != 0 {
		t.Fatalf("ordinary indexing recorded %d Teams proofs", n)
	}
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	s, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), true, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(ctx, d, s.SettingsRevision-1); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("stale setup accepted: %v", err)
	}
	if err = c.requestTeamsSourceProof(ctx, d, s.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	ingestSource(t, c, source)
	var status, native, bindingRoot, digest string
	var total int64
	if err = c.DB.QueryRow(`SELECT p.status,p.native_id,b.source_root,p.total_tokens,p.total_digest FROM teams_native_source_proofs p JOIN teams_source_bindings b ON b.binding_id=p.binding_id`).Scan(&status, &native, &bindingRoot, &total, &digest); err != nil {
		t.Fatal(err)
	}
	if status != "header_independent_unvalidated" || native != "synthetic-thread" || bindingRoot != root || total != 123 || len(digest) != 64 {
		t.Fatalf("unexpected parser observation: %q %q %q", status, native, bindingRoot)
	}
	var before, after int64
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	ingestSource(t, c, source)
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("unchanged source advanced revision: %d -> %d", before, after)
	}
}

func TestTeamsSourceProofRetainedCaptureUsesOriginalProvenance(t *testing.T) {
	useHost(t, "synthetic-host")
	c, config := testCatalog(t)
	root := filepath.Join(t.TempDir(), "codex")
	writeSourceFile(t, filepath.Join(root, "sessions", "rollout.jsonl"), codexLines("synthetic-thread", 1))
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	runCapture(t, config)
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	s, err := c.setTeamsLocalSettings(t.Context(), d.ID, d.Instance, "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), true, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(t.Context(), d, s.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	indexCaptures(t, c, config.CaptureRoot, "synthetic-host")
	var original, status string
	if err = c.DB.QueryRow(`SELECT b.source_root,p.status FROM teams_source_bindings b JOIN teams_native_source_proofs p ON p.binding_id=b.binding_id`).Scan(&original, &status); err != nil {
		t.Fatal(err)
	}
	if original != root || status != "header_independent_unvalidated" {
		t.Fatalf("capture provenance %q, status %q", original, status)
	}
}

func TestTeamsSourceMappingRequiresAttestationAndFencesExposure(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	credential := &teams.EnrollmentCredential{LibraryID: d.ID, Origin: "https://synthetic.invalid", OrgID: "synthetic-org", PersonID: "synthetic-person", InstallationID: "00000000-0000-4000-8000-000000000003"}
	if _, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, credential.OrgID, credential.InstallationID, teamsPayloadHash([]byte("synthetic-key")), true, false, 1); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `INSERT INTO teams_source_bindings(binding_id,provenance_sha256,host_id,source_name,source_kind,provider,account_label,source_root,created_at) VALUES('binding','hash','host','source','codex','codex','local','/synthetic','now')`)
	if err := c.reviewTeamsSourceMapping(ctx, d, credential, "binding", "work", 0, false); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("unattested work accepted: %v", err)
	}
	if err := c.reviewTeamsSourceMapping(ctx, d, credential, "binding", "work", 0, true); err != nil {
		t.Fatal(err)
	}
	if err := c.reviewTeamsSourceMapping(ctx, d, credential, "binding", "unavailable", 0, false); !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("stale review accepted: %v", err)
	}
	var mapping int64
	if err := c.DB.QueryRow(`SELECT revision FROM teams_source_mapping_current WHERE binding_id='binding'`).Scan(&mapping); err != nil {
		t.Fatal(err)
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		t.Fatal(err)
	}
	wrongPerson := *credential
	wrongPerson.PersonID = "other-person"
	if err = c.writeTransaction(ctx, "synthetic-wrong-person", func(tx *sql.Tx) error {
		return reserveTeamsSourceExposure(ctx, tx, d, &wrongPerson, "binding", mapping)
	}); !errors.Is(err, errTeamsConsentStale) {
		lease.Close()
		t.Fatalf("different person reserved mapping: %v", err)
	}
	err = c.writeTransaction(ctx, "synthetic-exposure", func(tx *sql.Tx) error { return reserveTeamsSourceExposure(ctx, tx, d, credential, "binding", mapping) })
	lease.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.reviewTeamsSourceMapping(ctx, d, credential, "binding", "work", 1, true); !errors.Is(err, errTeamsSourceRemapRequired) {
		t.Fatalf("exposed mapping edited: %v", err)
	}
	if err := c.reviewTeamsSourceMapping(ctx, d, credential, "binding", "unavailable", 1, false); err != nil {
		t.Fatal(err)
	}
	if err := c.reviewTeamsSourceMapping(ctx, d, credential, "binding", "work", 2, true); !errors.Is(err, errTeamsSourceRemapRequired) {
		t.Fatalf("unavailable bypassed exposure: %v", err)
	}
}
