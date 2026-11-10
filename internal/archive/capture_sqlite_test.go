package archive

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/platform/localidentity"
)

// Snapshots step through one read transaction: a writer committing between
// every one-page step neither restarts the copy forever nor makes it
// inconsistent, and cancelling stops it.
func TestSnapshotSQLiteStepsThroughOneReadTransaction(t *testing.T) {
	previous := backupStepPages
	backupStepPages = 1
	t.Cleanup(func() { backupStepPages = previous })
	dir := t.TempDir()
	file := filepath.Join(dir, "source.db")
	db := openWritableSQLite(t, file)
	execSQL(t, db, "CREATE TABLE sessions(id TEXT PRIMARY KEY, message_count INTEGER)", "CREATE TABLE messages(id INTEGER PRIMARY KEY, session_id TEXT, body TEXT)",
		"INSERT INTO sessions VALUES('s1',0)")
	body := strings.Repeat("x", 4000)
	commit := func() error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec("INSERT INTO messages(session_id,body) VALUES('s1',?)", body); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE sessions SET message_count=message_count+1 WHERE id='s1'"); err != nil {
			return err
		}
		return tx.Commit()
	}
	// What commit would write 2000 times, in one transaction.
	seed := seedTx(t, db)
	for range 2000 {
		if _, err := seed.Exec("INSERT INTO messages(session_id,body) VALUES('s1',?)", body); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := seed.Exec("UPDATE sessions SET message_count=2000 WHERE id='s1'"); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, seed)
	var commits atomic.Int64
	stop, done := make(chan struct{}), make(chan error)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if err := commit(); err != nil {
				done <- err
				return
			}
			commits.Add(1)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target := filepath.Join(dir, "snapshot.db")
	var copied, total int64
	partial := false
	err := snapshotSQLiteWithProgress(ctx, file, target, func(done, size int64) {
		if size <= 0 || done < copied || done > size || total != 0 && size != total {
			t.Fatalf("snapshot progress: %d of %d after %d of %d", done, size, copied, total)
		}
		partial = partial || done > 0 && done < size
		copied, total = done, size
	})
	during := commits.Load()
	close(stop)
	if writeErr := <-done; writeErr != nil {
		t.Fatalf("writer: %v", writeErr)
	}
	if err != nil {
		t.Fatalf("snapshot with one-page steps under %d commits: %v", during, err)
	}
	if during == 0 {
		t.Fatal("the writer never committed during the snapshot")
	}
	if info, statErr := os.Stat(target); statErr != nil || !partial || copied != total || copied != info.Size() {
		t.Fatalf("snapshot progress did not reach its actual size: %d of %d, partial=%v, stat=%v", copied, total, partial, statErr)
	}
	snapshot, err := openReadOnlySQLite(target)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var counted, messages int
	var integrity string
	if err := snapshot.QueryRow("SELECT message_count,(SELECT COUNT(*) FROM messages) FROM sessions").Scan(&counted, &messages); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if counted != messages || messages < 2000 || integrity != "ok" {
		t.Fatalf("snapshot: message_count=%d messages=%d integrity=%s", counted, messages, integrity)
	}
	t.Logf("snapshot of %d messages while the writer committed %d times", messages, during)

	// Cancelled a few pages into a copy of thousands, it stops.
	cancelled := &cancelAfter{Context: context.Background()}
	cancelled.left.Store(5)
	if err := backupSQLite(cancelled, openSourceSQLite, file, filepath.Join(dir, "cancelled.db"), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled snapshot: %v", err)
	}
}

// cancelAfter reports itself cancelled once Err has been asked enough times.
type cancelAfter struct {
	context.Context
	left atomic.Int64
}

func (c *cancelAfter) Err() error {
	if c.left.Add(-1) < 0 {
		return context.Canceled
	}
	return nil
}

// replaceKeepingStat renames a new file over name with the same bytes and
// modification time, while the old one stays open so its inode is not reused.
func replaceKeepingStat(t *testing.T, name string) {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Close() })
	if err := os.WriteFile(name+".new", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name+".new", info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name+".new", name); err != nil {
		t.Fatal(err)
	}
}

