package archive

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"strconv"

	"github.com/gbdubs/pharos/internal/platform/credentialstore"
)

// No production path sets this runtime until writer inventory, open/restore,
// policy, and handoff checks are complete. The key must come from the stable
// host store and must never be serialized to the catalog.
type teamsCheckpointRuntime struct {
	writer      *teamsProtectedWriter
	store       credentialstore.Store
	name, scope string
	guardKey    []byte
}

// This closed lifecycle is not wired to user operations. It is conservative:
// every changed projection arms a library-wide unknown cover. Production
// activation still requires the fence, complete projection, key lifecycle,
// recovery admission/release, and every writer/handoff integration.
var errTeamsCheckpointRetry = errors.New("protection checkpoint needs reconciliation")
var errTeamsProtectionHeld = errors.New("protection continuity is held")
var errTeamsCheckpointCommitted = errors.New("local change committed; protection checkpoint is reconciling")

func teamsCheckpointRandom() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func teamsCheckpointTupleFromSQL(ctx context.Context, tx *sql.Tx) (*teamsCheckpointTuple, error) {
	var t teamsCheckpointTuple
	err := tx.QueryRowContext(ctx, `SELECT generation,instance,revision,commit_id,digest FROM teams_protection_checkpoint WHERE id=1`).Scan(&t.Generation, &t.Instance, &t.Revision, &t.CommitID, &t.Digest)
	if err != nil {
		return nil, err
	}
	if !teamsCheckpointTupleValid(&t) {
		return nil, errTeamsProtectionContinuity
	}
	return &t, nil
}
func teamsCheckpointFenceRevision(ctx context.Context, tx *sql.Tx) (int64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM teams_protection_fence_revision WHERE id=1`).Scan(&revision)
	if err != nil || revision < 0 || revision >= math.MaxInt64 {
		return 0, errTeamsProtectionContinuity
	}
	return revision, nil
}
func teamsCheckpointPutTuple(ctx context.Context, tx *sql.Tx, t *teamsCheckpointTuple) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO teams_protection_checkpoint(id,generation,instance,revision,commit_id,digest) VALUES(1,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET generation=excluded.generation,instance=excluded.instance,revision=excluded.revision,commit_id=excluded.commit_id,digest=excluded.digest`, t.Generation, t.Instance, t.Revision, t.CommitID, t.Digest)
	return err
}
func teamsCheckpointLoad(store credentialstore.Store, name string) ([]byte, teamsCheckpointRecord, error) {
	raw, err := store.Load(name)
	if err != nil {
		return nil, teamsCheckpointRecord{}, errTeamsCheckpointRetry
	}
	record, err := teamsParseCheckpoint(raw)
	if err != nil {
		return nil, record, err
	}
	return raw, record, nil
}

// Replace may report an error after rename. Reload and successfully replace the
// exact visible bytes with themselves before treating pending as durable.
func teamsCheckpointReplace(store credentialstore.Store, name string, old, next []byte) error {
	if err := store.Replace(name, old, next); err == nil {
		return nil
	}
	visible, err := store.Load(name)
	if err != nil || string(visible) != string(next) {
		return errTeamsCheckpointRetry
	}
	if err = store.Replace(name, next, next); err != nil {
		return errTeamsCheckpointRetry
	}
	return nil
}
func teamsCheckpointFinal(r teamsCheckpointRecord) (teamsCheckpointRecord, error) {
	if r.Sequence >= math.MaxInt64-1 || r.Phase != "pending" {
		return teamsCheckpointRecord{}, errTeamsProtectionContinuity
	}
	return teamsCheckpointRecord{Version: 1, Scope: r.Scope, KeyID: r.KeyID, Sequence: r.Sequence + 1, Phase: "current", Current: r.Next, Cover: []teamsCheckpointGuardCover{}}, nil
}
func teamsFinalizeCheckpointHost(store credentialstore.Store, name string, pending []byte, r teamsCheckpointRecord) error {
	final, err := teamsCheckpointFinal(r)
	if err != nil {
		return err
	}
	raw, err := teamsEncodeCheckpoint(final)
	if err != nil {
		return err
	}
	return teamsCheckpointReplace(store, name, pending, raw)
}

