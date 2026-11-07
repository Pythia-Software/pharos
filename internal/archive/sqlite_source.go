package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"modernc.org/sqlite"
)

// openSourceSQLite opens a harness's database read-only, stepping down a
// ladder when the harness keeps it from being read in place
// (docs/technical-overview.md, "Reading a locked database"):
//
//  1. live: mode=ro and query_only, waiting out a lock for busy_timeout;
//  2. immutable: on a read-only volume, which nothing can change, when
//     SQLite cannot set up its shared-memory file and no change is pending
//     beside the database (no WAL frames, no hot journal);
//  3. copy: otherwise the database and its WAL are copied to a private
//     folder, the source is checked to be unchanged across the copy, and the
//     copy is read.
//
// It waits instead, failing as busy so that the reader tries again, when no
// copy can be shown consistent. A hot rollback journal means a write is half
// done in the main file. And a rollback-journal database held locked may be
// mid-write with nothing on disk to show it: its holder can spill a
// transaction into the main file without journaling it (journal_mode MEMORY
// or OFF), and in exclusive locking mode SQLite stops counting its commits.
// A WAL database's holder writes only whole commits to the main file, by
// checkpoint, so a WAL database is copied.
// Within one pass over a source (withSQLiteCopyShare), a database copied
// once is read again from that copy, without waiting for the lock again.
// Pharos's own catalog and captures are opened with openReadOnlySQLite,
// never copied.
func openSourceSQLite(ctx context.Context, path string) (*sql.DB, error) {
	sweepSQLiteCopiesOnce.Do(sweepSQLiteCopies)
	share := sqliteCopyShareOf(ctx)
	if shared, ok := share.lookup(path); ok {
		if db, err := openCopiedSQLite(ctx, shared.file, share); err == nil {
			noteSQLiteRead(path, shared.read)
			return db, nil
		}
		share.drop(path)
	}
	db, err := openPinnedSQLite(ctx, sqliteURI(path)+fmt.Sprintf("?mode=ro&_pragma=busy_timeout(%d)&_pragma=query_only(1)", sourceBusyTimeoutMS))
	if err == nil {
		noteSQLiteRead(path, sqliteRead{Via: sqliteReadLive})
		return db, nil
	}
	code := sqliteCode(err)
	locked := code == 5 || code == 6
	if !locked && code != 8 && code != 14 || ctx.Err() != nil {
		return nil, err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, err
	}
	// SQLite resolves a symlinked database, and finds its WAL and journal
	// beside the file the link names; so does the rest of the ladder.
	resolved, resolveErr := filepath.EvalSymlinks(path)
	if resolveErr != nil {
		return nil, errors.Join(err, resolveErr)
	}
	cause := map[bool]string{true: "locked", false: "unreadable"}[locked]
	pending, stateErr := sqlitePendingState(resolved)
	if stateErr != nil {
		return nil, errors.Join(err, stateErr)
	}
	if pending.hot || locked && pending.rollback {
		noteSQLiteRead(path, sqliteRead{Via: sqliteReadWaiting, Cause: cause, Reason: err.Error()})
		return nil, fmt.Errorf("%s: %w", path, errSQLiteWriteInProgress)
	}
	if !locked && !pending.frames && sqliteReadOnlyVolume(resolved) {
		if db, immutableErr := openPinnedSQLite(ctx, sqliteURI(resolved)+"?mode=ro&immutable=1&_pragma=query_only(1)"); immutableErr == nil {
			noteSQLiteRead(path, sqliteRead{Via: sqliteReadImmutable, Cause: cause, Reason: err.Error()})
			return db, nil
		}
	}
	db, copied, copyErr := openSQLiteCopy(ctx, resolved, locked, share)
	if copyErr != nil {
		via := sqliteReadFailed
		if errors.Is(copyErr, errSQLiteWriteInProgress) {
			via = sqliteReadWaiting
		}
		noteSQLiteRead(path, sqliteRead{Via: via, Cause: cause, Reason: copyErr.Error()})
		return nil, errors.Join(err, copyErr)
	}
	read := sqliteRead{Via: sqliteReadCopy, Cause: cause, Reason: err.Error()}
	share.keep(path, sqliteSharedCopy{file: copied, read: read})
	noteSQLiteRead(path, read)
	return db, nil
}