// A database's dependency state names the database and, only while it holds
// frames, its WAL, so either can be replaced without changing size or time
// and still invalidate what depends on it. An identity it cannot observe is
// an error, not an empty identity.
func TestSQLiteDependencyStateIdentities(t *testing.T) {
	file := filepath.Join(t.TempDir(), "source.db")
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	observe := func() sqliteSourceState {
		t.Helper()
		state, err := sqliteDependencyState(file)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	legacy := func(name string) string {
		t.Helper()
		key, err := localidentity.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		text, err := key.DeviceInode()
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	write(file, "database")
	// An absent and an empty WAL need no identity.
	state := observe()
	if state.Identity != legacy(file) || state.WALIdentity != "" || state.Size != 8 || state.MTimeNS != stamp.UnixNano() {
		t.Fatalf("database alone: %+v", state)
	}
	if again := observe(); again != state {
		t.Fatalf("same files observed differently: %+v then %+v", state, again)
	}
	write(file+"-wal", "")
	if state = observe(); state.WALIdentity != "" || state.WALSize != 0 || state.WALMTimeNS != stamp.UnixNano() {
		t.Fatalf("empty WAL: %+v", state)
	}
	write(file+"-wal", "frames")
	state = observe()
	if state.WALIdentity != legacy(file+"-wal") || state.WALIdentity == state.Identity || state.WALSize != 6 {
		t.Fatalf("nonempty WAL: %+v", state)
	}
	if again := observe(); again != state {
		t.Fatalf("same files observed differently: %+v then %+v", state, again)
	}

	replaceKeepingStat(t, file)
	replaced := observe()
	if replaced.Identity == state.Identity || replaced.WALIdentity != state.WALIdentity {
		t.Fatalf("database replacement: %+v, was %+v", replaced, state)
	}
	state = replaced
	replaceKeepingStat(t, file+"-wal")
	replaced = observe()
	if replaced.WALIdentity == state.WALIdentity || replaced.Identity != state.Identity {
		t.Fatalf("WAL replacement: %+v, was %+v", replaced, state)
	}
	replaced.Identity, replaced.WALIdentity, state.Identity, state.WALIdentity = "", "", "", ""
	if replaced != state {
		t.Fatalf("replacement changed size or time: %+v, was %+v", replaced, state)
	}

	// A failed identity is the error itself and never reads as a missing
	// file, so a capture keeps what it had; the WAL's matters only while it
	// holds frames.
	for _, failing := range []string{"source.db", "source.db-wal"} {
		override(t, &localIdentity, func(info fs.FileInfo) (localidentity.Key, error) {
			if info.Name() == failing {
				return localidentity.Key{}, localidentity.ErrUnavailable
			}
			return localidentity.Of(info)
		})
		state, err := sqliteDependencyState(file)
		if !errors.Is(err, localidentity.ErrUnavailable) || errors.Is(err, fs.ErrNotExist) || state != (sqliteSourceState{}) {
			t.Fatalf("%s identity failure: %+v, %v", failing, state, err)
		}
	}
	write(file+"-wal", "")
	if state, err := sqliteDependencyState(file); err != nil || state.WALIdentity != "" {
		t.Fatalf("empty WAL needed its identity: %+v, %v", state, err)
	}
}

// A capture that cannot identify a database records a failure and keeps the
// snapshot it has, as current rather than missing, and reuses it once the
// identity is available again.
func TestCaptureKeepsSnapshotWhenIdentityFails(t *testing.T) {
	useHost(t, "host-a")
	conductor := filepath.Join(t.TempDir(), "conductor")
	if err := os.MkdirAll(conductor, 0o755); err != nil {
		t.Fatal(err)
	}
	db := openWritableSQLite(t, filepath.Join(conductor, "conductor.db"))
	execSQL(t, db, "CREATE TABLE sessions(id TEXT PRIMARY KEY)", "CREATE TABLE messages(id INTEGER PRIMARY KEY, session_id TEXT)", "INSERT INTO sessions VALUES('kept')")
	config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: conductor, Enabled: true})
	if summary := runCapture(t, config); !summary.OK {
		t.Fatalf("first capture: %#v", summary)
	}
	before := readCaptureManifest(t, config, "conductor").Snapshots[0]
	if before.Source.Identity == "" {
		t.Fatalf("snapshot has no source identity: %+v", before)
	}

	attempts := 0
	override(t, &localIdentity, func(fs.FileInfo) (localidentity.Key, error) {
		attempts++
		return localidentity.Key{}, localidentity.ErrUnsupported
	})
	summary, err := Capture(t.Context(), config, config.Sources, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result := captureResult(t, summary, "conductor"); summary.OK || attempts == 0 || result.SnapshotsTaken != 0 || result.SnapshotsUnchanged != 0 {
		t.Fatalf("unidentified database: %#v", summary)
	}
	manifest := readCaptureManifest(t, config, "conductor")
	if len(manifest.Snapshots) != 1 || manifest.Snapshots[0].MissingSince != "" || manifest.Snapshots[0].Source != before.Source || manifest.Snapshots[0].Captured != before.Captured {
		t.Fatalf("identity failure changed the snapshot: %+v, was %+v", manifest.Snapshots, before)
	}
	if _, err := os.Stat(capturedPath(config, "conductor", before.Captured)); err != nil {
		t.Fatal(err)
	}

	localIdentity = localidentity.Of
	if result := captureResult(t, runCapture(t, config), "conductor"); result.SnapshotsUnchanged != 1 || result.SnapshotsTaken != 0 {
		t.Fatalf("restored identity did not reuse the snapshot: %#v", result)
	}
}
