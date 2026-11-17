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
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true, Options: map[string]any{"synthetic-option": true}}
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
	hosts, err := c.capturedHosts(config.CaptureRoot)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("capture status: %v %v", hosts, err)
	}
	sources := hosts[0]["sources"].([]map[string]any)
	if len(sources) != 1 || sources[0]["needs_index"] != false {
		t.Fatalf("capture stayed stale: %v", sources)
	}
	appendSourceFile(t, filepath.Join(root, "sessions", "rollout.jsonl"), `{"type":"event_msg","payload":{"type":"agent_message","message":"synthetic"}}`+"\n")
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT COUNT(*) FROM teams_source_bindings`); n != 1 {
		t.Fatalf("live and captured custody produced different bindings: %d", n)
	}
	config.Sources[0].Options = map[string]any{"synthetic-option": false}
	runCapture(t, config)
	appendSourceFile(t, filepath.Join(root, "sessions", "rollout.jsonl"), `{"type":"event_msg","payload":{"type":"agent_message","message":"changed"}}`+"\n")
	runCapture(t, config)
	indexCaptures(t, c, config.CaptureRoot, "synthetic-host")
	if n := countRows(t, c, `SELECT COUNT(*) FROM teams_source_bindings WHERE superseded_at=''`); n != 0 {
		t.Fatalf("changed capture options retained active proof: %d", n)
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

func TestTeamsSourceProofRetainsDistinctOriginsAndFencesChangedSource(t *testing.T) {
	useHost(t, "synthetic-host")
	c, _ := testCatalog(t)
	ctx := context.Background()
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	credential := &teams.EnrollmentCredential{LibraryID: d.ID, Origin: "https://synthetic.invalid", OrgID: "synthetic-org", PersonID: "synthetic-person", InstallationID: "00000000-0000-4000-8000-000000000003"}
	s, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, credential.OrgID, credential.InstallationID, teamsPayloadHash([]byte("synthetic-key")), true, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(ctx, d, s.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "codex")
	writeSourceFile(t, filepath.Join(root, "sessions", "one.jsonl"), codexLines("same-thread", 1))
	writeSourceFile(t, filepath.Join(root, "archived_sessions", "two.jsonl"), codexLines("same-thread", 2))
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT COUNT(*) FROM teams_native_source_proofs`); n != 2 {
		t.Fatalf("lost distinct file origins: %d", n)
	}
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
	var mapping int64
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_mapping_current WHERE binding_id=?`, binding).Scan(&mapping); err != nil {
		t.Fatal(err)
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		t.Fatal(err)
	}
	err = c.writeTransaction(ctx, "synthetic-exposure", func(tx *sql.Tx) error { return reserveTeamsSourceExposure(ctx, tx, d, credential, binding, mapping) })
	lease.Close()
	if err != nil {
		t.Fatal(err)
	}
	newRoot := filepath.Join(t.TempDir(), "codex")
	writeSourceFile(t, filepath.Join(newRoot, "sessions", "three.jsonl"), codexLines("new-thread", 1))
	source.Path = newRoot
	ingestSource(t, c, source)
	var next string
	if err = c.DB.QueryRow(`SELECT binding_id FROM teams_source_bindings WHERE superseded_at=''`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next == binding || countRows(t, c, `SELECT COUNT(*) FROM teams_native_source_proofs WHERE binding_id=?`, binding) != 0 {
		t.Fatal("old source proof remained active")
	}
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err = c.reviewTeamsSourceMapping(ctx, d, credential, next, "work", revision, true); !errors.Is(err, errTeamsSourceRemapRequired) {
		t.Fatalf("changed source bypassed exposure: %v", err)
	}
	if _, err = c.DB.Exec(`DELETE FROM teams_native_source_proofs WHERE binding_id=?`, next); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&after); err != nil || after <= revision {
		t.Fatalf("deletion did not advance revision: %d -> %d, %v", revision, after, err)
	}
}

func TestTeamsSourceProofCascadeAdvancesRevision(t *testing.T) {
	c, _ := testCatalog(t)
	execTx(t, c.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Synthetic','2026-01-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','codex','local','native')`,
		`INSERT INTO teams_source_bindings(binding_id,provenance_sha256,host_id,source_name,source_kind,provider,account_label,source_root,created_at) VALUES('binding','hash','host','source','codex','codex','local','/synthetic','now')`,
		`INSERT INTO teams_native_source_proofs(binding_id,conversation_id,provider,native_id,status,trailing_partial,indexed_digest,origin) VALUES('binding','c','codex','native','capture_gap',0,'digest','/synthetic/file')`)
	var before, after int64
	if err := c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`DELETE FROM conversations WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	if err := c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&after); err != nil || after <= before {
		t.Fatalf("cascade did not advance revision %d -> %d: %v", before, after, err)
	}
}
