package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
)

// The format-34 schema remains schemaSQL(). Format 35 is an opt-in extension;
// it must never be applied by ordinary catalog initialization.
const teamsProtectedFormat35 = 35

// Independent of the existing base metadata budget. Pending wire responses
// reserve capacity before any external effect.
const teamsOriginJournalMaxBytes = 6 << 20
const teamsOriginJournalMaxCellBytes = (2 << 20) + (128 << 10)

const teamsProtectedFormat35DDL = `
CREATE TABLE teams_origin_attempts (
 observation_id TEXT PRIMARY KEY REFERENCES teams_origin_outbox(observation_id) ON DELETE CASCADE,
 first_attempt_at INTEGER NOT NULL, terminal_code TEXT NOT NULL DEFAULT ''
);
CREATE TABLE teams_origin_runs (
 run_key TEXT PRIMARY KEY, origin TEXT NOT NULL, org_id TEXT NOT NULL, person_id TEXT NOT NULL,
 installation_id TEXT NOT NULL, library_id TEXT NOT NULL, instance_id TEXT NOT NULL, work_id TEXT NOT NULL,
 snapshot_id TEXT NOT NULL, inventory_sha256 TEXT NOT NULL, settings_revision INTEGER NOT NULL,
 source_revision INTEGER NOT NULL, privacy_revision INTEGER NOT NULL, cache_generation INTEGER NOT NULL, token TEXT NOT NULL,
 start_json BLOB NOT NULL, start_receipt_json BLOB,
 expires_at INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('prepared','started','ready','completed','held'))
);
CREATE TABLE teams_origin_run_pages (
 run_key TEXT NOT NULL REFERENCES teams_origin_runs(run_key) ON DELETE CASCADE,
 page_index INTEGER NOT NULL CHECK(page_index>=0 AND page_index<4),
 request_json BLOB NOT NULL, receipt_json BLOB,
 PRIMARY KEY(run_key,page_index)
);
CREATE TABLE teams_source_transfer_ack (
 id INTEGER PRIMARY KEY CHECK(id=1), policy_id TEXT NOT NULL, policy_sha256 TEXT NOT NULL,
 origin TEXT NOT NULL, org_id TEXT NOT NULL, person_id TEXT NOT NULL, installation_id TEXT NOT NULL,
 library_id TEXT NOT NULL, instance_id TEXT NOT NULL, enrollment_generation TEXT NOT NULL,
 acknowledged_at TEXT NOT NULL
);
CREATE TRIGGER teams_source_transfer_identity AFTER UPDATE ON teams_local_settings
WHEN (old.library_id,old.instance_id,old.org_id,old.installation_id,old.key_fingerprint) IS NOT (new.library_id,new.instance_id,new.org_id,new.installation_id,new.key_fingerprint)
BEGIN DELETE FROM teams_source_transfer_ack; END;
`

var teamsProtectedFormat35Inputs = []string{"teams_origin_attempts", "teams_origin_runs", "teams_origin_run_pages", "teams_source_transfer_ack"}

var teamsProtectionProjectionQueries35 = []struct{ name, query string }{
	{"v35_attempts", `SELECT observation_id,first_attempt_at,terminal_code FROM teams_origin_attempts ORDER BY observation_id`},
	{"v35_runs", `SELECT run_key,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,token,start_json,start_receipt_json,expires_at,state FROM teams_origin_runs ORDER BY run_key`},
	{"v35_pages", `SELECT run_key,page_index,request_json,receipt_json FROM teams_origin_run_pages ORDER BY run_key,page_index`},
	{"v35_transfer_ack", `SELECT id,policy_id,policy_sha256,origin,org_id,person_id,installation_id,library_id,instance_id,enrollment_generation,acknowledged_at FROM teams_source_transfer_ack ORDER BY id`},
}

// Its identity commits the immutable old schema and extension separately;
// generated fence SQL is also bound into the compiled identity.
func teamsCompiledSchemaDigest35() string {
	h := sha256.New()
	h.Write([]byte("pharos-teams-protected-format-35\x00"))
	h.Write([]byte(schemaSQL()))
	h.Write([]byte("\x00"))
	h.Write([]byte(teamsProtectedFormat35DDL))
	for _, f := range teamsProtectionFenceStatements35() {
		h.Write([]byte("\x00"))
		h.Write([]byte(f.sql))
	}
	base, journal := teamsProtectionProjectionParts35()
	for _, part := range []struct {
		domain string
		specs  []struct{ name, query string }
	}{{"pharos-teams-protected-base-35-v1", base}, {"pharos-teams-origin-journal-projection-v1", journal}} {
		h.Write([]byte("\x00"))
		h.Write([]byte(part.domain))
		for _, spec := range part.specs {
			h.Write([]byte("\x00"))
			h.Write([]byte(spec.name))
			h.Write([]byte("\x00"))
			h.Write([]byte(spec.query))
		}
	}
	h.Write([]byte("\x00pharos-teams-protected-format-35-root-v1"))
	return hex.EncodeToString(h.Sum(nil))
}

