package archive

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/platform/credentialstore"
)

type teamsCheckpointTestStore struct {
	raw                         []byte
	failAfterRename             bool
	replaceCount, failReplaceAt int
}

func (s *teamsCheckpointTestStore) Save(_ string, b []byte) error {
	if s.raw != nil {
		return credentialstore.ErrExists
	}
	s.raw = bytes.Clone(b)
	return nil
}
func (s *teamsCheckpointTestStore) Load(_ string) ([]byte, error) {
	if s.raw == nil {
		return nil, os.ErrNotExist
	}
	return bytes.Clone(s.raw), nil
}
func (s *teamsCheckpointTestStore) Replace(_ string, old, next []byte) error {
	s.replaceCount++
	if s.failReplaceAt == s.replaceCount {
		return credentialstore.ErrUnsafe
	}
	if !bytes.Equal(old, s.raw) {
		return credentialstore.ErrUnsafe
	}
	s.raw = bytes.Clone(next)
	if s.failAfterRename {
		s.failAfterRename = false
		return errors.New("synthetic after-rename failure")
	}
	return nil
}
func (s *teamsCheckpointTestStore) Delete(_ string, old []byte) error {
	if !bytes.Equal(old, s.raw) {
		return credentialstore.ErrUnsafe
	}
	s.raw = nil
	return nil
}

