package archive

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gbdubs/pharos/internal/platform/credentialstore"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTeamsProtectedFormat35RejectsPreviouslyAuthenticatedExtraSchema(t *testing.T) {
	useHost(t, "synthetic-host")
	ctx := context.Background()
	c, cfg := testCatalog(t)
	cfg.Application = filepath.Join(t.TempDir(), "Synthetic.app")
	d, err := ensureLibraryDescriptor(cfg, "Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`CREATE TABLE unrelated_before_activation (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	scope := teamsCheckpointScope(installationID(cfg.Application), d.ID)
	instance := teamsCheckpointInstance(d.ID, d.Instance)
	key := bytes.Repeat([]byte{42}, 32)
	store := &teamsCheckpointTestStore{}
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
	w.authorized.Store(true)
	err = teamsCaptureOpenContract(ctx, tx)
	w.authorized.Store(false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = teamsCheckpointActivate(ctx, w, store, scope, scope, instance, key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	w.requireFence = true
	w.expectedInstance = instance
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: w, store: store, name: scope, scope: scope, guardKey: key}
	defer func() { c.teamsCheckpoint = nil }()
	before := bytes.Clone(store.raw)
	if err = c.teamsUpgradeProtectedFormat35(ctx, cfg, d, func(*sql.Tx) error { return nil }, nil); err == nil {
		t.Fatal("upgrade accepted previously authenticated extra schema")
	}
	if !bytes.Equal(before, store.raw) {
		t.Fatal("rejected inventory changed host")
	}
	var version string
	if err = c.DB.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil || version != "34" {
		t.Fatalf("version %q: %v", version, err)
	}
}

func TestTeamsProtectedFormat35Upgrade(t *testing.T) {
	useHost(t, "synthetic-host")
	ctx := context.Background()
	c, cfg := testCatalog(t)
	cfg.Application = filepath.Join(t.TempDir(), "Synthetic.app")
	d, err := ensureLibraryDescriptor(cfg, "Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	scope := teamsCheckpointScope(installationID(cfg.Application), d.ID)
	instance := teamsCheckpointInstance(d.ID, d.Instance)
	key := bytes.Repeat([]byte{42}, 32)
	store := &teamsCheckpointTestStore{}
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
	w.authorized.Store(true)
	err = teamsCaptureOpenContract(ctx, tx)
	w.authorized.Store(false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	w.requireFence = true
	w.expectedInstance = instance
	// Interrupt first activation after the host arm and fail its cleanup CAS.
	store.failReplaceAt = 1
	if err = teamsCheckpointActivate(ctx, w, store, scope, scope, instance, key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, func() error { return errors.New("synthetic interruption") }); !errors.Is(err, errTeamsCheckpointRetry) {
		t.Fatalf("first activation interruption: %v", err)
	}
	store.failReplaceAt = 0
	rawPending, pending, err := teamsCheckpointLoad(store, scope)
	if err != nil || pending.Phase != "pending" || pending.PreviousUnactivated == "" {
		t.Fatalf("first activation pending: %v", err)
	}
	w.expectedInstance = strings.Repeat("f", 64)
	if err = teamsCheckpointReconcile(ctx, w, store, scope, scope, key); !errors.Is(err, errTeamsProtectionContinuity) || !bytes.Equal(rawPending, store.raw) {
		t.Fatalf("wrong instance reconciled first activation: %v", err)
	}
	w.expectedInstance = instance
	if err = teamsCheckpointReconcile(ctx, w, store, scope, scope, key); err != nil {
		t.Fatalf("reconcile interrupted first activation: %v", err)
	}
	if err = teamsCheckpointActivate(ctx, w, store, scope, scope, instance, key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: w, store: store, name: scope, scope: scope, guardKey: key}
	defer func() { c.teamsCheckpoint = nil }()
	if got := teamsCompiledSchemaDigest35(); got != "548f2283f5fba965fa0d2b9deeaa5f5c7f5c3edfa52d7090d0088d7b1ef75dcf" {
		t.Fatalf("v35 compiled identity changed: %s", got)
	}
	if teamsCompiledSchemaDigest() != "1928edfb2b265047e9d4af02949a35ed2389d36ce29c769d4a03c2d964925ece" {
		t.Fatal("protected compiled schema identity changed")
	}

	before := bytes.Clone(store.raw)
	ordinary, err := OpenCatalog(c.Path)
	if err != nil {
		t.Fatalf("v34 ordinary open: %v", err)
	}
	ordinary.Close()
	if !bytes.Equal(before, store.raw) {
		t.Fatal("generic open changed host checkpoint")
	}
	insertOutbox := func(id, state string, payload []byte, receipt any) error {
		return teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
			_, e := tx.Exec(`INSERT INTO teams_origin_outbox(observation_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,run_id,run_generation,expires_at,token,expected_revision,observation_json,observation_sha256,state,receipt_json) VALUES(?, 'o','g','p','i','l','n','w','s','h',1,1,1,1,?,1,1,?,1,?,'hash',?,?)`, id, id, id, payload, state, receipt)
			return e
		})
	}
	if err = insertOutbox("accepted", "accepted", []byte(`{"observation":1}`), []byte(`{"accepted":true}`)); err != nil {
		t.Fatal(err)
	}
	if err = insertOutbox("pending", "prepared", []byte(`{"observation":2}`), nil); err != nil {
		t.Fatal(err)
	}
	if err = insertOutbox("oversize", "accepted", bytes.Repeat([]byte("x"), teamsOriginJournalMaxCellBytes+1), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	hostBeforeUpgrade := bytes.Clone(store.raw)
	if err = c.teamsUpgradeProtectedFormat35(ctx, cfg, d, func(*sql.Tx) error { return nil }, nil); err == nil {
		t.Fatal("over-capacity v34 catalog was upgraded")
	}
	if !bytes.Equal(hostBeforeUpgrade, store.raw) {
		t.Fatal("failed upgrade changed host checkpoint")
	}
	var oldVersion string
	if err = c.DB.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&oldVersion); err != nil || oldVersion != "34" {
		t.Fatalf("failed upgrade changed format: %q %v", oldVersion, err)
	}
	if err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		_, e := tx.Exec(`DELETE FROM teams_origin_outbox WHERE observation_id='oversize'`)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	// The seam fails after arming pending but before SQLite commit. Reopening
	// must see v34 data and allow the interrupted checkpoint to reconcile.
	stop := errors.New("synthetic precommit interruption")
	if err = c.teamsUpgradeProtectedFormat35(ctx, cfg, d, func(*sql.Tx) error { return nil }, func() error { return stop }); !errors.Is(err, errTeamsCheckpointRetry) {
		t.Fatalf("interrupted upgrade: %v", err)
	}
	if err = c.DB.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&oldVersion); err != nil || oldVersion != "34" {
		t.Fatalf("interrupted format: %q %v", oldVersion, err)
	}
	recovered, err := OpenCatalog(c.Path)
	if err != nil {
		t.Fatalf("reopen interrupted upgrade: %v", err)
	}
	recovered.Close()
	// An ambiguous arm followed by failed cleanup leaves reconciliation to the
	// next writer, while the v34 transaction remains rolled back.
	store.failAfterRename = true
	store.failReplaceAt = store.replaceCount + 2
	if err = c.teamsUpgradeProtectedFormat35(ctx, cfg, d, func(*sql.Tx) error { return nil }, nil); !errors.Is(err, errTeamsCheckpointRetry) {
		t.Fatalf("ambiguous arm: %v", err)
	}
	store.failReplaceAt = 0
	w.upgradePreflight = true
	if err = teamsCheckpointReconcile(ctx, w, store, scope, scope, key); err != nil {
		t.Fatalf("reconcile ambiguous arm: %v", err)
	}
	w.upgradePreflight = false
	// Lose final host CAS after the SQLite commit, then let the normal upgrade
	// entry reconcile the pending host record from the committed v35 tuple.
	store.failReplaceAt = store.replaceCount + 2
	if err = c.teamsUpgradeProtectedFormat35(ctx, cfg, d, func(*sql.Tx) error { return nil }, nil); !errors.Is(err, errTeamsCheckpointCommitted) {
		t.Fatalf("committed upgrade with interrupted finalizer: %v", err)
	}
	store.failReplaceAt = 0
	if err = c.teamsUpgradeProtectedFormat35(ctx, cfg, d, func(*sql.Tx) error { return nil }, nil); err != nil {
		t.Fatalf("reconcile committed upgrade: %v", err)
	}
	for _, tc := range []struct{ id, state, payload, receipt string }{{"accepted", "accepted", `{"observation":1}`, `{"accepted":true}`}, {"pending", "prepared", `{"observation":2}`, ""}} {
		var state string
		var payload, receipt []byte
		if err = c.DB.QueryRow(`SELECT state,observation_json,receipt_json FROM teams_origin_outbox WHERE observation_id=?`, tc.id).Scan(&state, &payload, &receipt); err != nil || state != tc.state || string(payload) != tc.payload || string(receipt) != tc.receipt {
			t.Fatalf("outbox %s changed across upgrade: %s %q %q %v", tc.id, state, payload, receipt, err)
		}
	}
	goldenTx, e := w.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	golden, e := teamsProtectionProjection(ctx, goldenTx)
	goldenTx.Rollback()
	if e != nil {
		t.Fatal(e)
	}
	if golden != "088ac485712a6aa5d02194d5ac0e8d784a32102b05fe437cb75d68c38bfe33c3" {
		t.Fatalf("v35 populated projection changed: %s", golden)
	}
	t.Run("changed executable opens the compatible protected catalog", func(t *testing.T) {
		override(t, &buildDigest, func() string { return "synthetic-different-executable" })
		current, err := c.initializedByThisBuild()
		if err != nil || current {
			t.Fatalf("changed build classified as current: %t %v", current, err)
		}
		host := bytes.Clone(store.raw)
		reopened, err := OpenCatalog(c.Path)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		read, err := reopened.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer read.Rollback()
		ok, err := teamsOpenContractMatches(ctx, read)
		if err != nil || !ok {
			t.Fatalf("changed-build contract: %t %v", ok, err)
		}
		reopenedRoot, err := teamsProtectionProjection(ctx, read)
		if err != nil || reopenedRoot != golden || !bytes.Equal(host, store.raw) {
			t.Fatalf("changed-build open altered protected state: %v", err)
		}
	})

	copyPath := filepath.Join(t.TempDir(), "recovery.sqlite")
	if _, err = c.DB.Exec(`VACUUM INTO ?`, copyPath); err != nil {
		t.Fatal(err)
	}
	if err = prepareTeamsRecoveredCopy(ctx, copyPath); !errors.Is(err, errTeamsProtectionContinuity) {
		t.Fatalf("v35 recovery stripping: %v", err)
	}
	copyDB, err := sql.Open("sqlite", catalogDSN(copyPath))
	if err != nil {
		t.Fatal(err)
	}
	var copyVersion string
	err = copyDB.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&copyVersion)
	var copyOutbox int
	if err == nil {
		err = copyDB.QueryRow(`SELECT count(*) FROM teams_origin_outbox`).Scan(&copyOutbox)
	}
	copyDB.Close()
	if err != nil || copyVersion != "35" || copyOutbox != 2 {
		t.Fatalf("v35 recovery changed copy: %s %d %v", copyVersion, copyOutbox, err)
	}
	// Exercise the worker's realistic 4 x 500 serialized receipt shape through
	// the actual protected writer, with the existing pending response reserved.
	if err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		if _, e := tx.Exec(`INSERT INTO teams_origin_runs(run_key,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,token,start_json,start_receipt_json,expires_at,state) VALUES('fixture','o','g','p','i','l','n','','','h',1,1,1,1,'t','{}','{}',1,'completed')`); e != nil {
			return e
		}
		for page := 0; page < 4; page++ {
			receipt := sourcewire.RecoveryPageReceipt{Status: "proof_required", RunID: "00000000-0000-4000-8000-000000000004", Generation: 1, ServerTime: 1, ExternalProofRequired: true, Family: "origins", Items: []json.RawMessage{}}
			for i := 0; i < 500; i++ {
				sum := sha256.Sum256([]byte(fmt.Sprintf("synthetic-%d-%d", page, i)))
				value := base64.RawURLEncoding.EncodeToString(sum[:])
				item, e := json.Marshal(teamsRecoveryOrigin{Token: value, Witness: value, Revision: 1, EvidenceRevision: value, Protection: "unknown", Classification: sourcewire.Classification{State: "unclassified"}})
				if e != nil {
					return e
				}
				receipt.Items = append(receipt.Items, item)
			}
			body, e := json.Marshal(receipt)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(`INSERT INTO teams_origin_run_pages(run_key,page_index,request_json,receipt_json) VALUES('fixture',?,'{}',?)`, page, body); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("4 x 500 protected receipt fixture: %v", err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_run_pages WHERE run_key='fixture'`); n != 4 {
		t.Fatalf("got %d fixture pages", n)
	}
	if err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		_, e := tx.Exec(`DELETE FROM teams_origin_run_pages WHERE run_key='fixture'`)
		if e != nil {
			return e
		}
		_, e = tx.Exec(`DELETE FROM teams_origin_runs WHERE run_key='fixture'`)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	var version string
	if err = c.DB.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil || version != "35" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	if err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO teams_origin_runs(run_key,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,token,start_json,expires_at,state) VALUES('r','o','g','p','i','l','n','','','h',1,1,1,1,'t','{}',1,'prepared')`)
		return e
	}); err != nil {
		t.Fatalf("v35 checkpointed mutation: %v", err)
	}
	// An over-limit proposed page must roll back without changing the host
	// even when a caller forgets the explicit journal preflight.
	hostBefore := bytes.Clone(store.raw)
	tooLarge := bytes.Repeat([]byte("x"), teamsOriginJournalMaxCellBytes+1)
	err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO teams_origin_run_pages(run_key,page_index,request_json,receipt_json) VALUES('r',0,'{}',?)`, tooLarge)
		return e
	})
	if !errors.Is(err, errTeamsJournalAdmissionBound) {
		t.Fatalf("oversize admission: %v", err)
	}
	if !bytes.Equal(hostBefore, store.raw) {
		t.Fatal("rejected page changed host checkpoint")
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_run_pages`); n != 0 {
		t.Fatal("rejected page was persisted")
	}
	// A normal protected writer cannot promise more responses than fit, even
	// when its callback does not know about the journal admission helper.
	hostBefore = bytes.Clone(store.raw)
	err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		if _, e := tx.Exec(`UPDATE teams_origin_runs SET state='started',start_receipt_json='{}' WHERE run_key='r'`); e != nil {
			return e
		}
		_, e := tx.Exec(`INSERT INTO teams_origin_run_pages(run_key,page_index,request_json) VALUES('r',0,'{}'),('r',1,'{}')`)
		return e
	})
	if !errors.Is(err, errTeamsJournalAdmissionBound) || !bytes.Equal(hostBefore, store.raw) {
		t.Fatalf("unreserved generic write: %v", err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_origin_run_pages`); n != 0 {
		t.Fatal("unreserved pages persisted")
	}
	reopened, err := OpenCatalog(c.Path)
	if err != nil {
		t.Fatalf("reopen after rejected write: %v", err)
	}
	reopened.Close()
	// A prepared outgoing page consumes a durable response reservation.
	err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		if _, e := tx.Exec(`UPDATE teams_origin_runs SET state='started',start_receipt_json='{}' WHERE run_key='r'`); e != nil {
			return e
		}
		if _, e := tx.Exec(`INSERT INTO teams_origin_run_pages(run_key,page_index,request_json) VALUES('r',0,'{}')`); e != nil {
			return e
		}
		return teamsPreflightOriginJournal(ctx, tx)
	})
	if err != nil {
		t.Fatalf("reserve pending page: %v", err)
	}
	if err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		_, journal := teamsProtectionProjectionParts35()
		size, e := teamsProtectionProjectionPreflight35(ctx, tx, journal, "pharos-teams-origin-journal-projection-v1", teamsOriginJournalMaxBytes)
		if e != nil {
			return e
		}
		// Two unresolved responses retain two reserves. Fill to within 128
		// bytes of the admitted limit without consuming either reserve.
		padding := int64(teamsOriginJournalMaxBytes) - 2*int64(teamsOriginJournalMaxCellBytes) - size - 128 + int64(len(`{"observation":1}`))
		if padding <= 0 || padding > int64(teamsOriginJournalMaxCellBytes) {
			return errors.New("unexpected journal fixture size")
		}
		_, e = tx.Exec(`UPDATE teams_origin_outbox SET observation_json=? WHERE observation_id='accepted'`, bytes.Repeat([]byte("p"), int(padding)))
		return e
	}); err != nil {
		t.Fatalf("fill reserved journal budget: %v", err)
	}
	if err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE teams_privacy_revision SET revision=revision+1 WHERE id=1`)
		return e
	}); err != nil {
		t.Fatalf("ordinary protected write while journal responses reserved: %v", err)
	}
	err = teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
		if _, e := tx.Exec(`UPDATE teams_origin_run_pages SET receipt_json=? WHERE run_key='r' AND page_index=0`, bytes.Repeat([]byte("a"), 2<<20)); e != nil {
			return e
		}
		return teamsPreflightOriginJournal(ctx, tx)
	})
	if err != nil {
		t.Fatalf("receipt did not consume its reservation: %v", err)
	}
	raw, host, err := teamsCheckpointLoad(store, scope)
	if err != nil || host.Phase != "current" || len(raw) == 0 {
		t.Fatalf("host not current: %v", err)
	}
	oldDB := openCatalogDB(c.Path, catalogDSN(c.Path), "legacy")
	defer oldDB.Close()
	legacy := &Catalog{Path: c.Path, DB: oldDB}
	// Simulate an old build's maximum-version test without mutating the catalog.
	var numeric int
	if err = legacy.DB.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='schema_version'`).Scan(&numeric); err != nil || numeric <= protectedCatalogSchemaVersion {
		t.Fatal("older build would not refuse newer marker")
	}
	if strings.TrimSpace(version) != "35" {
		t.Fatal("unexpected format marker")
	}
	if err = teamsCheckpointHoldForReplacement(ctx, c.teamsCheckpoint); err != nil {
		t.Fatal(err)
	}
	heldHost := bytes.Clone(store.raw)
	write := func(query string, args ...any) error {
		return teamsCheckpointWrite(ctx, w, store, scope, scope, key, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, query, args...)
			return err
		})
	}
	for _, query := range []string{
		"UPDATE teams_origin_run_pages SET receipt_json=zeroblob(2228225) WHERE run_key='r' AND page_index=0",
		"INSERT INTO teams_origin_run_pages(run_key,page_index,request_json) VALUES('r',1,'{}')",
	} {
		if err = write(query); !errors.Is(err, errTeamsJournalAdmissionBound) {
			t.Fatalf("held admission: %v", err)
		}
		var receipt []byte
		if err = c.DB.QueryRow("SELECT receipt_json FROM teams_origin_run_pages WHERE run_key='r' AND page_index=0").Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(heldHost, store.raw) || !bytes.Equal(receipt, bytes.Repeat([]byte("a"), 2<<20)) ||
			countRows(t, c, "SELECT count(*) FROM teams_origin_run_pages WHERE run_key='r'") != 1 {
			t.Fatal("rejected held write changed stored data")
		}
	}
	if err = write("UPDATE teams_origin_outbox SET state='accepted', receipt_json=zeroblob(2097152) WHERE observation_id='pending'"); err != nil {
		t.Fatalf("held reserved receipt: %v", err)
	}
	ordinaryWrite := "UPDATE teams_privacy_revision SET revision=revision+1 WHERE id=1"
	if err = write(ordinaryWrite); err != nil {
		t.Fatalf("held ordinary write: %v", err)
	}
	// Simulate an already inadmissible journal from a previous held writer.
	tx, err = teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	w.authorized.Store(true)
	_, err = tx.ExecContext(ctx, "UPDATE teams_origin_outbox SET receipt_json=zeroblob(2228225) WHERE observation_id='pending'")
	w.authorized.Store(false)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = write(ordinaryWrite); err != nil {
		t.Fatalf("held ordinary write with inadmissible journal: %v", err)
	}
	if err = write("DELETE FROM teams_origin_outbox WHERE observation_id='pending'"); !errors.Is(err, errTeamsJournalAdmissionBound) {
		t.Fatalf("previously inadmissible journal changed: %v", err)
	}
	if !bytes.Equal(heldHost, store.raw) || countRows(t, c, "SELECT count(*) FROM teams_origin_outbox WHERE observation_id='pending' AND length(receipt_json)=2228225") != 1 {
		t.Fatal("previously inadmissible held journal or host changed")
	}

}

// PHAROS_TEST_OLD_V34_TEST_BINARY is built from #800 with a test-only helper;
// PHAROS_TEST_OLD_V34_BINARY is the unmodified #800 command binary.
func TestTeamsProtectedFormat35OldWriter(t *testing.T) {
	testBinary, commandBinary := os.Getenv("PHAROS_TEST_OLD_V34_TEST_BINARY"), os.Getenv("PHAROS_TEST_OLD_V34_BINARY")
	if testBinary == "" || commandBinary == "" {
		t.Skip("separate format-34 binaries not configured")
	}
	useHost(t, "synthetic-host")
	ctx := context.Background()
	c, cfg := testCatalog(t)
	cfg.Application = filepath.Join(t.TempDir(), "Synthetic.app")
	d, err := ensureLibraryDescriptor(cfg, "Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	scope := teamsCheckpointScope(installationID(cfg.Application), d.ID)
	instance := teamsCheckpointInstance(d.ID, d.Instance)
	key := bytes.Repeat([]byte{42}, 32)
	support := t.TempDir()
	root := filepath.Join(support, "teams-credentials")
	store := credentialstore.New(root)
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
	w.authorized.Store(true)
	err = teamsCaptureOpenContract(ctx, tx)
	w.authorized.Store(false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = teamsCheckpointActivate(ctx, w, store, scope, scope, instance, key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	w.requireFence = true
	w.expectedInstance = instance
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: w, store: store, name: scope, scope: scope, guardKey: key}
	defer func() { c.teamsCheckpoint = nil }()
	childCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, testBinary, "-test.run=^TestFormat34LateWriterHelper$")
	cmd.Env = append(os.Environ(), "PHAROS_TEST_PROCESSES=1", "PHAROS_FORMAT34_LATE_HELPER=1", "PHAROS_FORMAT34_PATH="+c.Path, "PHAROS_FORMAT34_STORE="+root, "PHAROS_FORMAT34_SCOPE="+scope)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	scan := bufio.NewScanner(out)
	if !scan.Scan() || scan.Text() != "old-finalizer-ready" {
		t.Fatalf("old writer did not reach finalizer: %q %v", scan.Text(), scan.Err())
	}
	if err = c.teamsUpgradeProtectedFormat35(ctx, cfg, d, func(*sql.Tx) error { return nil }, nil); err != nil {
		t.Fatalf("upgrade with old finalizer paused: %v", err)
	}
	afterUpgrade, err := store.Load(scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = in.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if !scan.Scan() || scan.Text() != "old-first-returned" {
		t.Fatalf("old writer did not finish: %q", scan.Text())
	}
	afterLate, err := store.Load(scope)
	if err != nil || !bytes.Equal(afterUpgrade, afterLate) {
		t.Fatal("late old finalizer replaced v35 host")
	}
	if _, err = in.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if !scan.Scan() || scan.Text() != "old-reentry-rejected-host-unchanged" {
		t.Fatalf("old writer re-entry: %q", scan.Text())
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	config := "catalog_path = " + strconv.Quote(cfg.CatalogPath) + "\narchive_root = " + strconv.Quote(cfg.ArchiveRoot) + "\nstaging_root = " + strconv.Quote(cfg.StagingRoot) + "\ncapture_root = " + strconv.Quote(cfg.CaptureRoot) + "\n"
	if err = os.WriteFile(cfg.Path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	health := exec.CommandContext(childCtx, commandBinary, "--config", cfg.Path, "health")
	health.Env = append(os.Environ(), "PHAROS_SUPPORT_DIR="+support)
	output, e := health.CombinedOutput()
	if e == nil || (!bytes.Contains(output, []byte("schema version 35")) && !bytes.Contains(output, []byte("protection continuity is unproved"))) {
		t.Fatalf("old command did not refuse v35: %v %s", e, output)
	}
	final, err := store.Load(scope)
	if err != nil || !bytes.Equal(afterUpgrade, final) {
		t.Fatal("old command changed host checkpoint")
	}
}

func TestTeamsProtectedFormat35OldWriterBeforeCommit(t *testing.T) {
	testBinary := os.Getenv("PHAROS_TEST_OLD_V34_TEST_BINARY")
	if testBinary == "" {
		t.Skip("separate format-34 test binary not configured")
	}
	useHost(t, "synthetic-host")
	ctx := context.Background()
	c, cfg := testCatalog(t)
	cfg.Application = filepath.Join(t.TempDir(), "Synthetic.app")
	d, err := ensureLibraryDescriptor(cfg, "Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	scope := teamsCheckpointScope(installationID(cfg.Application), d.ID)
	instance := teamsCheckpointInstance(d.ID, d.Instance)
	key := bytes.Repeat([]byte{42}, 32)
	root := filepath.Join(t.TempDir(), "credentials")
	store := credentialstore.New(root)
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
	w.authorized.Store(true)
	err = teamsCaptureOpenContract(ctx, tx)
	w.authorized.Store(false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = teamsCheckpointActivate(ctx, w, store, scope, scope, instance, key, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(ctx, tx) }, nil); err != nil {
		t.Fatal(err)
	}
	w.requireFence = true
	w.expectedInstance = instance
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: w, store: store, name: scope, scope: scope, guardKey: key}
	defer func() { c.teamsCheckpoint = nil }()
	childCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, testBinary, "-test.run=^TestFormat34LateWriterHelper$")
	cmd.Env = append(os.Environ(), "PHAROS_TEST_PROCESSES=1", "PHAROS_FORMAT34_LATE_HELPER=1", "PHAROS_FORMAT34_PRECOMMIT=1", "PHAROS_FORMAT34_PATH="+c.Path, "PHAROS_FORMAT34_STORE="+root, "PHAROS_FORMAT34_SCOPE="+scope)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	scan := bufio.NewScanner(out)
	if !scan.Scan() || scan.Text() != "old-before-commit" {
		t.Fatalf("old writer did not hold transaction: %q", scan.Text())
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- c.teamsUpgradeProtectedFormat35(ctx, cfg, d, func(*sql.Tx) error { return nil }, nil)
	}()
	<-started
	if _, err = in.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if !scan.Scan() || scan.Text() != "old-first-returned" {
		t.Fatalf("old write did not finish: %q", scan.Text())
	}
	if err = <-done; err != nil {
		t.Fatalf("upgrade after old transaction: %v", err)
	}
	var rev int64
	if err = c.DB.QueryRow(`SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&rev); err != nil || rev != 1 {
		t.Fatalf("old committed change missing: %d %v", rev, err)
	}
	after, err := store.Load(scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = in.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if !scan.Scan() || scan.Text() != "old-reentry-rejected-host-unchanged" {
		t.Fatalf("old writer reentry: %q", scan.Text())
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	last, err := store.Load(scope)
	if err != nil || !bytes.Equal(after, last) {
		t.Fatal("old reentry changed host")
	}
}
