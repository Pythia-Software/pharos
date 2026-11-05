package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
)

// openSourceSQLite opens a harness's database read-only, stepping down a
// ladder when the harness keeps it from being read in place
// (docs/technical-overview.md, "Reading a locked database"):
//
//  1. live: mode=ro and query_only, waiting out a lock for busy_timeout;
//  2. immutable: when SQLite cannot set up its shared-memory sidecar (a
//     read-only folder or volume) and no change is pending beside the
//     database (no WAL frames, no hot journal), it is opened without locks;
//  3. copy: otherwise the database and its WAL are copied to a private
//     folder, the source is checked to be unchanged across the copy, and the
//     copy is read. A held lock always comes here, not to 2: its holder can
//     write at any moment, and only a copy is checked.
//
// A hot rollback journal means a write is half done in the main file, so
// neither 2 nor 3 is consistent; the read fails as busy and is tried again.
// Pharos's own catalog and captures are opened with openReadOnlySQLite,
// never copied.
func openSourceSQLite(path string) (*sql.DB, error) {
	db, err := openPinnedSQLite(sqliteURI(path) + fmt.Sprintf("?mode=ro&_pragma=busy_timeout(%d)&_pragma=query_only(1)", sourceBusyTimeoutMS))
	if err == nil {
		noteSQLiteRead(path, sqliteReadLive, nil)
		return db, nil
	}
	code := sqliteCode(err)
	locked := code == 5 || code == 6
	if !locked && code != 8 && code != 14 {
		return nil, err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, err
	}
	hot, frames, stateErr := sqlitePending(path)
	if stateErr != nil {
		return nil, errors.Join(err, stateErr)
	}
	if hot {
		noteSQLiteRead(path, sqliteReadWaiting, err)
		return nil, fmt.Errorf("%s: %w", path, errSQLiteWriteInProgress)
	}
	if !locked && !frames {
		if db, immutableErr := openPinnedSQLite(sqliteURI(path) + "?mode=ro&immutable=1&_pragma=query_only(1)"); immutableErr == nil {
			noteSQLiteRead(path, sqliteReadImmutable, err)
			return db, nil
		}
	}
	db, copyErr := openSQLiteCopy(path)
	if copyErr != nil {
		noteSQLiteRead(path, sqliteReadWaiting, err)
		return nil, errors.Join(err, copyErr)
	}
	noteSQLiteRead(path, sqliteReadCopy, err)
	return db, nil
}

// errSQLiteWriteInProgress says "busy" so that the readers' retry loops,
// which match "busy" and "locked", try it again.
var errSQLiteWriteInProgress = errors.New("database is busy: another app is writing it")

// sourceBusyTimeoutMS is how long a live read waits for a harness's lock
// before it steps down to a copy.
var sourceBusyTimeoutMS = 5000

// sqliteCopyRoot holds the copies: a private folder on the Mac's own disk,
// never in a capture or the library, since a copy holds everything the
// harness keeps, credentials included. A copy is removed when its database
// closes, and one a crash left behind on the next copy.
var sqliteCopyRoot = os.TempDir()

const sqliteCopyPrefix = "pharos-sqlite-copy-"

// sqliteCopyAttempts bounds the copies of a source that changes while it is
// copied; sqliteCopyHook runs between a copy and its check, for tests.
var (
	sqliteCopyAttempts = 3
	sqliteCopyHook     func(path string)
)

func sqliteURI(path string) string { return (&url.URL{Scheme: "file", Path: path}).String() }