// teamsSchemaInventoryPreflight rejects objects hidden by format 34's
// historical LIKE predicate and bounds sqlite_master values before Scan.
// It does not alter format 34's persisted digest algorithm.
func teamsSchemaInventoryPreflight(ctx context.Context, q teamsSchemaQuery, rejectHidden bool) error {
	rows, err := q.QueryContext(ctx, `SELECT typeof(type),length(CAST(type AS BLOB)),typeof(name),length(CAST(name AS BLOB)),typeof(tbl_name),length(CAST(tbl_name AS BLOB)),typeof(sql),length(CAST(sql AS BLOB)),(name LIKE 'sqlite_%' AND substr(name,1,7) != 'sqlite_' COLLATE BINARY) FROM sqlite_master WHERE sql IS NOT NULL AND substr(name,1,7) != 'sqlite_' COLLATE BINARY ORDER BY type,name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	total, count := 0, 0
	for rows.Next() {
		var kinds [4]string
		var sizes [4]int64
		var hidden bool
		if err = rows.Scan(&kinds[0], &sizes[0], &kinds[1], &sizes[1], &kinds[2], &sizes[2], &kinds[3], &sizes[3], &hidden); err != nil {
			return err
		}
		count++
		if count > 10000 || (rejectHidden && hidden) {
			return errTeamsProtectionContinuity
		}
		for i := range kinds {
			if kinds[i] != "text" || sizes[i] < 0 || sizes[i] > 16<<20 || int64(total)+sizes[i]+8 > 16<<20 {
				return errTeamsProjectionBound
			}
			total += int(sizes[i]) + 8
		}
	}
	return rows.Err()
}

func teamsActualSchemaDigest35(ctx context.Context, q teamsSchemaQuery) (string, error) {
	if err := teamsSchemaInventoryPreflight(ctx, q, false); err != nil {
		return "", err
	}
	rows, err := q.QueryContext(ctx, `SELECT type,name,tbl_name,sql FROM sqlite_master WHERE sql IS NOT NULL AND substr(name,1,7) != 'sqlite_' COLLATE BINARY ORDER BY type,name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	var length [8]byte
	for rows.Next() {
		var fields [4]string
		if err = rows.Scan(&fields[0], &fields[1], &fields[2], &fields[3]); err != nil {
			return "", err
		}
		for _, v := range fields {
			binary.BigEndian.PutUint64(length[:], uint64(len(v)))
			h.Write(length[:])
			h.Write([]byte(v))
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Upgrade accepts only the supported #800 inventory, independently of the
// previously authenticated schema digest, which may include older extras.
func teamsVerifySupported34Inventory(ctx context.Context, tx *sql.Tx) error {
	// Generated independently from a freshly initialized, fenced #800 catalog.
	const supported = "4affefba521dee4ca2ca6eb3eb7ac5d627c9cbaf66b39111c733201ad9f9429c"
	actual, err := teamsActualSchemaDigest35(ctx, tx)
	if err != nil {
		return err
	}
	if actual != supported {
		return errTeamsProtectionContinuity
	}
	return nil
}

var errTeamsJournalProjectionBound = fmt.Errorf("origin journal exceeds projection bound: %w", errTeamsProjectionBound)
var errTeamsJournalAdmissionBound = errors.New("origin journal has no safely reserved capacity")

// Called inside each journal mutation's checkpointed transaction. Every
// unresolved outgoing command reserves its maximal receipt before dispatch.
func teamsPreflightOriginJournal(ctx context.Context, tx *sql.Tx) error {
	var version string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		return err
	}
	if version != "35" {
		return errTeamsProtectionContinuity
	}
	_, journal := teamsProtectionProjectionParts35()
	size, err := teamsProtectionProjectionPreflight35(ctx, tx, journal, "pharos-teams-origin-journal-projection-v1", teamsOriginJournalMaxBytes)
	if errors.Is(err, errTeamsProjectionBound) {
		return errTeamsJournalAdmissionBound
	}
	if err != nil {
		return err
	}
	var pending int64
	err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM teams_origin_runs WHERE state IN ('prepared','held') AND start_receipt_json IS NULL)+(SELECT count(*) FROM teams_origin_run_pages WHERE receipt_json IS NULL)+(SELECT count(*) FROM teams_origin_outbox o LEFT JOIN teams_origin_attempts a ON o.observation_id=a.observation_id WHERE o.state IN ('prepared','held') AND o.receipt_json IS NULL AND (a.observation_id IS NULL OR a.terminal_code='' OR substr(a.terminal_code,1,8)='unknown_'))`).Scan(&pending)
	if err != nil {
		return err
	}
	const reserve = int64(teamsOriginJournalMaxCellBytes)
	if pending < 0 || pending > int64(teamsOriginJournalMaxBytes)/reserve || size > int64(teamsOriginJournalMaxBytes)-pending*reserve {
		return errTeamsJournalAdmissionBound
	}
	return nil
}

// The temporary table and triggers live only inside the held writer's
// transaction. Rollback removes them on failure, explicit cleanup on success.
func teamsObserveHeldJournalWrites(ctx context.Context, tx *sql.Tx, install bool) error {
	if install {
		if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE teams_held_journal_changed(id INTEGER PRIMARY KEY)"); err != nil {
			return err
		}
	} else {
		var changed bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM temp.teams_held_journal_changed)").Scan(&changed); err != nil {
			return err
		}
		if changed {
			return errTeamsJournalAdmissionBound
		}
	}
	for _, table := range append([]string{"teams_origin_outbox"}, teamsProtectedFormat35Inputs...) {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			name := "teams_held_watch_" + table + "_" + event
			stmt := "DROP TRIGGER temp." + name
			if install {
				stmt = "CREATE TEMP TRIGGER " + name + " AFTER " + event + " ON main." + table +
					" BEGIN INSERT OR IGNORE INTO teams_held_journal_changed VALUES(1); END"
			}
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
	}
	if !install {
		_, err := tx.ExecContext(ctx, "DROP TABLE temp.teams_held_journal_changed")
		return err
	}
	return nil
}

// Every host record involved in a format-35 CAS must belong to the current
// descriptor's instance, including both arms of an interrupted transition.
func teamsRequireFormat35HostInstance(ctx context.Context, tx *sql.Tx, w *teamsProtectedWriter, host teamsCheckpointRecord) error {
	var version string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		return err
	}
	if version != "35" && w.expectedInstance == "" {
		return nil
	}
	if !teamsCheckpointHex(w.expectedInstance, 64) {
		return errTeamsProtectionContinuity
	}
	for _, tuple := range []*teamsCheckpointTuple{host.Current, host.Previous, host.Next} {
		if tuple != nil && tuple.Instance != w.expectedInstance {
			return errTeamsProtectionContinuity
		}
	}
	current, err := teamsCheckpointTupleFromSQL(ctx, tx)
	// Before the first activation commits, SQLite has no tuple. A pending
	// host must still name this descriptor in Next; reconciliation verifies
	// the authenticated previous-unactivated projection before restoring it.
	initial := host.Phase == "unactivated" || (host.Phase == "pending" && host.PreviousUnactivated != "" && host.Previous == nil)
	if errors.Is(err, sql.ErrNoRows) && version == strconv.Itoa(catalogSchemaVersion) && initial {
		return nil
	}
	if err != nil || current.Instance != w.expectedInstance {
		return errTeamsProtectionContinuity
	}
	return nil
}

// teamsUpgradeProtectedFormat35 is an explicitly authorized, dormant seam.
// It is never invoked by a generic open. The caller supplies an independent
// authority check and the already-configured checkpoint/descriptor scope.
func (c *Catalog) teamsUpgradeProtectedFormat35(ctx context.Context, cfg Config, d LibraryDescriptor, verify func(*sql.Tx) error, beforeCommit func() error) error {
	if c == nil || c.teamsCheckpoint == nil || cfg.Application == "" || cfg.CatalogPath != c.Path || d.ID == "" || d.Instance == "" || verify == nil {
		return errTeamsProtectionContinuity
	}
	// A transfer holds this lease exclusively across its preflight and all
	// subsequent effects; a format upgrade must not race that decision.
	use, err := fileLease(libraryUseLock(cfg), false)
	if err != nil {
		return err
	}
	defer use.Close()
	r := c.teamsCheckpoint
	expectedScope := teamsCheckpointScope(installationID(cfg.Application), d.ID)
	instance := teamsCheckpointInstance(d.ID, d.Instance)
	if r.scope != expectedScope || r.writer == nil || !r.writer.requireFence || r.writer.expectedInstance != instance {
		return errTeamsProtectionContinuity
	}
	initLease, err := fileLease(filepath.Join(filepath.Dir(c.Path), initializeLockName), true)
	if err != nil {
		return err
	}
	defer initLease.Close()
	sharing, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer sharing.Close()
	capture, err := beginCapture(cfg)
	if err != nil {
		return err
	}
	defer capture.Close()
	// Reconcile a previous interruption while holding the same outer barriers.
	w := r.writer
	w.mu.Lock()
	pre, preErr := teamsBeginProtectedWrite(ctx, w)
	if preErr == nil {
		preErr = teamsVerifyUpgradeAuthority(ctx, pre, verify)
		if preErr == nil {
			var v string
			preErr = pre.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&v)
			if preErr == nil && v == "34" {
				preErr = teamsVerifySupported34Inventory(ctx, pre)
			}
		}
		_ = pre.Rollback()
	}
	if preErr == nil {
		w.upgradePreflight = true
		preErr = teamsCheckpointReconcileLocked(ctx, w, r.store, r.name, r.scope, r.guardKey)
		w.upgradePreflight = false
	}
	w.mu.Unlock()
	if preErr != nil {
		return preErr
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		return err
	}
	if version == "35" {
		ok, e := teamsOpenContractMatches(ctx, tx)
		if e != nil || !ok {
			return errTeamsProtectionContinuity
		}
		return nil
	}
	if version != "34" {
		return errTeamsProtectionContinuity
	}
	if err = teamsSchemaInventoryPreflight(ctx, tx, true); err != nil {
		return err
	}
	if err = teamsVerifyProtectionFence(ctx, tx); err != nil {
		return err
	}
	ok, err := teamsOpenContractMatches(ctx, tx)
	if err != nil || !ok {
		return errTeamsProtectionContinuity
	}
	raw, host, err := teamsCheckpointLoad(r.store, r.name)
	if err != nil {
		return err
	}
	keyID, err := teamsCheckpointKeyID(r.guardKey)
	if err != nil || host.Scope != r.scope || host.KeyID != keyID || host.Phase != "current" || host.Current == nil || host.Current.Instance != instance || host.Sequence >= math.MaxInt64-2 {
		return errTeamsProtectionContinuity
	}
	current, err := teamsCheckpointTupleFromSQL(ctx, tx)
	if err != nil || *current != *host.Current || current.Instance != instance {
		return errTeamsProtectionContinuity
	}
	revision, err := teamsCheckpointFenceRevision(ctx, tx)
	if err != nil || revision != current.Revision {
		return errTeamsProtectionContinuity
	}
	if _, err = teamsProtectionProjectionPreflight35(ctx, tx, teamsProtectionProjectionQueries, "pharos-teams-protection-projection-v1", teamsProjectionMaxBytes); err != nil {
		return err
	}
	prior, err := teamsProtectionProjection(ctx, tx)
	if err != nil || prior != current.Digest {
		return errTeamsProtectionContinuity
	}
	if err = teamsVerifyUpgradeAuthority(ctx, tx, verify); err != nil {
		return err
	}
	checked, err := teamsProtectionProjection(ctx, tx)
	if err != nil || checked != prior {
		return errTeamsProtectionContinuity
	}
	for _, name := range append(append([]string(nil), teamsProtectedFormat35Inputs...), "teams_source_transfer_identity") {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name=?)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return errTeamsProtectionContinuity
		}
	}
	old := map[string]bool{}
	for _, f := range teamsProtectionFenceStatements() {
		old[f.name] = true
	}
	for _, f := range teamsProtectionFenceStatements35() {
		if old[f.name] {
			continue
		}
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name=?)`, f.name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return errTeamsProtectionContinuity
		}
	}
	w.authorized.Store(true)
	defer w.authorized.Store(false)
	if _, err = tx.ExecContext(ctx, `SAVEPOINT teams_protection_action`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, teamsProtectedFormat35DDL); err != nil {
		return err
	}
	for _, f := range teamsProtectionFenceStatements35() {
		if !old[f.name] {
			if _, err = tx.ExecContext(ctx, f.sql); err != nil {
				return err
			}
		}
	}
	// Empty additive structures must not alter the frozen v34 projection.
	checked, err = teamsProtectionProjection(ctx, tx)
	if err != nil || checked != prior {
		return errTeamsProtectionContinuity
	}
	actual, err := teamsActualSchemaDigest35(ctx, tx)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE teams_protection_open_contract SET initializer_epoch=?,compiled_schema_sha256=?,actual_schema_sha256=?,catalog_version=?,library_version=? WHERE id=1 AND catalog_version=?`, teamsProtectionInitializerEpoch, teamsCompiledSchemaDigest35(), actual, teamsProtectedFormat35, libraryVersion(), protectedCatalogSchemaVersion)
	if err != nil {
		return err
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return errTeamsProtectionContinuity
	}
	result, err = tx.ExecContext(ctx, `UPDATE meta SET value='35' WHERE key='schema_version' AND value='34'`)
	if err != nil {
		return err
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return errTeamsProtectionContinuity
	}
	if err = teamsVerifyProtectionFence(ctx, tx); err != nil {
		return err
	}
	ok, err = teamsOpenContractMatches(ctx, tx)
	if err != nil || !ok {
		return errTeamsProtectionContinuity
	}
	revision, err = teamsCheckpointFenceRevision(ctx, tx)
	if err != nil {
		return err
	}
	// Migration must leave enough room for every already admitted response.
	// This runs before arming the host checkpoint, so an over-capacity v34
	// catalog remains usable in its original format.
	if err = teamsPreflightOriginJournal(ctx, tx); err != nil {
		return err
	}
	digest, err := teamsProtectionProjection(ctx, tx)
	if err != nil {
		return err
	}
	id, err := teamsCheckpointRandom()
	if err != nil {
		return err
	}
	op, err := teamsCheckpointRandom()
	if err != nil {
		return err
	}
	next := &teamsCheckpointTuple{Generation: host.Sequence + 1, Instance: instance, Revision: revision, CommitID: id, Digest: digest}
	if next.Instance != current.Instance {
		return errTeamsProtectionContinuity
	}
	if err = teamsCheckpointPutTuple(ctx, tx, next); err != nil {
		return err
	}
	pending := teamsCheckpointRecord{Version: 1, Scope: r.scope, KeyID: keyID, Sequence: host.Sequence + 1, Phase: "pending", Previous: current, Next: next, Operation: op, Transition: "no_tightening", Cover: []teamsCheckpointGuardCover{}}
	armed, err := teamsEncodeCheckpoint(pending)
	if err != nil {
		return err
	}
	if err = teamsCheckpointReplace(r.store, r.name, raw, armed); err != nil {
		_ = teamsCheckpointUnwind(ctx, tx, r.store, r.name, raw, armed, host, prior)
		return errTeamsCheckpointRetry
	}
	if beforeCommit != nil && beforeCommit() != nil {
		_ = teamsCheckpointUnwind(ctx, tx, r.store, r.name, raw, armed, host, prior)
		return errTeamsCheckpointRetry
	}
	if err = tx.Commit(); err != nil {
		return errTeamsCheckpointRetry
	}
	if err = teamsFinalizeCheckpointHost(r.store, r.name, armed, pending); err != nil {
		return errTeamsCheckpointCommitted
	}
	return nil
}

// Verify authority without allowing the callback to make untracked SQL or
// schema changes, before a recovery CAS or a new transition is attempted.
func teamsVerifyUpgradeAuthority(ctx context.Context, tx *sql.Tx, verify func(*sql.Tx) error) error {
	var version string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		return err
	}
	if version != "34" && version != "35" {
		return errTeamsProtectionContinuity
	}
	if err := teamsSchemaInventoryPreflight(ctx, tx, version == "34"); err != nil {
		return err
	}
	if err := teamsVerifyProtectionFence(ctx, tx); err != nil {
		return err
	}
	ok, err := teamsOpenContractMatches(ctx, tx)
	if err != nil || !ok {
		return errTeamsProtectionContinuity
	}
	if version == "34" {
		if _, err = teamsProtectionProjectionPreflight35(ctx, tx, teamsProtectionProjectionQueries, "pharos-teams-protection-projection-v1", teamsProjectionMaxBytes); err != nil {
			return err
		}
	}
	before, err := teamsProtectionProjection(ctx, tx)
	if err != nil {
		return err
	}
	var changes int64
	if err = tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&changes); err != nil {
		return err
	}
	if err = verify(tx); err != nil {
		return err
	}
	var afterChanges int64
	if err = tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&afterChanges); err != nil || changes != afterChanges {
		return errTeamsProtectionContinuity
	}
	if err = teamsSchemaInventoryPreflight(ctx, tx, version == "34"); err != nil {
		return err
	}
	if err = teamsVerifyProtectionFence(ctx, tx); err != nil {
		return err
	}
	ok, err = teamsOpenContractMatches(ctx, tx)
	if err != nil || !ok {
		return errTeamsProtectionContinuity
	}
	after, err := teamsProtectionProjection(ctx, tx)
	if err != nil || before != after {
		return errTeamsProtectionContinuity
	}
	return nil
}
