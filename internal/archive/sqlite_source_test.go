package archive

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sqliteLadderTest lowers the live read's wait and keeps copies in a folder
// the test can see empty again.
func sqliteLadderTest(t *testing.T) string {
	t.Helper()
	override(t, &sourceBusyTimeoutMS, 20)
	copies := t.TempDir()
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
	if err != nil {
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
		db, err := openSourceSQLite(path)
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
				if hot, frames, _ := sqlitePending(path); hot || !frames {
					t.Fatalf("%s: hot %v, frames %v: the fixture has no commit in its WAL", journal, hot, frames)
				}
			}
			db, err := openSourceSQLite(path)
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
			if err := snapshotSQLite(context.Background(), path, snapshot); err != nil {
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
			if reads := sqliteFallbackReads(filepath.Dir(path)); len(reads) != 1 || reads[0]["via"] != sqliteReadCopy {
				t.Fatalf("%s: Settings shows %v", journal, reads)
			}
		}
	})
	t.Run("a write not yet in the main file is left out of the copy", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "uncommitted.db")
		sqliteNotes(t, path, "DELETE")
		sqliteHolder(t, path, `BEGIN EXCLUSIVE`, `INSERT INTO notes VALUES('uncommitted')`)
		db, err := openSourceSQLite(path)
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
		if hot, _, err := sqlitePending(path); err != nil || !hot {
			t.Fatalf("hot %v, %v: the fixture's transaction did not spill", hot, err)
		}
		db, err := openSourceSQLite(path)
		if err == nil {
			db.Close()
			t.Fatal("a database with a hot journal was read")
		}
		if !errors.Is(err, errSQLiteWriteInProgress) || !strings.Contains(err.Error(), "busy") || sqliteReadVia(path) != sqliteReadWaiting {
			t.Fatalf("error %v via %s", err, sqliteReadVia(path))
		}
		assertNoCopies(t, copies)
		if _, err := holder.ExecContext(context.Background(), `ROLLBACK`); err != nil {
			t.Fatal(err)
		}
		release()
		db, err = openSourceSQLite(path)
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
		if db, err := openSourceSQLite(path); err == nil {
			db.Close()
			t.Fatal("a copy of a changing database was read")
		} else if !errors.Is(err, errSQLiteWriteInProgress) || copied != sqliteCopyAttempts {
			t.Fatalf("error %v after %d copies", err, copied)
		}
		assertNoCopies(t, copies)
	})
	t.Run("a database in a read-only folder is read without locks or from a copy", func(t *testing.T) {
		for _, frames := range []bool{false, true} {
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
					if err := copyFilePlain(live+suffix, path+suffix); err != nil {
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
			db, err := openSourceSQLite(path)
			if err != nil {
				t.Fatalf("frames %v: %v", frames, err)
			}
			want := map[bool]string{false: sqliteReadImmutable, true: sqliteReadCopy}[frames]
			bodies := map[bool]string{false: "first", true: "first,second"}[frames]
			if got := sqliteBodies(t, db); got != bodies || sqliteReadVia(path) != want {
				t.Fatalf("frames %v: read %q via %s", frames, got, sqliteReadVia(path))
			}
			db.Close()
			assertNoCopies(t, copies)
		}
	})
	t.Run("a missing database fails as before", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.db")
		if db, err := openSourceSQLite(path); err == nil {
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
	_, release := sqliteHolder(t, database, `PRAGMA locking_mode=EXCLUSIVE`, `BEGIN EXCLUSIVE`, `COMMIT`)
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
}