// openPinnedSQLite opens dsn and reads its schema, so a database that
// cannot be read fails here, not at its reader's first query.
func openPinnedSQLite(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := pinSQLite(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func pinSQLite(db *sql.DB) error {
	var tables int
	return db.QueryRow("SELECT count(*) FROM sqlite_schema").Scan(&tables)
}

// sqliteCode is err's primary SQLite result code, or 0.
func sqliteCode(err error) int {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code() & 0xff
	}
	return 0
}

// sqlitePending reports what beside path is not yet in it: a hot rollback
// journal (one whose header SQLite has not zeroed or truncated), and WAL
// frames (a WAL longer than its 32-byte header).
func sqlitePending(path string) (hot, frames bool, err error) {
	if wal, err := os.Stat(path + "-wal"); err == nil {
		frames = wal.Size() > 32
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	}
	journal, err := os.Open(path + "-journal")
	if errors.Is(err, os.ErrNotExist) {
		return false, frames, nil
	}
	if err != nil {
		return false, false, err
	}
	defer journal.Close()
	first := make([]byte, 1)
	if _, err := io.ReadFull(journal, first); err != nil {
		if errors.Is(err, io.EOF) {
			return false, frames, nil
		}
		return false, false, err
	}
	return first[0] != 0, frames, nil
}

// openSQLiteCopy reads path from a copy of it and its WAL, made while
// neither changed. The copy is opened read-write (query_only) so SQLite can
// fold the WAL into it, which a read-only open of a copy without its
// shared-memory file could not. Closing the database removes the copy.
func openSQLiteCopy(path string) (*sql.DB, error) {
	sweepSQLiteCopies()
	for attempt := 0; attempt < sqliteCopyAttempts; attempt++ {
		dir, err := copySQLiteSource(path)
		if errors.Is(err, errSQLiteSourceMoved) {
			continue
		}
		if err != nil {
			return nil, err
		}
		dsn := sqliteURI(filepath.Join(dir, filepath.Base(path))) + fmt.Sprintf("?mode=rw&_pragma=busy_timeout(%d)&_pragma=query_only(1)", sourceBusyTimeoutMS)
		db := sql.OpenDB(sqliteCopyConnector{dsn: dsn, dir: dir})
		db.SetMaxOpenConns(1)
		if err := pinSQLite(db); err != nil {
			db.Close()
			return nil, err
		}
		return db, nil
	}
	return nil, fmt.Errorf("%s: %w", path, errSQLiteWriteInProgress)
}

var errSQLiteSourceMoved = errors.New("the database changed while it was copied")

func copySQLiteSource(path string) (string, error) {
	before, err := sqliteDependencyState(path)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(sqliteCopyRoot, sqliteCopyPrefix)
	if err != nil {
		return "", err
	}
	copied := filepath.Join(dir, filepath.Base(path))
	err = copyFilePlain(path, copied)
	if err == nil && before.WALSize > 0 {
		err = copyFilePlain(path+"-wal", copied+"-wal")
	}
	if err == nil && sqliteCopyHook != nil {
		sqliteCopyHook(path)
	}
	var hot bool
	if err == nil {
		hot, _, err = sqlitePending(path)
	}
	if err == nil {
		var after sqliteSourceState
		after, err = sqliteDependencyState(path)
		if err == nil && (after != before || hot) {
			err = errSQLiteSourceMoved
		}
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func copyFilePlain(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// sqliteCopyMaxAge is how old a copy must be before a later copy removes
// it: no read lasts a day, so an older one was left by a crash.
const sqliteCopyMaxAge = 24 * time.Hour

func sweepSQLiteCopies() {
	entries, err := os.ReadDir(sqliteCopyRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), sqliteCopyPrefix) {
			continue
		}
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > sqliteCopyMaxAge {
			os.RemoveAll(filepath.Join(sqliteCopyRoot, entry.Name()))
		}
	}
}

// sqliteCopyConnector opens a copy's connections; database/sql closes it
// after them, when the database closes, and so removes the copy.
type sqliteCopyConnector struct{ dsn, dir string }

func (c sqliteCopyConnector) Connect(context.Context) (driver.Conn, error) {
	return sqliteDriver().Open(c.dsn)
}
func (c sqliteCopyConnector) Driver() driver.Driver { return sqliteDriver() }
func (c sqliteCopyConnector) Close() error          { return os.RemoveAll(c.dir) }

// How a source database was last read, for Settings. Only reads that
// stepped down the ladder are kept; a live read clears its path.
const (
	sqliteReadLive      = "live"
	sqliteReadImmutable = "immutable"
	sqliteReadCopy      = "copy"
	sqliteReadWaiting   = "waiting"
)

type sqliteRead struct {
	Via    string
	At     time.Time
	Reason string
}

var sqliteReads sync.Map

func noteSQLiteRead(path, via string, reason error) {
	if via == sqliteReadLive {
		sqliteReads.Delete(path)
		return
	}
	read := sqliteRead{Via: via, At: time.Now().UTC()}
	if reason != nil {
		read.Reason = reason.Error()
	}
	sqliteReads.Store(path, read)
}

// sqliteFallbackReads lists the databases under root last read other than
// in place, newest first.
func sqliteFallbackReads(root string) []map[string]any {
	if root == "" {
		return nil
	}
	root = filepath.Clean(root)
	var reads []map[string]any
	sqliteReads.Range(func(key, value any) bool {
		path, read := key.(string), value.(sqliteRead)
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			reads = append(reads, map[string]any{"path": path, "via": read.Via, "at": read.At.Format(time.RFC3339Nano), "reason": read.Reason})
		}
		return true
	})
	sort.Slice(reads, func(i, j int) bool { return reads[i]["at"].(string) > reads[j]["at"].(string) })
	return reads
}