// The callback is invoked at most once. Commit errors are deliberately not
// wrapped, so an outer catalog-busy retry cannot replay a published action.
func teamsCheckpointWrite(ctx context.Context, w *teamsProtectedWriter, store credentialstore.Store, name, scope string, guardKey []byte, run func(*sql.Tx) error) error {
	err := teamsCheckpointWriteWithHook(ctx, w, store, name, scope, guardKey, run, nil)
	if errors.Is(err, errTeamsCheckpointCommitted) {
		// The application mutation committed. Pending continuity is separately
		// visible and reconciled before the next protected write.
		return nil
	}
	return err
}

// beforeCommit is a fault-injection seam; it never runs after a commit attempt.
func teamsCheckpointWriteWithHook(ctx context.Context, w *teamsProtectedWriter, store credentialstore.Store, name, scope string, guardKey []byte, run func(*sql.Tx) error, beforeCommit func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return teamsCheckpointWriteLocked(ctx, w, store, name, scope, guardKey, run, beforeCommit, true)
}

func teamsCheckpointWriteLocked(ctx context.Context, w *teamsProtectedWriter, store credentialstore.Store, name, scope string, guardKey []byte, run func(*sql.Tx) error, beforeCommit func() error, allowReconcile bool) error {
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if w.requireFence {
		if err = teamsVerifyProtectionFence(ctx, tx); err != nil {
			return err
		}
		compatible, e := teamsOpenContractMatches(ctx, tx)
		if e != nil || !compatible {
			return errTeamsProtectionContinuity
		}
	}
	raw, host, err := teamsCheckpointLoad(store, name)
	if err != nil {
		return err
	}
	keyID, err := teamsCheckpointKeyID(guardKey)
	if err != nil || keyID != host.KeyID || host.Scope != scope {
		return errTeamsProtectionContinuity
	}
	if err = teamsRequireFormat35HostInstance(ctx, tx, w, host); err != nil {
		return err
	}
	if host.Phase == "held" {
		return teamsCheckpointWriteHeld(ctx, w, tx, host, run)
	}
	if host.Phase == "pending" {
		if !allowReconcile {
			return errTeamsCheckpointRetry
		}
		if err = tx.Rollback(); err != nil {
			return errTeamsCheckpointRetry
		}
		if err = teamsCheckpointReconcileLocked(ctx, w, store, name, scope, guardKey); err != nil && !errors.Is(err, errTeamsProtectionHeld) {
			return err
		}
		return teamsCheckpointWriteLocked(ctx, w, store, name, scope, guardKey, run, beforeCommit, false)
	}
	current, err := teamsCheckpointTupleFromSQL(ctx, tx)
	if err != nil {
		return errTeamsProtectionContinuity
	}
	revision, err := teamsCheckpointFenceRevision(ctx, tx)
	if err != nil || revision != current.Revision {
		return errTeamsProtectionContinuity
	}
	before, err := teamsProtectionProjection(ctx, tx)
	if errors.Is(err, errTeamsProjectionBound) {
		if host.Phase != "current" || host.Current == nil || *host.Current != *current || host.Sequence >= math.MaxInt64-1 {
			return errTeamsProtectionContinuity
		}
		guard, e := teamsCheckpointGuard(guardKey, "unknown", false)
		if e != nil {
			return e
		}
		held := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: host.KeyID, Sequence: host.Sequence + 1, Phase: "held", Current: current, Transition: "continuity_hold", Cover: []teamsCheckpointGuardCover{guard}}
		encoded, e := teamsEncodeCheckpoint(held)
		if e != nil {
			return e
		}
		if e = teamsCheckpointReplace(store, name, raw, encoded); e != nil {
			return e
		}
		return teamsCheckpointWriteHeld(ctx, w, tx, held, run)
	}
	if err != nil {
		return err
	}
	disposition := teamsClassifyCheckpoint(host, scope, current, "", before == current.Digest, true)
	if disposition != teamsCheckpointCurrent {
		if disposition == teamsCheckpointRecover || disposition == teamsCheckpointFinalize {
			return errTeamsCheckpointRetry
		}
		return errTeamsProtectionContinuity
	}
	if _, err = tx.ExecContext(ctx, `SAVEPOINT teams_protection_action`); err != nil {
		return err
	}
	w.authorized.Store(true)
	defer w.authorized.Store(false)
	if err = run(tx); err != nil {
		return err
	}
	var format string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&format); err != nil {
		return err
	}
	if format == "35" {
		if err = teamsPreflightOriginJournal(ctx, tx); err != nil {
			return err
		}
	}
	revision, err = teamsCheckpointFenceRevision(ctx, tx)
	if err != nil {
		return err
	}
	after, err := teamsProtectionProjection(ctx, tx)
	// A new oversized journal candidate is not evidence of prior corruption.
	// Undo it before any host CAS; an already-oversized prior projection still
	// follows the conservative hold path above.
	if errors.Is(err, errTeamsJournalProjectionBound) {
		if _, e := tx.ExecContext(ctx, `ROLLBACK TO teams_protection_action`); e != nil {
			return errTeamsCheckpointRetry
		}
		return errTeamsJournalAdmissionBound
	}
	if errors.Is(err, errTeamsProjectionBound) {
		if host.Sequence >= math.MaxInt64-1 {
			return errTeamsProtectionContinuity
		}
		guard, e := teamsCheckpointGuard(guardKey, "unknown", false)
		if e != nil {
			return e
		}
		held := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: host.KeyID, Sequence: host.Sequence + 1, Phase: "held", Current: current, Transition: "continuity_hold", Cover: []teamsCheckpointGuardCover{guard}}
		encoded, e := teamsEncodeCheckpoint(held)
		if e != nil {
			return e
		}
		if e = teamsCheckpointReplace(store, name, raw, encoded); e != nil {
			return e
		}
		return teamsCheckpointWriteHeld(ctx, w, tx, held, func(*sql.Tx) error { return nil })
	}
	if err != nil {
		return err
	}
	if before == after {
		if err = tx.Commit(); err != nil {
			return errTeamsCheckpointRetry
		}
		return nil
	}
	if host.Sequence >= math.MaxInt64-1 || current.Revision >= math.MaxInt64-1 || current.Generation >= math.MaxInt64-1 {
		return errTeamsProtectionContinuity
	}
	gen := max(host.Sequence, current.Generation) + 1
	id, err := teamsCheckpointRandom()
	if err != nil {
		return err
	}
	next := &teamsCheckpointTuple{Generation: gen, Instance: current.Instance, Revision: revision, CommitID: id, Digest: after}
	if err = teamsCheckpointPutTuple(ctx, tx, next); err != nil {
		return err
	}
	op, err := teamsCheckpointRandom()
	if err != nil {
		return err
	}
	cover, err := teamsCheckpointGuard(guardKey, "unknown", false)
	if err != nil {
		return err
	}
	pending := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: host.KeyID, Sequence: host.Sequence + 1, Phase: "pending", Previous: current, Next: next, Operation: op, Transition: "tightening", Cover: []teamsCheckpointGuardCover{cover}}
	armed, err := teamsEncodeCheckpoint(pending)
	if err != nil {
		return err
	}
	if err = teamsCheckpointReplace(store, name, raw, armed); err != nil {
		_ = teamsCheckpointUnwind(ctx, tx, store, name, raw, armed, host, before)
		return errTeamsCheckpointRetry
	}
	if beforeCommit != nil && beforeCommit() != nil {
		_ = teamsCheckpointUnwind(ctx, tx, store, name, raw, armed, host, before)
		return errTeamsCheckpointRetry
	}
	if err = tx.Commit(); err != nil {
		return errTeamsCheckpointRetry
	}
	if err = teamsFinalizeCheckpointHost(store, name, armed, pending); err != nil {
		return errTeamsCheckpointCommitted
	}
	return nil
}

