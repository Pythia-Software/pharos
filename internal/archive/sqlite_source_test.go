package archive

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sqliteLadderTest lowers the live read's wait and keeps copies in a folder
// the test can see empty again.
func sqliteLadderTest(t *testing.T) string {
	t.Helper()
	override(t, &sourceBusyTimeoutMS, 20)
	copies := filepath.Join(t.TempDir(), "copies")
	override(t, &sqliteCopyRoot, copies)
	return copies
}

// sqliteHolder is another app's connection to a database, holding whatever
// lock its statements leave until release.
func sqliteHolder(t *testing.T, path string, statements ...string) (holder *sql.Conn, release func()) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	for _, statement := range statements {
		if _, err := conn.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	return conn, func() { conn.Close(); db.Close() }
}

func sqliteNotes(t *testing.T, path string, journal string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=` + journal + `; CREATE TABLE notes(body TEXT); INSERT INTO notes VALUES('first')`); err != nil {
		t.Fatal(err)
	}
}

func sqliteBodies(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT body FROM notes ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var bodies []string
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(bodies, ",")
}

func sqliteReadVia(path string) string {
	if read, ok := sqliteReads.Load(path); ok {
		return read.(sqliteRead).Via
	}
	return sqliteReadLive
}

func assertNoCopies(t *testing.T, copies string) {
	t.Helper()
	entries, err := os.ReadDir(copies)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("copies left after close: %v", entries)
	}
}

func TestSourceSQLiteStepsDownTheLadder(t *testing.T) {
	copies := sqliteLadderTest(t)
	t.Run("an unlocked database is read in place", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "free.db")
		sqliteNotes(t, path, "WAL")
		db, err := openSourceSQLite(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if got := sqliteBodies(t, db); got != "first" || sqliteReadVia(path) != sqliteReadLive {
			t.Fatalf("read %q via %s", got, sqliteReadVia(path))
		}
		assertNoCopies(t, copies)
	})
	t.Run("a database another app holds locked is read from a copy", func(t *testing.T) {
		for _, journal := range []string{"DELETE", "WAL"} {
			path := filepath.Join(t.TempDir(), journal+".db")
			sqliteNotes(t, path, journal)
			// Exclusive locking mode keeps the lock after the commit; in
			// WAL mode the commit stays in the WAL, which the copy folds in.
			sqliteHolder(t, path, `PRAGMA locking_mode=EXCLUSIVE`, `INSERT INTO notes VALUES('second')`)
			if journal == "WAL" {
				if pending, _ := sqlitePendingState(path); pending.hot || !pending.frames {
					t.Fatalf("%s: %+v: the fixture has no commit in its WAL", journal, pending)
				}
			}
			db, err := openSourceSQLite(context.Background(), path)
			if err != nil {
				t.Fatalf("%s: %v", journal, err)
			}
			if got := sqliteBodies(t, db); got != "first,second" || sqliteReadVia(path) != sqliteReadCopy {
				t.Fatalf("%s: read %q via %s", journal, got, sqliteReadVia(path))
			}
			entries, _ := os.ReadDir(copies)
			if len(entries) != 1 {
				t.Fatalf("%s: copies %v", journal, entries)
			}
			if info, err := os.Stat(filepath.Join(copies, entries[0].Name())); err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("%s: the copy's folder is %v, %v", journal, info.Mode(), err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			assertNoCopies(t, copies)
			// A capture's snapshot reads through the same ladder.
			snapshot := filepath.Join(t.TempDir(), "snapshot.db")
			if err := snapshotSourceSQLite(context.Background(), path, snapshot); err != nil {
				t.Fatalf("%s: snapshot: %v", journal, err)
			}
			snapshotDB, err := openReadOnlySQLite(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if got := sqliteBodies(t, snapshotDB); got != "first,second" {
				t.Fatalf("%s: snapshot holds %q", journal, got)
			}
			snapshotDB.Close()
			assertNoCopies(t, copies)
			if reads := sqliteFallbackReads(filepath.Dir(path)); len(reads) != 1 || reads[0]["via"] != sqliteReadCopy || reads[0]["cause"] != "locked" {
				t.Fatalf("%s: Settings shows %v", journal, reads)
			}
		}
	})
	t.Run("a write not yet in the main file is left out of the copy", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "uncommitted.db")
		sqliteNotes(t, path, "DELETE")
		sqliteHolder(t, path, `BEGIN EXCLUSIVE`, `INSERT INTO notes VALUES('uncommitted')`)
		db, err := openSourceSQLite(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if got := sqliteBodies(t, db); got != "first" || sqliteReadVia(path) != sqliteReadCopy {
			t.Fatalf("read %q via %s", got, sqliteReadVia(path))
		}
	})
	t.Run("a write half done in the main file is waited for", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "spilled.db")
		sqliteNotes(t, path, "DELETE")
		// A cache this small spills the transaction into the main file,
		// syncing the journal first, so the journal is hot.
		holder, release := sqliteHolder(t, path, `PRAGMA cache_size=2`, `BEGIN EXCLUSIVE`)
		for i := 0; i < 200; i++ {
			if _, err := holder.ExecContext(context.Background(), `INSERT INTO notes VALUES(?)`, strings.Repeat("x", 2000)); err != nil {
				t.Fatal(err)
			}
		}
		if pending, err := sqlitePendingState(path); err != nil || !pending.hot {
			t.Fatalf("%+v, %v: the fixture's transaction did not spill", pending, err)
		}
		db, err := openSourceSQLite(context.Background(), path)
		if err == nil {
			db.Close()
			t.Fatal("a database with a hot journal was read")
		}
		if !errors.Is(err, errSQLiteWriteInProgress) || !strings.Contains(err.Error(), "busy") || sqliteReadVia(path) != sqliteReadWaiting {
			t.Fatalf("error %v via %s", err, sqliteReadVia(path))
		}
		// The copy checks again, for a journal gone hot since.
		if db, err := openSQLiteCopy(context.Background(), path); err == nil {
			db.Close()
			t.Fatal("a database with a hot journal was copied")
		} else if !errors.Is(err, errSQLiteWriteInProgress) {
			t.Fatal(err)
		}
		assertNoCopies(t, copies)
		if _, err := holder.ExecContext(context.Background(), `ROLLBACK`); err != nil {
			t.Fatal(err)
		}
		release()
		db, err = openSourceSQLite(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if got := sqliteBodies(t, db); got != "first" || sqliteReadVia(path) != sqliteReadLive {
			t.Fatalf("after the write: read %q via %s", got, sqliteReadVia(path))
		}
	})
	t.Run("a database changing as it is copied is waited for", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "changing.db")
		sqliteNotes(t, path, "DELETE")
		holder, _ := sqliteHolder(t, path, `PRAGMA locking_mode=EXCLUSIVE`, `INSERT INTO notes VALUES('second')`)
		copied := 0
		override(t, &sqliteCopyHook, func(string) {
			copied++
			if _, err := holder.ExecContext(context.Background(), `INSERT INTO notes VALUES('more')`); err != nil {
				t.Error(err)
			}
		})
		if db, err := openSourceSQLite(context.Background(), path); err == nil {
			db.Close()
			t.Fatal("a copy of a changing database was read")
		} else if !errors.Is(err, errSQLiteWriteInProgress) || copied != sqliteCopyAttempts {
			t.Fatalf("error %v after %d copies", err, copied)
		}
		assertNoCopies(t, copies)
	})
	t.Run("a database in a read-only folder is read from a copy, or on a read-only volume without locks", func(t *testing.T) {
		for _, c := range []struct {
			frames, readOnlyVolume bool
			via, bodies            string
		}{
			{false, false, sqliteReadCopy, "first"},
			{false, true, sqliteReadImmutable, "first"},
			{true, true, sqliteReadCopy, "first,second"},
		} {
			frames := c.frames
			override(t, &sqliteReadOnlyVolume, func(string) bool { return c.readOnlyVolume })
			dir := filepath.Join(t.TempDir(), "readonly")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "notes.db")
			if frames {
				// Copied while its writer is open, the WAL holds a commit
				// and no shared-memory file comes with it.
				live := filepath.Join(t.TempDir(), "live.db")
				sqliteNotes(t, live, "WAL")
				sqliteHolder(t, live, `PRAGMA wal_autocheckpoint=0`, `INSERT INTO notes VALUES('second')`)
				for _, suffix := range []string{"", "-wal"} {
					if err := copyFileContext(context.Background(), live+suffix, path+suffix); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				// Closed, the database leaves no WAL, and SQLite cannot
				// create the shared-memory file it needs to read it.
				sqliteNotes(t, path, "WAL")
			}
			if err := os.Chmod(dir, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(dir, 0o755) })
			db, err := openSourceSQLite(context.Background(), path)
			if err != nil {
				t.Fatalf("%+v: %v", c, err)
			}
			if got := sqliteBodies(t, db); got != c.bodies || sqliteReadVia(path) != c.via {
				t.Fatalf("%+v: read %q via %s", c, got, sqliteReadVia(path))
			}
			db.Close()
			assertNoCopies(t, copies)
		}
	})
	t.Run("a rollback-journal database locked with no journal on disk is waited for", func(t *testing.T) {
		// Without a journal on disk, the holder's writes reach the main
		// file with nothing to undo them, so no copy is known to be whole.
		path := filepath.Join(t.TempDir(), "unjournaled.db")
		sqliteNotes(t, path, "DELETE")
		sqliteHolder(t, path, `PRAGMA journal_mode=MEMORY`, `PRAGMA locking_mode=EXCLUSIVE`, `INSERT INTO notes VALUES('second')`)
		if _, err := os.Stat(path + "-journal"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the fixture keeps a journal on disk: %v", err)
		}
		if db, err := openSourceSQLite(context.Background(), path); err == nil {
			db.Close()
			t.Fatal("a database written without a journal was read from a copy")
		} else if !errors.Is(err, errSQLiteWriteInProgress) || sqliteReadVia(path) != sqliteReadWaiting {
			t.Fatalf("error %v via %s", err, sqliteReadVia(path))
		}
		assertNoCopies(t, copies)
	})
	t.Run("a change a coarse clock hides is seen", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "coarse.db")
		sqliteNotes(t, path, "DELETE")
		changed := 0
		override(t, &sqliteCopyHook, func(string) {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			// The same size and time: only the header's change counter moves.
			_, release := sqliteHolder(t, path, `UPDATE notes SET body=body||'.'`)
			release()
			if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			if after, err := os.Stat(path); err != nil || after.Size() != info.Size() {
				t.Fatalf("the update changed the size: %v", err)
			}
			changed++
		})
		if db, err := openSQLiteCopy(context.Background(), path); err == nil {
			db.Close()
			t.Fatal("a copy of a changing database was read")
		} else if !errors.Is(err, errSQLiteWriteInProgress) || changed != sqliteCopyAttempts {
			t.Fatalf("error %v after %d changes", err, changed)
		}
		assertNoCopies(t, copies)
	})
	t.Run("copies whose process ended are removed", func(t *testing.T) {
		ended := exec.Command("true")
		if err := ended.Run(); err != nil {
			t.Fatal(err)
		}
		left := filepath.Join(copies, strconv.Itoa(ended.Process.Pid)+"-left")
		own := filepath.Join(copies, strconv.Itoa(os.Getpid())+"-open")
		for _, dir := range []string{left, own} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		sweepSQLiteCopies()
		if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("an ended process's copy is left: %v", err)
		}
		if _, err := os.Stat(own); err != nil {
			t.Fatalf("this process's copy was removed: %v", err)
		}
		os.RemoveAll(own)
	})
	t.Run("a missing database fails as before", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.db")
		if db, err := openSourceSQLite(context.Background(), path); err == nil {
			db.Close()
			t.Fatal("a missing database opened")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the open created the database: %v", err)
		}
		assertNoCopies(t, copies)
	})
}

// A harness that keeps its database locked is still indexed and captured,
// and Settings says how it was read until it is read in place again.
func TestLockedSourceIsIndexedAndCapturedFromACopy(t *testing.T) {
	copies := sqliteLadderTest(t)
	useHost(t, "host-a")
	root := zedFixture(t)
	source := zedSource(root)
	database := filepath.Join(root, filepath.FromSlash(zedDatabase))
	_, release := sqliteHolder(t, database, `PRAGMA locking_mode=EXCLUSIVE`, `INSERT INTO credentials VALUES('held', 'by Zed')`)
	c, config := testCatalog(t)
	config.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	messages := func(c *Catalog) int {
		return countRows(t, c, `SELECT COUNT(*) FROM messages m JOIN conversations v ON v.id=m.conversation_id WHERE v.provider='zed'`)
	}
	live := messages(c)
	if live == 0 {
		t.Fatal("nothing was indexed from the locked database")
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	if strings.Contains(capturedText(t, config.CaptureRoot), zedSecret) {
		t.Fatal("the capture of a copy holds what Zed keeps beside its threads")
	}
	assertNoCopies(t, copies)
	if reads := sqliteFallbackReads(root); len(reads) != 1 || reads[0]["via"] != sqliteReadCopy {
		t.Fatalf("Settings shows %v", reads)
	}
	release()
	zedWrite(t, root, func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE threads SET summary=summary||' (renamed)' WHERE rowid=(SELECT min(rowid) FROM threads)`)
		return err
	})
	ingestSource(t, c, source)
	if reads := sqliteFallbackReads(root); len(reads) != 0 {
		t.Fatalf("after a read in place, Settings shows %v", reads)
	}
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if captured := messages(other); captured != live {
		t.Fatalf("captured %d messages, live %d", captured, live)
	}

	// A kind captured whole, with SQLite's backup, reads through the
	// ladder too.
	useHost(t, "host-a")
	dir := filepath.Join(t.TempDir(), "conductor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	conductor := filepath.Join(dir, "conductor.db")
	writer, err := sql.Open("sqlite", conductor)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range append([]string{"CREATE TABLE sessions(id TEXT PRIMARY KEY, title TEXT, created_at TEXT, updated_at TEXT)",
		"CREATE TABLE session_messages(id TEXT PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, created_at TEXT, sent_at TEXT, cancelled_at TEXT)"},
		addConductorSession("s1", 2)...) {
		if _, err := writer.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()
	sqliteHolder(t, conductor, `PRAGMA locking_mode=EXCLUSIVE`, `INSERT INTO sessions VALUES('held','Held','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`)
	whole := SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true}
	_, wholeConfig := testCatalog(t)
	wholeConfig.Sources = []SourceConfig{whole}
	if result := runCapture(t, wholeConfig); !result.OK {
		t.Fatalf("capture of a locked Conductor database = %+v", result)
	}
	assertNoCopies(t, copies)
	useHost(t, "host-b")
	fromWhole, _ := testCatalog(t)
	indexCaptures(t, fromWhole, wholeConfig.CaptureRoot, "host-a")
	if got := conversationMessages(t, fromWhole, "conductor:s1"); got != 2 {
		t.Fatalf("captured Conductor session holds %d messages", got)
	}
}