// sqliteCopyShare is one pass over a source: its discovery and every group
// it reads. The copies made in a pass stay until it ends, and each later
// open of the same database in the pass reads its copy, rather than waiting
// out the lock and copying the whole database and WAL again for each group.
// A copy is made after the pass's discovery began, so a group read from it
// is never older than what discovery planned for it; a write since leaves a
// change the next pass reads. A pass opens its databases one at a time (its
// groups are read in turn), so two opens never race to copy one database.
type sqliteCopyShare struct {
	mu     sync.Mutex
	copies map[string]sqliteSharedCopy // by the path opened
	ended  bool
}

type sqliteSharedCopy struct {
	file string     // the copied database
	read sqliteRead // how it came to be copied, for Settings
}

type sqliteCopyShareKey struct{}

// withSQLiteCopyShare begins a pass whose opens of a source's databases
// share their copies, unless ctx is in one already; end removes the pass's
// copies, and must run when the pass ends, however it ends.
func withSQLiteCopyShare(ctx context.Context) (shared context.Context, end func()) {
	if share := sqliteCopyShareOf(ctx); share != nil {
		return ctx, func() {}
	}
	share := &sqliteCopyShare{copies: map[string]sqliteSharedCopy{}}
	return context.WithValue(ctx, sqliteCopyShareKey{}, share), share.end
}

// sqliteCopyShareOf is ctx's pass, or nil once it has ended.
func sqliteCopyShareOf(ctx context.Context) *sqliteCopyShare {
	share, _ := ctx.Value(sqliteCopyShareKey{}).(*sqliteCopyShare)
	if share == nil {
		return nil
	}
	share.mu.Lock()
	defer share.mu.Unlock()
	if share.ended {
		return nil
	}
	return share
}

func (s *sqliteCopyShare) lookup(path string) (sqliteSharedCopy, bool) {
	if s == nil {
		return sqliteSharedCopy{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	shared, ok := s.copies[path]
	return shared, ok
}

// keep has the pass own a copy, which then outlives its database. A copy
// the pass does not keep, because it has ended or holds one already, stays
// its database's, removed when that closes.
func (s *sqliteCopyShare) keep(path string, shared sqliteSharedCopy) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.copies[path]; !ok && !s.ended {
		s.copies[path] = shared
	}
}

// holds says whether the pass owns the copy in dir.
func (s *sqliteCopyShare) holds(dir string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, shared := range s.copies {
		if filepath.Dir(shared.file) == dir {
			return true
		}
	}
	return false
}

// drop removes a copy that can no longer be opened.
func (s *sqliteCopyShare) drop(path string) {
	s.mu.Lock()
	shared, ok := s.copies[path]
	delete(s.copies, path)
	s.mu.Unlock()
	if ok {
		os.RemoveAll(filepath.Dir(shared.file))
	}
}

func (s *sqliteCopyShare) end() {
	s.mu.Lock()
	copies := s.copies
	s.copies, s.ended = nil, true
	s.mu.Unlock()
	for _, shared := range copies {
		os.RemoveAll(filepath.Dir(shared.file))
	}
}

// errSQLiteWriteInProgress says "busy" so that the readers' retry loops,
// which match "busy" and "locked", try it again.
var errSQLiteWriteInProgress = errors.New("database is busy: another app is writing it")

// sourceBusyTimeoutMS is how long a live read waits for a harness's lock
// before it steps down to a copy.
var sourceBusyTimeoutMS = 5000

// sqliteCopyRoot holds the copies, in a private folder that must be on the
// source's own disk (copySQLiteSource): a copy holds everything the harness
// keeps, credentials included, so it never goes to a library drive. A copy
// is removed when its database closes, or its pass ends (sqliteCopyShare),
// and one whose process ended on the next open in another process.
var sqliteCopyRoot = filepath.Join(os.TempDir(), "pharos-sqlite-copies")

// sqliteCopyAttempts bounds the copies of a source that changes while it is
// copied; sqliteCopyHook runs between a copy and its check, for tests.
var (
	sqliteCopyAttempts = 3
	sqliteCopyHook     func(path string)
)

// sqliteReadOnlyVolume says whether path is on a volume mounted read-only
// (MNT_RDONLY, ST_RDONLY), the one place an unlocked read cannot see a write.
var sqliteReadOnlyVolume = func(path string) bool {
	var stat syscall.Statfs_t
	return syscall.Statfs(path, &stat) == nil && stat.Flags&1 != 0
}

func sqliteURI(path string) string { return (&url.URL{Scheme: "file", Path: path}).String() }

// openPinnedSQLite opens dsn and reads its schema, so a database that
// cannot be read fails here, not at its reader's first query.
func openPinnedSQLite(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := pinSQLite(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func pinSQLite(ctx context.Context, db *sql.DB) error {
	var tables int
	return db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&tables)
}

// sqliteCode is err's primary SQLite result code, or 0.
func sqliteCode(err error) int {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code() & 0xff
	}
	return 0
}

// sqlitePending is what a database's files say about changes not yet in its
// main file.
type sqlitePending struct {
	rollback bool // the header names a rollback journal, not a WAL
	journal  bool // a -journal file is present
	hot      bool // and SQLite has not zeroed or truncated its header
	frames   bool // the WAL is longer than its 32-byte header
	// counter is the main header's change counter, which a rollback-journal
	// commit increments; wal is the WAL header's checkpoint sequence and
	// salts, which a WAL restart changes. They catch a change a coarse mtime
	// hides.
	counter, wal string
}

func sqlitePendingState(path string) (sqlitePending, error) {
	var pending sqlitePending
	header, err := readHead(path, 28)
	if err != nil {
		return pending, err
	}
	if len(header) == 28 {
		pending.rollback = header[18] == 1
		pending.counter = string(header[24:28])
	}
	wal, err := readHead(path+"-wal", 32)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return pending, err
	}
	pending.wal = string(wal)
	if info, err := os.Stat(path + "-wal"); err == nil {
		pending.frames = info.Size() > 32
	}
	journal, err := readHead(path+"-journal", 1)
	if errors.Is(err, os.ErrNotExist) {
		return pending, nil
	}
	if err != nil {
		return pending, err
	}
	pending.journal, pending.hot = true, len(journal) == 1 && journal[0] != 0
	return pending, nil
}

