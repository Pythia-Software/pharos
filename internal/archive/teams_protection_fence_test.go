package archive

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTeamsProtectionFenceCatalogInputs(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','n')`)
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`INSERT INTO teams_conversation_privacy(conversation_id,user_private) VALUES('c',1)`, `DELETE FROM conversations WHERE id='c'`, `UPDATE teams_privacy_revision SET revision=revision+1 WHERE id=1`} {
		if _, err = c.DB.Exec(q); err == nil {
			t.Fatalf("uncovered write accepted: %s", q)
		}
	}
	if _, err = c.DB.Exec(`INSERT INTO ui_preferences(key,value) VALUES('synthetic','1')`); err != nil { // table shape can vary; use an unrelated metadata key instead.
		if _, err = c.DB.Exec(`INSERT INTO meta(key,value) VALUES('synthetic-unrelated','1')`); err != nil {
			t.Fatal(err)
		}
	}
	tx, err = teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsVerifyProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(true)
	if _, err = tx.Exec(`INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,user_private) VALUES('c','test','local','n',1)`); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(false)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT revision FROM teams_protection_fence_revision WHERE id=1`); n < 2 {
		t.Fatalf("trigger-induced assertion not counted: %d", n)
	}
	if _, err = c.DB.Exec(`DROP TRIGGER teams_fence_teams_conversation_privacy_INSERT`); err != nil {
		t.Fatal(err)
	}
	tx, err = teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = teamsVerifyProtectionFence(ctx, tx); err == nil {
		t.Fatal("missing fence was accepted")
	}
}

func TestTeamsProtectionFenceNoopThenWrite(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", "library")
	key := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointActivate(ctx, w, store, "checkpoint", scope, strings.Repeat("a", 64), key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`UPDATE teams_privacy_revision SET revision=revision WHERE id=1`, `UPDATE teams_privacy_revision SET revision=revision+1 WHERE id=1`} {
		if err = teamsCheckpointWrite(ctx, w, store, "checkpoint", scope, key, func(tx *sql.Tx) error { _, e := tx.Exec(statement); return e }); err != nil {
			t.Fatalf("write %q: %v", statement, err)
		}
	}
	origin := `INSERT INTO teams_origin_outbox(observation_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,run_id,run_generation,expires_at,token,expected_revision,observation_json,observation_sha256,state) VALUES('o','origin','org','person','install','library','instance','work','snapshot','hash',1,1,1,1,'run',1,1,'token',0,'{}','hash','prepared')`
	if _, err = c.DB.Exec(origin); err == nil {
		t.Fatal("ordinary writer inserted acquisition evidence")
	}
	if err = teamsCheckpointWrite(ctx, w, store, "checkpoint", scope, key, func(tx *sql.Tx) error { _, e := tx.Exec(origin); return e }); err != nil {
		t.Fatalf("fenced acquisition write: %v", err)
	}
	state, err := teamsParseCheckpoint(store.raw)
	if err != nil || state.Current == nil || state.Current.Revision != 3 {
		t.Fatalf("fence revision not checkpointed: %+v %v", state, err)
	}
}

func TestTeamsProtectionFenceRealPrivacyWriterAtCapacity(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','n')`)
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", "library")
	key := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointActivate(ctx, w, store, "checkpoint", scope, strings.Repeat("a", 64), key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: w, store: store, name: "checkpoint", scope: scope, guardKey: key}
	defer func() { c.teamsCheckpoint = nil }()
	override(t, &teamsProjectionMaxBytes, 1)
	if _, err = c.setTeamsPrivate(ctx, "c", true); err != nil {
		t.Fatalf("private mark at bound: %v", err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='c' AND kind='explicit_private' AND active=1`); n != 1 {
		t.Fatal("private assertion not retained")
	}
	host, err := teamsParseCheckpoint(store.raw)
	if err != nil || host.Phase != "held" {
		t.Fatal("capacity did not retain host hold", err)
	}
	// Simulate restoration of a pre-checkpoint catalog: the durable host hold
	// keeps proof unavailable while local privacy controls still work.
	tx, err = teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(true)
	_, err = tx.Exec(`DELETE FROM teams_protection_checkpoint`)
	w.authorized.Store(false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.setTeamsPrivate(ctx, "c", false); err != nil {
		t.Fatalf("unmark while held: %v", err)
	}
	if err = teamsCheckpointReconcile(ctx, w, store, "checkpoint", scope, key); !errors.Is(err, errTeamsProtectionHeld) {
		t.Fatalf("held proof was not denied: %v", err)
	}
}

func TestTeamsCheckpointConfiguredCleanOpen(t *testing.T) {
	c, cfg := testCatalog(t)
	ctx := context.Background()
	cfg.Application = filepath.Join(t.TempDir(), "SyntheticPharos.app")
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','n')`)
	store, err := teamsCredentialStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	scope := teamsCheckpointScope(installationID(cfg.Application), d.ID)
	key := bytes.Repeat([]byte{42}, 32)
	if err = store.Save(teamsCheckpointKeyName(scope), key); err != nil {
		t.Fatal(err)
	}
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(true)
	if err = teamsCaptureOpenContract(ctx, tx); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(false)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = teamsCheckpointActivate(ctx, w, store, scope, scope, teamsCheckpointInstance(d.ID, d.Instance), key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	if err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE teams_protection_baseline SET state='current' WHERE id=1`)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	// An updated executable with the same schema contract must not run
	// legacy initializer writes through the unfenced ordinary pool.
	if _, err = c.DB.Exec(`UPDATE meta SET value='synthetic-previous-build' WHERE key=?`, initializedBuildKey); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenCatalog(cfg.CatalogPath)
	if err != nil {
		t.Fatalf("clean open: %v", err)
	}
	defer opened.Close()
	held, err := opened.configureTeamsCheckpoint(ctx, cfg, d)
	if err != nil || held {
		t.Fatalf("configure clean open: held=%v %v", held, err)
	}
	after, err := store.Load(scope)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("clean open changed checkpoint: %v", err)
	}
	if n := countRows(t, opened, `SELECT count(*) FROM teams_protection_baseline WHERE state='current'`); n != 1 {
		t.Fatal("clean open held baseline")
	}
	if _, err = opened.setTeamsPrivate(ctx, "c", true); err != nil {
		t.Fatalf("configured local write: %v", err)
	}
	if _, err = opened.DB.Exec(`CREATE TABLE synthetic_schema_drift(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err = opened.setTeamsPrivate(ctx, "c", false); !errors.Is(err, errTeamsProtectionContinuity) {
		t.Fatalf("schema drift accepted: %v", err)
	}
}

func TestTeamsProtectionFenceBackgroundIngest(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", "library")
	key := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointActivate(ctx, w, store, "checkpoint", scope, strings.Repeat("a", 64), key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: w, store: store, name: "checkpoint", scope: scope, guardKey: key}
	defer func() { c.teamsCheckpoint = nil }()
	ingestUsageFixture(t, c)
	if n := countRows(t, c, `SELECT count(*) FROM conversations WHERE provider='codex'`); n < 1 {
		t.Fatal("background ingest did not index")
	}
}

func TestTeamsProtectionFenceBackgroundIngestAtCapacity(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", "library")
	key := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointActivate(ctx, w, store, "checkpoint", scope, strings.Repeat("a", 64), key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: w, store: store, name: "checkpoint", scope: scope, guardKey: key}
	defer func() { c.teamsCheckpoint = nil }()
	override(t, &teamsProjectionMaxBytes, 1)
	ingestUsageFixture(t, c)
	var id string
	if err = c.DB.QueryRow(`SELECT id FROM conversations WHERE provider='codex' LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = c.setTeamsPrivate(ctx, id, true); err != nil {
		t.Fatalf("privacy tightening at bound: %v", err)
	}
	host, err := teamsParseCheckpoint(store.raw)
	if err != nil || host.Phase != "held" {
		t.Fatal("capacity did not hold proof", err)
	}
}

func TestTeamsCheckpointMoveRetainsIndependentHold(t *testing.T) {
	support := t.TempDir()
	t.Setenv("PHAROS_SUPPORT_DIR", support)
	source, target := lifecycleLibraries(t)
	source.Application = filepath.Join(support, "Synthetic.app")
	d, err := readLibraryDescriptor(source)
	if err != nil {
		t.Fatal(err)
	}
	c := reopenCatalog(t, source.CatalogPath)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','n')`, `INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,classifier_state,classifier_input) VALUES('c','test','local','n','flagged','synthetic')`, `UPDATE teams_conversation_privacy SET classifier_state='clear' WHERE conversation_id='c'`)
	execTx(t, c.DB, `INSERT INTO teams_origin_outbox(observation_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,run_id,run_generation,expires_at,token,expected_revision,observation_json,observation_sha256,state) VALUES('o','origin','org','person','install','library','instance','work','snapshot','hash',1,1,1,1,'run',1,1,'token',0,'{}','hash','held')`)
	store, err := teamsCredentialStore(source)
	if err != nil {
		t.Fatal(err)
	}
	scope := teamsCheckpointScope(installationID(source.Application), d.ID)
	key := bytes.Repeat([]byte{42}, 32)
	if err = store.Save(teamsCheckpointKeyName(scope), key); err != nil {
		t.Fatal(err)
	}
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := teamsBeginProtectedWrite(t.Context(), w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(true)
	if err = teamsCaptureOpenContract(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(false)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = teamsCheckpointActivate(t.Context(), w, store, scope, scope, teamsCheckpointInstance(d.ID, d.Instance), key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(t.Context(), tx) }, nil); err != nil {
		t.Fatal(err)
	}
	if err = w.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = TransferLibrary(t.Context(), source, target, true, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Load(scope)
	if err != nil {
		t.Fatal(err)
	}
	host, err := teamsParseCheckpoint(raw)
	if err != nil || host.Phase != "held" {
		t.Fatalf("source host not held: %+v %v", host, err)
	}
	c = reopenCatalog(t, source.CatalogPath)
	defer c.Close()
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='c' AND kind='positive_flag' AND active=1`); n != 1 {
		t.Fatalf("source flag lost: %d", n)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_baseline WHERE state='held'`); n != 1 {
		t.Fatal("source baseline not held")
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_outbox WHERE observation_id='o' AND state='held'`); n != 1 {
		t.Fatal("source lost held origin evidence")
	}
	dest := reopenCatalog(t, target.CatalogPath)
	defer dest.Close()
	if n := countRows(t, dest, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='c' AND kind='legacy_hold' AND evidence='unreconciled_import'`); n != 1 {
		t.Fatal("destination import not held")
	}
}

func TestTeamsCheckpointImportHoldsDestination(t *testing.T) {
	source, _ := testCatalog(t)
	target, _ := testCatalog(t)
	ctx := context.Background()
	execTx(t, source.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','n')`)
	w, err := teamsOpenProtectedWriter(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", "library")
	key := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointActivate(ctx, w, store, "checkpoint", scope, strings.Repeat("a", 64), key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	target.teamsCheckpoint = &teamsCheckpointRuntime{writer: w, store: store, name: "checkpoint", scope: scope, guardKey: key}
	defer func() { target.teamsCheckpoint = nil }()
	if err = mergeHistory(ctx, target, source.Path); err != nil {
		t.Fatal(err)
	}
	host, err := teamsParseCheckpoint(store.raw)
	if err != nil || host.Phase != "held" {
		t.Fatal("import failed to hold host", err)
	}
	if n := countRows(t, target, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='c' AND kind='legacy_hold' AND evidence='unreconciled_import'`); n != 1 {
		t.Fatal("import hold missing")
	}
}

func TestTeamsCheckpointRejectOldBinary(t *testing.T) {
	binary := os.Getenv("PHAROS_TEST_OLD_V33_BINARY")
	if binary == "" {
		t.Skip("set PHAROS_TEST_OLD_V33_BINARY to a separately built version-33 Pharos binary")
	}
	c, cfg := testCatalog(t)
	ctx := context.Background()
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", "library")
	key := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointActivate(ctx, w, store, "checkpoint", scope, strings.Repeat("a", 64), key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	contents := "catalog_path = " + strconv.Quote(cfg.CatalogPath) + "\narchive_root = " + strconv.Quote(cfg.ArchiveRoot) + "\nstaging_root = " + strconv.Quote(cfg.StagingRoot) + "\ncapture_root = " + strconv.Quote(cfg.CaptureRoot) + "\n"
	if err = os.WriteFile(cfg.Path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	childCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, binary, "--config", cfg.Path, "health")
	output, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("schema version 34")) {
		t.Fatalf("old binary did not reject activated catalog: %v %s", err, output)
	}
	var version string
	if err = c.DB.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil || version != "34" {
		t.Fatalf("old binary changed version: %q %v", version, err)
	}
}

func TestTeamsCheckpointRecoverCopyRetainsUnknown(t *testing.T) {
	source, _ := lifecycleLibraries(t)
	d, err := readLibraryDescriptor(source)
	if err != nil {
		t.Fatal(err)
	}
	c := reopenCatalog(t, source.CatalogPath)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','n')`, `INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,classifier_state,classifier_input) VALUES('c','test','local','n','flagged','synthetic')`, `UPDATE teams_conversation_privacy SET classifier_state='clear' WHERE conversation_id='c'`)
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := teamsBeginProtectedWrite(t.Context(), w)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(true)
	if err = teamsCaptureOpenContract(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	// A different supported executable can have a different compiled schema
	// or library version while this copied structure remains intact.
	if _, err = tx.Exec(`UPDATE teams_protection_open_contract SET compiled_schema_sha256=?,library_version='synthetic-other-build' WHERE id=1`, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(false)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", d.ID)
	key := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointActivate(t.Context(), w, store, "checkpoint", scope, teamsCheckpointInstance(d.ID, d.Instance), key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(t.Context(), tx) }, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`CREATE TABLE synthetic_recovery_schema_drift(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	check, err := c.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsRecoveryStructureSupported(t.Context(), check); !errors.Is(err, errTeamsProtectionContinuity) {
		t.Fatalf("recovery accepted schema drift: %v", err)
	}
	_ = check.Rollback()
	if _, err = c.DB.Exec(`DROP TABLE synthetic_recovery_schema_drift`); err != nil {
		t.Fatal(err)
	}
	check, err = teamsBeginProtectedWrite(t.Context(), w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = check.Exec(`CREATE TRIGGER synthetic_recovery_trigger AFTER INSERT ON teams_protection_assertions BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	digest, err := teamsActualSchemaDigest(t.Context(), check)
	if err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(true)
	if _, err = check.Exec(`UPDATE teams_protection_open_contract SET actual_schema_sha256=? WHERE id=1`, digest); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(false)
	if err = teamsRecoveryStructureSupported(t.Context(), check); !errors.Is(err, errTeamsProtectionContinuity) {
		t.Fatalf("recovery accepted extra trigger with matching digest: %v", err)
	}
	_ = check.Rollback()
	original := bytes.Clone(store.raw)
	if err = w.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverLibrary(t.Context(), filepath.Dir(source.Path), filepath.Join(t.TempDir(), "recovered"), nil)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := readLibraryDescriptor(recovered)
	if err != nil || rd.RecoveredFrom != d.ID || rd.ID == d.ID {
		t.Fatalf("recovery identity: %+v %v", rd, err)
	}
	restored := reopenCatalog(t, recovered.CatalogPath)
	defer restored.Close()
	if n := countRows(t, restored, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='c' AND kind='positive_flag' AND active=1`); n != 1 {
		t.Fatal("recovery lost prior flag")
	}
	if n := countRows(t, restored, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='' AND kind='legacy_hold' AND evidence='unproved_recovery' AND active=1`); n != 1 {
		t.Fatal("recovery did not retain an unscoped hold")
	}
	if n := countRows(t, restored, `SELECT count(*) FROM teams_protection_checkpoint`); n != 0 {
		t.Fatal("host checkpoint copied into recovery authority")
	}
	if !bytes.Equal(store.raw, original) {
		t.Fatal("recovery changed source host checkpoint")
	}
}

func TestTeamsRecoveryLegacyCopyHeld(t *testing.T) {
	source, _ := lifecycleLibraries(t)
	c := reopenCatalog(t, source.CatalogPath)
	if _, err := c.DB.Exec(`UPDATE meta SET value=' 33 ' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverLibrary(t.Context(), filepath.Dir(source.Path), filepath.Join(t.TempDir(), "recovered"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c = reopenCatalog(t, recovered.CatalogPath)
	defer c.Close()
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='' AND kind='legacy_hold' AND evidence='unproved_recovery' AND active=1`); n != 1 {
		t.Fatal("pre-checkpoint recovery lacks unproved hold")
	}
}

func TestTeamsRecoveryPreviousProtectedSchema(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := t.Context()
	for _, name := range []string{"teams_transcript_message_insert", "teams_transcript_message_update", "teams_transcript_message_delete", "teams_transcript_conversation_identity", "teams_transcript_conversation_delete", "teams_transcript_custody_update", "teams_transcript_evidence_deleted"} {
		if _, err := c.DB.Exec(`DROP TRIGGER IF EXISTS ` + quotedIdentifier(name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.DB.Exec(`DROP TABLE teams_codex_transcript_evidence_v1`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"teams_recovery_stage_pages", "teams_recovery_stage_batches", "teams_profile_outbox_pages", "teams_profile_outbox", "teams_origin_outbox"} {
		if _, err := c.DB.Exec(`DROP TABLE ` + quotedIdentifier(name)); err != nil {
			t.Fatal(err)
		}
	}
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	newTables := []string{"teams_v2_uploads", "teams_origin_outbox", "teams_profile_outbox", "teams_profile_outbox_pages", "teams_recovery_stage_batches", "teams_recovery_stage_pages", "teams_codex_transcript_evidence_v1"}
	for _, fence := range teamsProtectionFenceStatements() {
		if !teamsFenceBelongsTo(fence.name, newTables) {
			if _, err = tx.Exec(fence.sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	digest, err := teamsActualSchemaDigest(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(true)
	if _, err = tx.Exec(`INSERT INTO teams_protection_open_contract(id,initializer_epoch,compiled_schema_sha256,actual_schema_sha256,catalog_version,library_version) VALUES(1,1,?,?,?,'previous-build')`, strings.Repeat("f", 64), digest, protectedCatalogSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE meta SET value=? WHERE key='schema_version'`, strconv.Itoa(protectedCatalogSchemaVersion)); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(false)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = check.Exec(`CREATE TABLE teams_origin_outbox(synthetic TEXT)`); err != nil {
		t.Fatal(err)
	}
	digest, err = teamsActualSchemaDigest(ctx, check)
	if err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(true)
	if _, err = check.Exec(`UPDATE teams_protection_open_contract SET actual_schema_sha256=? WHERE id=1`, digest); err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(false)
	if err = teamsRecoveryStructureSupported(ctx, check); !errors.Is(err, errTeamsProtectionContinuity) {
		t.Fatalf("partial new schema accepted: %v", err)
	}
	_ = check.Rollback()
	if err = w.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if err = prepareTeamsRecoveredCopy(ctx, c.Path); err != nil {
		t.Fatalf("previous v34 recovery: %v", err)
	}
	recovered := reopenCatalog(t, c.Path)
	defer recovered.Close()
	if n := countRows(t, recovered, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='' AND kind='legacy_hold' AND evidence='unproved_recovery' AND active=1`); n != 1 {
		t.Fatal("previous v34 recovery lost unknown hold")
	}
}