func TestTeamsCheckpointWriteAndRecoveryLifecycle(t *testing.T) {
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
	digest, err := teamsProtectionProjection(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	prior := &teamsCheckpointTuple{Generation: 1, Instance: strings.Repeat("a", 64), Revision: 0, CommitID: strings.Repeat("b", 32), Digest: digest}
	if err = teamsCheckpointPutTuple(ctx, tx, prior); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	scope := teamsCheckpointScope("app", "library")
	key := bytes.Repeat([]byte{42}, 32)
	keyID, err := teamsCheckpointKeyID(key)
	if err != nil {
		t.Fatal(err)
	}
	store := &teamsCheckpointTestStore{}
	initial := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: keyID, Sequence: 1, Phase: "current", Current: prior, Cover: []teamsCheckpointGuardCover{}}
	store.raw, err = teamsEncodeCheckpoint(initial)
	if err != nil {
		t.Fatal(err)
	}
	action := func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,user_private) VALUES('c','test','local','n',1)`)
		return err
	}
	if err = teamsCheckpointWriteWithHook(ctx, w, store, "checkpoint", scope, key, action, func() error { return errors.New("synthetic interruption") }); !errors.Is(err, errTeamsCheckpointRetry) {
		t.Fatalf("unwind: %v", err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_conversation_privacy WHERE conversation_id='c'`); n != 0 {
		t.Fatal("precommit action survived unwind")
	}
	afterCancel, e := teamsParseCheckpoint(store.raw)
	if e != nil || afterCancel.Phase != "current" || afterCancel.Sequence <= initial.Sequence {
		t.Fatal("cancel did not preserve current and advance sequence", e)
	}
	store.failAfterRename = true
	if err = teamsCheckpointWrite(ctx, w, store, "checkpoint", scope, key, action); err != nil {
		t.Fatal(err)
	}
	committed, e := teamsParseCheckpoint(store.raw)
	if e != nil || committed.Phase != "current" || committed.Current.Generation <= afterCancel.Sequence {
		t.Fatal("commit not finalized", e)
	}
	// Model a crash after arming a later tightening and before its SQL commit.
	cover, e := teamsCheckpointGuard(key, "positive", false)
	if e != nil {
		t.Fatal(e)
	}
	pending := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: keyID, Sequence: committed.Sequence + 1, Phase: "pending", Previous: committed.Current, Next: &teamsCheckpointTuple{Generation: committed.Sequence + 1, Instance: prior.Instance, Revision: committed.Current.Revision + 1, CommitID: strings.Repeat("c", 32), Digest: strings.Repeat("d", 64)}, Operation: strings.Repeat("e", 32), Transition: "tightening", Cover: []teamsCheckpointGuardCover{cover}}
	store.raw, e = teamsEncodeCheckpoint(pending)
	if e != nil {
		t.Fatal(e)
	}
	if got := teamsClassifyCheckpoint(pending, scope, committed.Current, "", true, false); got != teamsCheckpointDefer {
		t.Fatal("live reader did not defer")
	}
	if err = teamsCheckpointReconcile(ctx, w, store, "checkpoint", scope, key); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_recovery_guards WHERE kind='positive' AND scope='library'`); n != 1 {
		t.Fatal("interrupted positive lost")
	}
	resolved, e := teamsParseCheckpoint(store.raw)
	if e != nil || resolved.Phase != "current" || resolved.Sequence <= pending.Sequence {
		t.Fatal("recovery did not finalize", e)
	}
	if err = teamsCheckpointReconcile(ctx, w, store, "checkpoint", scope, key); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(store.raw, mustCheckpointBytes(t, resolved)) {
		t.Fatal("clean reconciliation mutated host")
	}
	// Model a lost commit response: the committed next tuple requires a real
	// FULL WAL barrier before its pending host arm may be finalized.
	pendingNext := teamsCheckpointRecord{Version: 1, Scope: scope, KeyID: keyID, Sequence: resolved.Current.Generation, Phase: "pending", Previous: committed.Current, Next: resolved.Current, Operation: strings.Repeat("f", 32), Transition: "tightening", Cover: []teamsCheckpointGuardCover{cover}}
	store.raw, e = teamsEncodeCheckpoint(pendingNext)
	if e != nil {
		t.Fatal(e)
	}
	if err = teamsCheckpointReconcile(ctx, w, store, "checkpoint", scope, key); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT revision FROM teams_protection_flush WHERE id=1`); n != 1 {
		t.Fatalf("durability barrier missing: %d", n)
	}
	// An uncovered writer cannot be mistaken for a valid current projection.
	execTx(t, c.DB, `UPDATE teams_conversation_privacy SET user_private=0 WHERE conversation_id='c'`)
	if err = teamsCheckpointReconcile(ctx, w, store, "checkpoint", scope, key); !errors.Is(err, errTeamsProtectionContinuity) {
		t.Fatalf("uncovered mutation accepted: %v", err)
	}
}
func mustCheckpointBytes(t *testing.T, r teamsCheckpointRecord) []byte {
	t.Helper()
	b, err := teamsEncodeCheckpoint(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTeamsCheckpointInitialActivationCrashAndRetry(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", "library")
	key := bytes.Repeat([]byte{42}, 32)
	instance := strings.Repeat("a", 64)
	verify := func(*sql.Tx) error { return nil }
	if err = teamsCheckpointActivate(ctx, w, store, "checkpoint", scope, instance, key, verify, func() error { return errors.New("fault") }); !errors.Is(err, errTeamsCheckpointRetry) {
		t.Fatalf("initial unwind: %v", err)
	}
	state, err := teamsParseCheckpoint(store.raw)
	if err != nil || state.Phase != "unactivated" || state.Sequence != 2 {
		t.Fatalf("unactivated state: %+v %v", state, err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_checkpoint`); n != 0 {
		t.Fatal("failed activation committed")
	}
	if err = teamsCheckpointActivate(ctx, w, store, "checkpoint", scope, instance, key, verify, nil); err != nil {
		t.Fatal(err)
	}
	active, err := teamsParseCheckpoint(store.raw)
	if err != nil || active.Phase != "current" || active.Current.Generation <= state.Sequence {
		t.Fatalf("activation: %+v %v", active, err)
	}
	if err = teamsCheckpointReconcile(ctx, w, store, "checkpoint", scope, key); err != nil {
		t.Fatal(err)
	}
}

// This tests process death and WAL recovery, not loss of power to a drive.
func TestTeamsCheckpointProcessCrash(t *testing.T) {
	if mode := os.Getenv("PHAROS_CHECKPOINT_CRASH_HELPER"); mode != "" {
		path, root := os.Getenv("PHAROS_CHECKPOINT_TEST_PATH"), os.Getenv("PHAROS_CHECKPOINT_TEST_STORE")
		w, err := teamsOpenProtectedWriter(path)
		if err != nil {
			t.Fatal(err)
		}
		defer w.db.Close()
		store := credentialstore.New(root)
		scope := teamsCheckpointScope("app", "library")
		key := bytes.Repeat([]byte{42}, 32)
		hook := func() error {
			_, _ = os.Stdout.WriteString("armed\n")
			var b [1]byte
			_, _ = os.Stdin.Read(b[:])
			return errors.New("unexpected release")
		}
		if mode == "initial" {
			err = teamsCheckpointActivate(context.Background(), w, store, scope, scope, strings.Repeat("a", 64), key, func(*sql.Tx) error { return nil }, hook)
		} else {
			err = teamsCheckpointWriteWithHook(context.Background(), w, store, scope, scope, key, func(tx *sql.Tx) error {
				_, e := tx.Exec(`INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,user_private) VALUES('c','test','local','n',1)`)
				return e
			}, hook)
		}
		t.Fatalf("helper returned: %v", err)
	}
	slowTest(t)
	useDurableCatalog(t)
	c, _ := testCatalog(t)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','n')`)
	scope := teamsCheckpointScope("app", "library")
	root := filepath.Join(t.TempDir(), "credentials")
	killAfterArm := func(mode string) {
		t.Helper()
		childCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestTeamsCheckpointProcessCrash$")
		cmd.Env = append(os.Environ(), "PHAROS_TEST_PROCESSES=1", "PHAROS_CHECKPOINT_CRASH_HELPER="+mode, "PHAROS_CHECKPOINT_TEST_PATH="+c.Path, "PHAROS_CHECKPOINT_TEST_STORE="+root)
		out, e := cmd.StdoutPipe()
		if e != nil {
			t.Fatal(e)
		}
		in, e := cmd.StdinPipe()
		if e != nil {
			t.Fatal(e)
		}
		defer in.Close()
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		scanner := bufio.NewScanner(out)
		if !scanner.Scan() || scanner.Text() != "armed" {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("helper did not arm: %q %v", scanner.Text(), scanner.Err())
		}
		if e = cmd.Process.Kill(); e != nil {
			t.Fatal(e)
		}
		_ = cmd.Wait()
	}
	killAfterArm("initial")
	store := credentialstore.New(root)
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	key := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointReconcile(context.Background(), w, store, scope, scope, key); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Load(scope)
	if err != nil {
		t.Fatal(err)
	}
	state, err := teamsParseCheckpoint(raw)
	if err != nil || state.Phase != "unactivated" {
		t.Fatalf("crash recovery: %+v %v", state, err)
	}
	if err = teamsCheckpointActivate(context.Background(), w, store, scope, scope, strings.Repeat("a", 64), key, func(*sql.Tx) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	killAfterArm("positive")
	if err = teamsCheckpointReconcile(context.Background(), w, store, scope, scope, key); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_recovery_guards WHERE kind='unknown' AND scope='library'`); n != 1 {
		t.Fatal("interrupted positive had no durable cover")
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_baseline WHERE state='held'`); n != 1 {
		t.Fatal("interrupted positive did not hold baseline")
	}
}

func TestTeamsCheckpointCapacityHoldKeepsLocalWrites(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','n')`)
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", "library")
	key := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointActivate(ctx, w, store, "checkpoint", scope, strings.Repeat("a", 64), key, func(*sql.Tx) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	override(t, &teamsProjectionMaxBytes, 4096)
	if err = teamsCheckpointWrite(ctx, w, store, "checkpoint", scope, key, func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,user_private,classifier_input) VALUES('c','test','local','n',1,?)`, strings.Repeat("x", 6000))
		return e
	}); err != nil {
		t.Fatalf("privacy write at capacity: %v", err)
	}
	host, err := teamsParseCheckpoint(store.raw)
	if err != nil || host.Phase != "held" {
		t.Fatalf("missing durable host hold: %+v %v", host, err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_recovery_guards WHERE kind='unknown' AND scope='library'`); n != 1 {
		t.Fatal("missing catalog hold")
	}
	if err = teamsCheckpointWrite(ctx, w, store, "checkpoint", scope, key, func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE teams_conversation_privacy SET user_private=0 WHERE conversation_id='c'`)
		return e
	}); err != nil {
		t.Fatalf("subsequent local write: %v", err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_assertions WHERE conversation_id='c' AND kind='explicit_private' AND active=1`); n != 1 {
		t.Fatal("held write lost retained private assertion")
	}
	restored, err := teamsParseCheckpoint(store.raw)
	if err != nil || restored.Phase != "held" {
		t.Fatal("local write released host hold", err)
	}
}

func TestTeamsCheckpointFinalizationFailureReconcilesNextWrite(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','test','local','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','test','local','n')`)
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.db.Close()
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", "library")
	key := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointActivate(ctx, w, store, "checkpoint", scope, strings.Repeat("a", 64), key, func(*sql.Tx) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	// Pending publication succeeds; the final host replacement fails before rename.
	store.failReplaceAt = store.replaceCount + 2
	firstCalls := 0
	err = teamsCheckpointWrite(ctx, w, store, "checkpoint", scope, key, func(tx *sql.Tx) error {
		firstCalls++
		_, e := tx.Exec(`INSERT INTO teams_conversation_privacy(conversation_id,provider,account,native_id,user_private) VALUES('c','test','local','n',1)`)
		return e
	})
	if err != nil || firstCalls != 1 {
		t.Fatalf("committed result: calls=%d err=%v", firstCalls, err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_conversation_privacy WHERE conversation_id='c' AND user_private=1`); n != 1 {
		t.Fatal("committed mutation missing")
	}
	pending, err := teamsParseCheckpoint(store.raw)
	if err != nil || pending.Phase != "pending" {
		t.Fatal("failed finalization did not leave pending", err)
	}
	secondCalls := 0
	if err = teamsCheckpointWrite(ctx, w, store, "checkpoint", scope, key, func(tx *sql.Tx) error {
		secondCalls++
		_, e := tx.Exec(`UPDATE teams_conversation_privacy SET user_private=0 WHERE conversation_id='c'`)
		return e
	}); err != nil {
		t.Fatalf("next action did not reconcile: %v", err)
	}
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("business action replayed: %d %d", firstCalls, secondCalls)
	}
	if n := countRows(t, c, `SELECT revision FROM teams_protection_flush WHERE id=1`); n != 1 {
		t.Fatal("pending-next lacked durable barrier")
	}
}