// openSQLiteCopy reads path from a copy of it and its WAL, made while
// neither changed. The copy is opened read-write (query_only) so SQLite can
// fold the WAL into it, which a read-only open of a copy without its
// shared-memory file could not. Closing the database removes the copy,
// unless share keeps it; copied is the copy's database.
func openSQLiteCopy(ctx context.Context, path string, locked bool, share *sqliteCopyShare) (*sql.DB, string, error) {
	sweepSQLiteCopies()
	for attempt := 0; attempt < sqliteCopyAttempts; attempt++ {
		dir, err := copySQLiteSource(ctx, path, locked)
		if errors.Is(err, errSQLiteSourceMoved) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		copied := filepath.Join(dir, filepath.Base(path))
		db, err := openCopiedSQLite(ctx, copied, share)
		return db, copied, err
	}
	return nil, "", fmt.Errorf("%s: %w", path, errSQLiteWriteInProgress)
}

// openCopiedSQLite opens a copy openSQLiteCopy made; closing it removes the
// copy unless share holds it.
func openCopiedSQLite(ctx context.Context, file string, share *sqliteCopyShare) (*sql.DB, error) {
	dsn := sqliteURI(file) + fmt.Sprintf("?mode=rw&_pragma=busy_timeout(%d)&_pragma=query_only(1)", sourceBusyTimeoutMS)
	db := sql.OpenDB(sqliteCopyConnector{dsn: dsn, dir: filepath.Dir(file), share: share})
	db.SetMaxOpenConns(1)
	if err := pinSQLite(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

var errSQLiteSourceMoved = errors.New("the database changed while it was copied")

// sqliteCopyState is what must not change across a copy.
type sqliteCopyState struct {
	files   sqliteSourceState
	pending sqlitePending
}

func sqliteCopyStateOf(path string) (sqliteCopyState, error) {
	files, err := sqliteDependencyState(path)
	if err != nil {
		return sqliteCopyState{}, err
	}
	pending, err := sqlitePendingState(path)
	return sqliteCopyState{files: files, pending: pending}, err
}

// copySQLiteSource refuses what openSourceSQLite waits for, again on each
// attempt: a holder can change its journal mode between them.
func copySQLiteSource(ctx context.Context, path string, locked bool) (string, error) {
	before, err := sqliteCopyStateOf(path)
	if err != nil {
		return "", err
	}
	if before.pending.hot || locked && before.pending.rollback {
		return "", fmt.Errorf("%s: %w", path, errSQLiteWriteInProgress)
	}
	if err := os.MkdirAll(sqliteCopyRoot, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(sqliteCopyRoot, 0o700); err != nil {
		return "", err
	}
	if !sameDevice(sqliteCopyRoot, path) {
		return "", fmt.Errorf("the folder for copies, %s, is not on the disk %s is on", sqliteCopyRoot, path)
	}
	dir, err := os.MkdirTemp(sqliteCopyRoot, strconv.Itoa(os.Getpid())+"-")
	if err != nil {
		return "", err
	}
	copied := filepath.Join(dir, filepath.Base(path))
	err = copyFileContext(ctx, path, copied)
	if err == nil && before.files.WALSize > 0 {
		err = copyFileContext(ctx, path+"-wal", copied+"-wal")
	}
	if err == nil && sqliteCopyHook != nil {
		sqliteCopyHook(path)
	}
	if err == nil {
		var after sqliteCopyState
		after, err = sqliteCopyStateOf(path)
		if err == nil && after != before {
			err = errSQLiteSourceMoved
		}
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func copyFileContext(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := cloudCopy(ctx, out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

var sweepSQLiteCopiesOnce sync.Once

// sweepSQLiteCopies removes the copies of processes that have ended. Each
// copy's folder is named for the process that made it; a process with that
// ID that started after the folder was made, this one included, has reused
// the ID.
func sweepSQLiteCopies() {
	entries, err := os.ReadDir(sqliteCopyRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name, _, _ := strings.Cut(entry.Name(), "-")
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		ended := syscall.Kill(pid, 0) == syscall.ESRCH
		if !ended {
			started, known := processStartTime(pid)
			info, err := entry.Info()
			ended = known && err == nil && started.After(info.ModTime())
		}
		if ended {
			os.RemoveAll(filepath.Join(sqliteCopyRoot, entry.Name()))
		}
	}
}

// sqliteCopyConnector opens a copy's connections; database/sql closes it
// after them, when the database closes, and so removes the copy, unless a
// pass holds it.
type sqliteCopyConnector struct {
	dsn, dir string
	share    *sqliteCopyShare
}

func (c sqliteCopyConnector) Connect(context.Context) (driver.Conn, error) {
	return sqliteDriver().Open(c.dsn)
}
func (c sqliteCopyConnector) Driver() driver.Driver { return sqliteDriver() }
func (c sqliteCopyConnector) Close() error {
	if c.share.holds(c.dir) {
		return nil
	}
	return os.RemoveAll(c.dir)
}

// How a source database was last read, for Settings. Only reads that
// stepped down the ladder are kept; a live read clears its path. Cause is
// why it could not be read in place: "locked" or "unreadable".
const (
	sqliteReadLive      = "live"
	sqliteReadImmutable = "immutable"
	sqliteReadCopy      = "copy"
	sqliteReadWaiting   = "waiting"
	sqliteReadFailed    = "failed"
)

type sqliteRead struct {
	Via, Cause, Reason string
	At                 time.Time
}

var sqliteReads sync.Map

func noteSQLiteRead(path string, read sqliteRead) {
	if read.Via == sqliteReadLive {
		sqliteReads.Delete(path)
		return
	}
	read.At = time.Now().UTC()
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
			reads = append(reads, map[string]any{"path": path, "via": read.Via, "cause": read.Cause, "at": read.At.Format(time.RFC3339Nano), "reason": read.Reason})
		}
		return true
	})
	sort.Slice(reads, func(i, j int) bool { return reads[i]["at"].(string) > reads[j]["at"].(string) })
	return reads
}

// openReadOnlySQLite opens a database Pharos owns or has already copied (its
// catalog, a backup, a snapshot generation capture indexing materialized)
// read-only, never down the locked-database ladder.
func openReadOnlySQLite(path string) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