// A durable library-wide host hold denies all proof even if SQLite rolls back.
// Ordinary protected writes remain available while the hold is retained; no
// generic acknowledgement or classification can release it.
func teamsCheckpointWriteHeld(ctx context.Context, w *teamsProtectedWriter, tx *sql.Tx, host teamsCheckpointRecord, run func(*sql.Tx) error) error {
	if host.Phase != "held" || !host.valid() {
		return errTeamsProtectionContinuity
	}
	var format string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='schema_version'").Scan(&format); err != nil {
		return err
	}
	var frozen bool
	if format == "35" {
		err := teamsPreflightOriginJournal(ctx, tx)
		if errors.Is(err, errTeamsJournalAdmissionBound) {
			// A previously inadmissible journal may be too large to hash.
			// Transaction-local triggers observe writes, including cascades,
			// without reading or copying its protected data.
			frozen = true
			if err := teamsObserveHeldJournalWrites(ctx, tx, true); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	w.authorized.Store(true)
	defer w.authorized.Store(false)
	guard := host.Cover[0]
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO teams_protection_recovery_guards(kind,scope,commitment) VALUES(?,?,?)`, guard.Kind, guard.Scope, guard.Commitment); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1 AND state<>'held'`); err != nil {
		return err
	}
	if err := run(tx); err != nil {
		return err
	}
	if frozen {
		if err := teamsObserveHeldJournalWrites(ctx, tx, false); err != nil {
			return err
		}
	} else if format == "35" {
		if err := teamsPreflightOriginJournal(ctx, tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return errTeamsCheckpointRetry
	}
	return nil
}

// Before a COMMIT attempt, unwind this operation only while retaining writer
// exclusion. Failed/ambiguous cleanup leaves pending for recovery.
func teamsCheckpointUnwind(ctx context.Context, tx *sql.Tx, store credentialstore.Store, name string, prior, armed []byte, host teamsCheckpointRecord, before string) error {
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO teams_protection_action`); err != nil {
		return errTeamsCheckpointRetry
	}
	digest, err := teamsProtectionProjection(ctx, tx)
	if err != nil || digest != before {
		return errTeamsCheckpointRetry
	}
	visible, err := store.Load(name)
	if err != nil {
		return errTeamsCheckpointRetry
	}
	if string(visible) == string(prior) {
		return nil
	}
	if string(visible) != string(armed) || host.Sequence >= math.MaxInt64-2 {
		return errTeamsCheckpointRetry
	}
	cancelled := teamsCheckpointRecord{Version: 1, Scope: host.Scope, KeyID: host.KeyID, Sequence: host.Sequence + 2, Phase: "current", Current: host.Current, Cover: []teamsCheckpointGuardCover{}}
	encoded, err := teamsEncodeCheckpoint(cancelled)
	if err != nil {
		return err
	}
	return teamsCheckpointReplace(store, name, armed, encoded)
}

// Reconcile only while holding the real immediate SQLite writer. Pending plus
// next is flushed with a changed FULL WAL transaction before host finalization.
// Tightening plus previous installs the original cover as durable opaque holds
// before reopening the proof path; another crash retains that same cover.
func teamsCheckpointReconcile(ctx context.Context, w *teamsProtectedWriter, store credentialstore.Store, name, scope string, guardKey []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return teamsCheckpointReconcileLocked(ctx, w, store, name, scope, guardKey)
}
func teamsCheckpointReconcileLocked(ctx context.Context, w *teamsProtectedWriter, store credentialstore.Store, name, scope string, guardKey []byte) error {
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if w.upgradePreflight {
		var format string
		if err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&format); err != nil {
			return err
		}
		if err = teamsSchemaInventoryPreflight(ctx, tx, format == "34"); err != nil {
			return err
		}
		if format == "34" {
			if _, err = teamsProtectionProjectionPreflight35(ctx, tx, teamsProtectionProjectionQueries, "pharos-teams-protection-projection-v1", teamsProjectionMaxBytes); err != nil {
				return err
			}
		}
	}
	if w.requireFence {
		if err = teamsVerifyProtectionFence(ctx, tx); err != nil {
			return err
		}
		compatible, e := teamsOpenContractMatches(ctx, tx)
		if e != nil || !compatible {
			return errTeamsProtectionContinuity
		}
	}
	raw, host, err := teamsCheckpointLoad(store, name)
	if err != nil {
		return err
	}
	keyID, e := teamsCheckpointKeyID(guardKey)
	if e != nil || keyID != host.KeyID || host.Scope != scope {
		return errTeamsProtectionContinuity
	}
	if err = teamsRequireFormat35HostInstance(ctx, tx, w, host); err != nil {
		return err
	}
	if host.Phase == "held" {
		return errTeamsProtectionHeld
	}
	current, err := teamsCheckpointTupleFromSQL(ctx, tx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errTeamsProtectionContinuity
	}
	projection, err := teamsProtectionProjection(ctx, tx)
	if errors.Is(err, errTeamsProjectionBound) && current != nil {
		if host.Phase != "current" && host.Phase != "pending" {
			return errTeamsProtectionContinuity
		}
		if host.Sequence >= math.MaxInt64-1 {
			return errTeamsProtectionContinuity
		}
		guard, e := teamsCheckpointGuard(guardKey, "unknown", false)
		if e != nil {
			return e
		}
		held := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: host.KeyID, Sequence: max(host.Sequence, current.Generation) + 1, Phase: "held", Current: current, Transition: "continuity_hold", Cover: []teamsCheckpointGuardCover{guard}}
		encoded, e := teamsEncodeCheckpoint(held)
		if e != nil {
			return e
		}
		if e = teamsCheckpointReplace(store, name, raw, encoded); e != nil {
			return e
		}
		return errTeamsProtectionHeld
	}
	if err != nil {
		return err
	}
	revision, err := teamsCheckpointFenceRevision(ctx, tx)
	if err != nil {
		return err
	}
	matches := current == nil || (projection == current.Digest && revision == current.Revision)
	w.authorized.Store(true)
	defer w.authorized.Store(false)
	switch teamsClassifyCheckpoint(host, scope, current, projection, matches, true) {
	case teamsCheckpointCurrent, teamsCheckpointUnactivated:
		return nil
	case teamsCheckpointFinalize:
		if _, err = tx.ExecContext(ctx, `UPDATE teams_protection_flush SET revision=revision+1 WHERE id=1 AND revision<9223372036854775807`); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return errTeamsCheckpointRetry
		}
		return teamsFinalizeCheckpointHost(store, name, raw, host)
	case teamsCheckpointRecover:
		if host.Sequence >= math.MaxInt64-1 {
			return errTeamsProtectionContinuity
		}
		if host.Transition == "no_tightening" {
			if current == nil {
				restored := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: host.KeyID, Sequence: host.Sequence + 1, Phase: "unactivated", PreviousUnactivated: projection, Cover: []teamsCheckpointGuardCover{}}
				encoded, e := teamsEncodeCheckpoint(restored)
				if e != nil {
					return e
				}
				return teamsCheckpointReplace(store, name, raw, encoded)
			}
			restored := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: host.KeyID, Sequence: host.Sequence + 1, Phase: "current", Current: current, Cover: []teamsCheckpointGuardCover{}}
			encoded, e := teamsEncodeCheckpoint(restored)
			if e != nil {
				return e
			}
			return teamsCheckpointReplace(store, name, raw, encoded)
		}
		if current == nil {
			return errTeamsProtectionContinuity
		}
		for _, guard := range host.Cover {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO teams_protection_recovery_guards(kind,scope,commitment) VALUES(?,?,?)`, guard.Kind, guard.Scope, guard.Commitment); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1 AND state<>'held'`); err != nil {
			return err
		}
		revision, e := teamsCheckpointFenceRevision(ctx, tx)
		if e != nil {
			return e
		}
		digest, e := teamsProtectionProjection(ctx, tx)
		if e != nil {
			return e
		}
		if current.Revision >= math.MaxInt64-1 || current.Generation >= math.MaxInt64-1 {
			return errTeamsProtectionContinuity
		}
		id, e := teamsCheckpointRandom()
		if e != nil {
			return e
		}
		op, e := teamsCheckpointRandom()
		if e != nil {
			return e
		}
		next := &teamsCheckpointTuple{Generation: max(host.Sequence, current.Generation) + 1, Instance: current.Instance, Revision: revision, CommitID: id, Digest: digest}
		if e = teamsCheckpointPutTuple(ctx, tx, next); e != nil {
			return e
		}
		successor := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: host.KeyID, Sequence: host.Sequence + 1, Phase: "pending", Previous: current, Next: next, Operation: op, Transition: "tightening", Cover: host.Cover}
		pending, e := teamsEncodeCheckpoint(successor)
		if e != nil {
			return e
		}
		if e = teamsCheckpointReplace(store, name, raw, pending); e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return errTeamsCheckpointRetry
		}
		return teamsFinalizeCheckpointHost(store, name, pending, successor)
	default:
		return errTeamsProtectionContinuity
	}
}

// teamsCheckpointActivate is an inactive integration seam. The caller must
// prove managed provenance and prior-origin/restore eligibility in verify;
// it must not mutate the projection. No production caller exists yet.
func teamsCheckpointActivate(ctx context.Context, w *teamsProtectedWriter, store credentialstore.Store, name, scope, instance string, guardKey []byte, verify func(*sql.Tx) error, beforeCommit func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = teamsCheckpointTupleFromSQL(ctx, tx); err == nil {
		return errTeamsProtectionContinuity
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	prior, err := teamsProtectionProjection(ctx, tx)
	if err != nil {
		return err
	}
	if verify == nil {
		return errTeamsProtectionContinuity
	}
	if err = verify(tx); err != nil {
		return err
	}
	checked, err := teamsProtectionProjection(ctx, tx)
	if err != nil || checked != prior {
		return errTeamsProtectionContinuity
	}
	revision, err := teamsCheckpointFenceRevision(ctx, tx)
	if err != nil {
		return err
	}
	keyID, err := teamsCheckpointKeyID(guardKey)
	if err != nil {
		return err
	}
	raw, err := store.Load(name)
	var sequence int64
	if errors.Is(err, os.ErrNotExist) {
		sequence = 0
	} else if err != nil {
		return errTeamsCheckpointRetry
	} else {
		host, e := teamsParseCheckpoint(raw)
		if e != nil {
			return e
		}
		if host.Phase != "unactivated" || host.Scope != scope || host.KeyID != keyID || host.PreviousUnactivated != prior {
			return errTeamsProtectionContinuity
		}
		sequence = host.Sequence
	}
	if sequence >= math.MaxInt64-2 || !teamsCheckpointHex(instance, 64) {
		return errTeamsProtectionContinuity
	}
	w.authorized.Store(true)
	defer w.authorized.Store(false)
	if _, err = tx.ExecContext(ctx, `SAVEPOINT teams_protection_action`); err != nil {
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
	// Version marker and checkpoint tuple commit atomically; the pending arm
	// still binds the pre-activation projection for crash reconciliation.
	result, e := tx.ExecContext(ctx, `UPDATE meta SET value=? WHERE key='schema_version' AND value=?`, strconv.Itoa(protectedCatalogSchemaVersion), strconv.Itoa(catalogSchemaVersion))
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		return errTeamsProtectionContinuity
	}
	activeDigest, e := teamsProtectionProjection(ctx, tx)
	if e != nil {
		return e
	}
	next := &teamsCheckpointTuple{Generation: sequence + 1, Instance: instance, Revision: revision, CommitID: id, Digest: activeDigest}
	if err = teamsCheckpointPutTuple(ctx, tx, next); err != nil {
		return err
	}
	pending := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: keyID, Sequence: sequence + 1, Phase: "pending", PreviousUnactivated: prior, Next: next, Operation: op, Transition: "no_tightening", Cover: []teamsCheckpointGuardCover{}}
	armed, err := teamsEncodeCheckpoint(pending)
	if err != nil {
		return err
	}
	if raw == nil {
		err = store.Save(name, armed)
		if err != nil {
			visible, e := store.Load(name)
			if e != nil || string(visible) != string(armed) || store.Replace(name, armed, armed) != nil {
				_ = teamsCheckpointUnwindInitial(ctx, tx, store, name, armed, pending, prior)
				return errTeamsCheckpointRetry
			}
			err = nil
		}
	} else {
		err = teamsCheckpointReplace(store, name, raw, armed)
	}
	if err != nil || (beforeCommit != nil && beforeCommit() != nil) {
		_ = teamsCheckpointUnwindInitial(ctx, tx, store, name, armed, pending, prior)
		return errTeamsCheckpointRetry
	}
	if err = tx.Commit(); err != nil {
		return errTeamsCheckpointRetry
	}
	if err = teamsFinalizeCheckpointHost(store, name, armed, pending); err != nil {
		return errTeamsCheckpointCommitted
	}
	return nil
}

func teamsCheckpointUnwindInitial(ctx context.Context, tx *sql.Tx, store credentialstore.Store, name string, armed []byte, pending teamsCheckpointRecord, prior string) error {
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO teams_protection_action`); err != nil {
		return errTeamsCheckpointRetry
	}
	digest, err := teamsProtectionProjection(ctx, tx)
	if err != nil || digest != prior {
		return errTeamsCheckpointRetry
	}
	visible, err := store.Load(name)
	if err != nil || string(visible) != string(armed) {
		return errTeamsCheckpointRetry
	}
	if pending.Sequence >= math.MaxInt64-1 {
		return errTeamsProtectionContinuity
	}
	cancelled := teamsCheckpointRecord{Version: 1, Scope: pending.Scope, KeyID: pending.KeyID, Sequence: pending.Sequence + 1, Phase: "unactivated", PreviousUnactivated: prior, Cover: []teamsCheckpointGuardCover{}}
	encoded, err := teamsEncodeCheckpoint(cancelled)
	if err != nil {
		return err
	}
	return teamsCheckpointReplace(store, name, armed, encoded)
}

