package archive

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"

	"github.com/gbdubs/pharos/internal/platform/credentialstore"
	"testing"
	"time"
)

func TestTeamsProtectionProjectionTracksMetadata(t *testing.T) {
	c, _ := testCatalog(t)
	digest := func() string {
		t.Helper()
		tx, err := c.DB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		v, err := teamsProtectionProjection(context.Background(), tx)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := digest()
	if before != digest() {
		t.Fatal("unstable projection")
	}
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','n')`)
	added := digest()
	if before == added {
		t.Fatal("identity omitted")
	}
	if _, err := c.setTeamsPrivate(context.Background(), "c", true); err != nil {
		t.Fatal(err)
	}
	marked := digest()
	if marked == added {
		t.Fatal("private assertion omitted")
	}
	if _, err := c.setTeamsPrivate(context.Background(), "c", false); err != nil {
		t.Fatal(err)
	}
	if digest() == marked {
		t.Fatal("retirement omitted")
	}
	execTx(t, c.DB, `INSERT INTO source_states(host_id,source_name,kind,capability,coverage,updated_at) VALUES('h','source','codex','incremental','complete','now')`)
	complete := digest()
	execTx(t, c.DB, `UPDATE source_states SET coverage='partial' WHERE host_id='h' AND source_name='source'`)
	if digest() == complete {
		t.Fatal("source completeness change omitted")
	}
}

// Synthetic pilot-size timing; this measures metadata only and is not a
// production latency guarantee or a substitute for a scalable commitment.
func TestTeamsProtectionProjectionSyntheticPilot(t *testing.T) {
	slowTest(t)
	c, _ := testCatalog(t)
	tx := seedTx(t, c.DB)
	for _, q := range []string{
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`,
		`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<4000) INSERT INTO conversations(id,workspace_id,provider,account,native_id,origin) SELECT printf('c-%05d',x),'w','codex','local',printf('thread-%05d',x),printf('origin-%05d',x) FROM n`,
		`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<400) INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,user_private) SELECT printf('c-%05d',x),'codex','local',printf('thread-%05d',x),1 FROM n`,
		`INSERT INTO teams_source_bindings(binding_id,provenance_sha256,host_id,source_name,source_kind,provider,account_label,source_root,created_at) VALUES('b','digest','host','synthetic','codex','codex','local','/synthetic','now')`,
		`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<4000) INSERT INTO teams_native_source_proofs(binding_id,conversation_id,provider,native_id,status,trailing_partial,indexed_digest,origin) SELECT 'b',printf('c-%05d',x),'codex',printf('thread-%05d',x),'complete',0,'indexed',printf('origin-%05d',x) FROM n`,
		`INSERT INTO teams_codex_request_profiles(binding_id,conversation_id,origin,indexed_digest,evidence_sha256,facts_json) SELECT binding_id,conversation_id,origin,'indexed','evidence','[]' FROM teams_native_source_proofs`,
		`INSERT INTO teams_codex_capture_custody(binding_id,conversation_id,origin,indexed_digest,evidence_sha256,capture_sha256,captured_at,capture_path,provenance_sha256,evidence_identity,capture_revision) SELECT binding_id,conversation_id,origin,'indexed','evidence','capture','now','/synthetic','provenance','identity','revision' FROM teams_native_source_proofs`,
	} {
		if _, err := tx.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	read, err := c.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	first, err := teamsProtectionProjection(context.Background(), read)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	second, err := teamsProtectionProjection(context.Background(), read)
	if err != nil || first != second {
		t.Fatalf("unstable pilot projection: %v", err)
	}
	t.Logf("synthetic 4000 identities, 400 private marks and 4000 custody records: first metadata projection %s", elapsed)
	_ = read.Rollback()
	writer, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.db.Close()
	lane, err := teamsBeginProtectedWrite(context.Background(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(context.Background(), lane); err != nil {
		t.Fatal(err)
	}
	writer.authorized.Store(true)
	if err = teamsCaptureOpenContract(context.Background(), lane); err != nil {
		t.Fatal(err)
	}
	writer.authorized.Store(false)
	if err = lane.Commit(); err != nil {
		t.Fatal(err)
	}
	scope := teamsCheckpointScope("synthetic-app", "synthetic-library")
	key := bytes.Repeat([]byte{42}, 32)
	store := credentialstore.New(filepath.Join(t.TempDir(), "store"))
	if err = teamsCheckpointActivate(context.Background(), writer, store, scope, scope, strings.Repeat("a", 64), key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(context.Background(), tx) }, nil); err != nil {
		t.Fatal(err)
	}
	writer.requireFence = true
	started = time.Now()
	if err = teamsCheckpointWrite(context.Background(), writer, store, scope, scope, key, func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE teams_conversation_privacy SET user_private=user_private WHERE conversation_id='c-00001'`)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("synthetic pilot fenced protected write with host-store and SQLite durability: %s", time.Since(started))
}