// A destructive catalog replacement must first put the independent host
// checkpoint into a conservative hold. The host hold survives a crash before
// the replacement or a rollback of either catalog, and never grants proof.
func teamsCheckpointHoldForReplacement(ctx context.Context, r *teamsCheckpointRuntime) error {
	w := r.writer
	w.mu.Lock()
	defer w.mu.Unlock()
	tx, err := teamsBeginProtectedWrite(ctx, w)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if w.requireFence {
		if err = teamsVerifyProtectionFence(ctx, tx); err != nil {
			return err
		}
		ok, e := teamsOpenContractMatches(ctx, tx)
		if e != nil || !ok {
			return errTeamsProtectionContinuity
		}
	}
	raw, host, err := teamsCheckpointLoad(r.store, r.name)
	if err != nil {
		return err
	}
	keyID, err := teamsCheckpointKeyID(r.guardKey)
	if err != nil || keyID != host.KeyID || host.Scope != r.scope {
		return errTeamsProtectionContinuity
	}
	if err = teamsRequireFormat35HostInstance(ctx, tx, w, host); err != nil {
		return err
	}
	if host.Phase == "held" {
		return nil
	}
	current, err := teamsCheckpointTupleFromSQL(ctx, tx)
	if err != nil {
		return errTeamsProtectionContinuity
	}
	if host.Phase != "current" && host.Phase != "pending" {
		return errTeamsProtectionContinuity
	}
	guard, err := teamsCheckpointGuard(r.guardKey, "unknown", false)
	if err != nil {
		return err
	}
	if max(host.Sequence, current.Generation) >= math.MaxInt64-1 {
		return errTeamsProtectionContinuity
	}
	held := teamsCheckpointRecord{Version: 1, Scope: r.scope, KeyID: host.KeyID, Sequence: max(host.Sequence, current.Generation) + 1, Phase: "held", Current: current, Transition: "continuity_hold", Cover: []teamsCheckpointGuardCover{guard}}
	encoded, err := teamsEncodeCheckpoint(held)
	if err != nil {
		return err
	}
	return teamsCheckpointReplace(r.store, r.name, raw, encoded)
}
